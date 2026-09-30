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
	parts, leaf, err := v.parseHeaderAndLeaf(compact)
	if err != nil {
		return nil, err
	}
	if err := v.verifyEmbeddedChain(leaf, compact); err != nil {
		return nil, err
	}
	return v.verifyPayloadSignature(parts, leaf)
}

// VerifySignatureWithEmbeddedLeaf checks ES256 using the leaf x5c certificate only.
// Used for sandbox client JWS when the Huawei chain is not anchored in the system CA pool.
func (v *Verifier) VerifySignatureWithEmbeddedLeaf(compact string) ([]byte, error) {
	parts, leaf, err := v.parseHeaderAndLeaf(compact)
	if err != nil {
		return nil, err
	}
	return v.verifyPayloadSignature(parts, leaf)
}

func (v *Verifier) parseHeaderAndLeaf(compact string) ([]string, *x509.Certificate, error) {
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		return nil, nil, errors.New("invalid compact JWS format")
	}
	var header struct {
		Algorithm string   `json:"alg"`
		Type      string   `json:"typ"`
		Chain     []string `json:"x5c"`
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerBytes, &header) != nil {
		return nil, nil, errors.New("invalid JWS header")
	}
	if header.Algorithm != "ES256" || (header.Type != "" && header.Type != "JWT") || len(header.Chain) == 0 {
		return nil, nil, errors.New("unsupported Huawei JWS header")
	}
	der, err := base64.StdEncoding.DecodeString(header.Chain[0])
	if err != nil {
		return nil, nil, errors.New("invalid x5c certificate encoding")
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, fmt.Errorf("parse x5c certificate: %w", err)
	}
	return parts, leaf, nil
}

func (v *Verifier) verifyEmbeddedChain(leaf *x509.Certificate, compact string) error {
	parts := strings.Split(compact, ".")
	var header struct {
		Chain []string `json:"x5c"`
	}
	headerBytes, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(headerBytes, &header)
	certificates := make([]*x509.Certificate, 0, len(header.Chain))
	for _, encoded := range header.Chain {
		der, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return errors.New("invalid x5c certificate encoding")
		}
		certificate, err := x509.ParseCertificate(der)
		if err != nil {
			return fmt.Errorf("parse x5c certificate: %w", err)
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
		return fmt.Errorf("verify Huawei JWS certificate chain: %w", err)
	}
	return nil
}

func (v *Verifier) verifyPayloadSignature(parts []string, leaf *x509.Certificate) ([]byte, error) {
	publicKey, ok := leaf.PublicKey.(*ecdsa.PublicKey)
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
