package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	sessionIssuer   = "onebeat-store-api"
	sessionAudience = "onebeat-app"
)

type SessionClaims struct {
	Subject   string `json:"sub"`
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type SessionManager struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

func NewSessionManager(key []byte, ttl time.Duration) *SessionManager {
	return &SessionManager{key: append([]byte(nil), key...), ttl: ttl, now: time.Now}
}

func (m *SessionManager) Issue(userID string) (string, time.Time, error) {
	now := m.now().UTC()
	expiresAt := now.Add(m.ttl)
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", time.Time{}, err
	}
	claims, err := json.Marshal(SessionClaims{
		Subject: userID, Issuer: sessionIssuer, Audience: sessionAudience,
		IssuedAt: now.Unix(), ExpiresAt: expiresAt.Unix(),
	})
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := encodeSegment(header) + "." + encodeSegment(claims)
	signature := hmacSHA256(m.key, []byte(unsigned))
	return unsigned + "." + encodeSegment(signature), expiresAt, nil
}

func (m *SessionManager) Verify(token string) (SessionClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return SessionClaims{}, errors.New("invalid session token format")
	}
	unsigned := parts[0] + "." + parts[1]
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, hmacSHA256(m.key, []byte(unsigned))) {
		return SessionClaims{}, errors.New("invalid session token signature")
	}

	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
	}
	if err := decodeSegment(parts[0], &header); err != nil || header.Algorithm != "HS256" || header.Type != "JWT" {
		return SessionClaims{}, errors.New("unsupported session token header")
	}
	var claims SessionClaims
	if err := decodeSegment(parts[1], &claims); err != nil {
		return SessionClaims{}, fmt.Errorf("decode session claims: %w", err)
	}
	now := m.now().Unix()
	if claims.Subject == "" || claims.Issuer != sessionIssuer || claims.Audience != sessionAudience ||
		claims.ExpiresAt <= now || claims.IssuedAt > now+300 {
		return SessionClaims{}, errors.New("invalid or expired session token")
	}
	return claims, nil
}

func encodeSegment(value []byte) string {
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeSegment(segment string, target interface{}) error {
	decoded, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(decoded, target)
}

func hmacSHA256(key []byte, value []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
