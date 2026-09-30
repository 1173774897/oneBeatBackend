package security

import (
	"bytes"
	"strings"
	"testing"
)

func TestEncryptTokenUsesRandomAuthenticatedCiphertext(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	plaintext := "purchase-token-that-must-not-be-stored-in-plain-text"
	first, err := EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt first token: %v", err)
	}
	second, err := EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt second token: %v", err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("AES-GCM ciphertext reused a nonce")
	}
	if strings.Contains(string(first), plaintext) {
		t.Fatal("ciphertext contains plaintext token")
	}
}

func TestDecryptTokenRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	plaintext := "purchase-token-round-trip"
	ciphertext, err := EncryptToken(key, plaintext)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	decrypted, err := DecryptToken(key, ciphertext)
	if err != nil {
		t.Fatalf("decrypt token: %v", err)
	}
	if decrypted != plaintext {
		t.Fatalf("decrypted = %q, want %q", decrypted, plaintext)
	}
}

func TestHMACPreservesIdentifierCase(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	if bytes.Equal(HMACSHA256(key, "UnionId"), HMACSHA256(key, "unionid")) {
		t.Fatal("case-sensitive Huawei identifiers must not be normalized")
	}
}
