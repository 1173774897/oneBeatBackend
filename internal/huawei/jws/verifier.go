package jws

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

type Verifier struct {
	roots *x509.CertPool
	now   func() time.Time
}

func NewVerifier() (*Verifier, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system certificate pool: %w", err)
	}
	return &Verifier{roots: roots, now: time.Now}, nil
}

// Verify validates the ES256 signature and the x5c chain before returning the payload.
func (v *Verifier) Verify(compact string) ([]byte, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid compact JWS format")
	}
	var header struct {
		Algorithm string   `json:"alg"`
		Type      string   `json:"typ"`
		Chain     []string `json:"x5c"`
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerBytes, &header) != nil {
		return nil, errors.New("invalid JWS header")
	}
	if header.Algorithm != "ES256" || (header.Type != "" && header.Type != "JWT") || len(header.Chain) == 0 {
		return nil, errors.New("unsupported Huawei JWS header")
	}

	certificates := make([]*x509.Certificate, 0, len(header.Chain))
	for _, encoded := range header.Chain {
		der, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("invalid x5c certificate encoding")
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, fmt.Errorf("parse x5c certificate: %w", err)
		}
		certificates = append(certificates, certificate)
	}
	intermediates := x509.NewCertPool()
	for _, certificate := range certificates[1:] {
		intermediates.AddCert(certificate)
	}
	if _, err := certificates[0].Verify(x509.VerifyOptions{
		Roots:         v.roots,
		Intermediates: intermediates,
		CurrentTime:   v.now(),
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}); err != nil {
		return nil, fmt.Errorf("verify Huawei JWS certificate chain: %w", err)
	}
	publicKey, ok := certificates[0].PublicKey.(*ecdsa.PublicKey)
	if !ok || publicKey.Curve.Params().Name != "P-256" {
		return nil, errors.New("Huawei JWS leaf certificate is not an ECDSA P-256 key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return nil, errors.New("invalid ES256 JWS signature")
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(publicKey, hash[:], r, s) {
		return nil, errors.New("Huawei JWS signature verification failed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("invalid JWS payload encoding")
	}
	return payload, nil
}
