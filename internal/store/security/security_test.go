package security

import (
	"bytes"
	"testing"
)

func TestHMACPreservesIdentifierCase(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	if bytes.Equal(HMACSHA256(key, "UnionId"), HMACSHA256(key, "unionid")) {
		t.Fatal("case-sensitive Huawei identifiers must not be normalized")
	}
}
