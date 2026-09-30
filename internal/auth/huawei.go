package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	huaweiJWKSURL   = "https://oauth-login.cloud.huawei.com/oauth2/v3/certs"
	huaweiIssuer    = "https://accounts.huawei.com"
	jwksCacheTTL    = 12 * time.Hour
	maximumJWKSBody = 1 << 20
)

type HuaweiIdentity struct {
	UnionID string
	Subject string
}

type jwk struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	Modulus   string `json:"n"`
	Exponent  string `json:"e"`
}

type HuaweiIDVerifier struct {
	client   *http.Client
	clientID string
	now      func() time.Time

	mu        sync.RWMutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewHuaweiIDVerifier(client *http.Client, clientID string) *HuaweiIDVerifier {
	return &HuaweiIDVerifier{client: client, clientID: clientID, now: time.Now}
}

func (v *HuaweiIDVerifier) Verify(ctx context.Context, token string) (HuaweiIdentity, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return HuaweiIdentity{}, errors.New("invalid Huawei ID token format")
	}
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
		Type      string `json:"typ"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return HuaweiIdentity{}, fmt.Errorf("decode Huawei ID token header: %w", err)
	}
	if (header.Algorithm != "PS256" && header.Algorithm != "RS256") || header.KeyID == "" {
		return HuaweiIdentity{}, errors.New("unsupported Huawei ID token algorithm")
	}

	key, err := v.key(ctx, header.KeyID)
	if err != nil {
		return HuaweiIdentity{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return HuaweiIdentity{}, errors.New("invalid Huawei ID token signature encoding")
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if header.Algorithm == "PS256" {
		err = rsa.VerifyPSS(key, crypto.SHA256, hash[:], signature, &rsa.PSSOptions{
			SaltLength: rsa.PSSSaltLengthEqualsHash,
			Hash:       crypto.SHA256,
		})
	} else {
		err = rsa.VerifyPKCS1v15(key, crypto.SHA256, hash[:], signature)
	}
	if err != nil {
		return HuaweiIdentity{}, errors.New("Huawei ID token signature verification failed")
	}

	var claims struct {
		Issuer    string          `json:"iss"`
		Audience  json.RawMessage `json:"aud"`
		ExpiresAt int64           `json:"exp"`
		IssuedAt  int64           `json:"iat"`
		Subject   string          `json:"sub"`
		UnionID   string          `json:"unionID"`
		UnionID2  string          `json:"unionId"`
		UnionID3  string          `json:"union_id"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return HuaweiIdentity{}, fmt.Errorf("decode Huawei ID token claims: %w", err)
	}
	now := v.now().Unix()
	if claims.Issuer != huaweiIssuer || !audienceContains(claims.Audience, v.clientID) ||
		claims.ExpiresAt <= now || claims.IssuedAt > now+300 || claims.Subject == "" {
		return HuaweiIdentity{}, errors.New("Huawei ID token claims verification failed")
	}
	unionID := firstNonEmpty(claims.UnionID, claims.UnionID2, claims.UnionID3)
	return HuaweiIdentity{UnionID: unionID, Subject: claims.Subject}, nil
}

func (v *HuaweiIDVerifier) key(ctx context.Context, keyID string) (*rsa.PublicKey, error) {
	v.mu.RLock()
	key := v.keys[keyID]
	fresh := v.now().Sub(v.fetchedAt) < jwksCacheTTL
	v.mu.RUnlock()
	if key != nil && fresh {
		return key, nil
	}
	if err := v.refresh(ctx); err != nil {
		return nil, err
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	key = v.keys[keyID]
	if key == nil {
		return nil, errors.New("Huawei ID token key is not present in current JWKS")
	}
	return key, nil
}

func (v *HuaweiIDVerifier) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, huaweiJWKSURL, nil)
	if err != nil {
		return err
	}
	response, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch Huawei JWKS: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch Huawei JWKS: HTTP %d", response.StatusCode)
	}
	var document struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maximumJWKSBody)).Decode(&document); err != nil {
		return fmt.Errorf("decode Huawei JWKS: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey)
	for _, item := range document.Keys {
		if item.KeyType != "RSA" || item.KeyID == "" || item.Modulus == "" || item.Exponent == "" {
			continue
		}
		key, err := rsaKey(item.Modulus, item.Exponent)
		if err == nil {
			keys[item.KeyID] = key
		}
	}
	if len(keys) == 0 {
		return errors.New("Huawei JWKS contained no usable RSA keys")
	}
	v.mu.Lock()
	v.keys = keys
	v.fetchedAt = v.now()
	v.mu.Unlock()
	return nil
}

func rsaKey(modulus string, exponent string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(modulus)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(exponent)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, value := range eBytes {
		e = e<<8 + int(value)
	}
	if e < 3 {
		return nil, errors.New("invalid RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
}

func audienceContains(raw json.RawMessage, expected string) bool {
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return single == expected
	}
	var multiple []string
	if json.Unmarshal(raw, &multiple) != nil {
		return false
	}
	for _, audience := range multiple {
		if audience == expected {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
