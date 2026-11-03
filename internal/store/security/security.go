package security

import (
	"crypto/hmac"
	"crypto/sha256"
)

// HMACSHA256 derives the stored account identifier from a Huawei UnionID, so
// the server never keeps the identifier itself.
func HMACSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}
