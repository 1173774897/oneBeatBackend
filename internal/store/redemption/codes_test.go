package redemption

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCodeBookMatchesNFKCEquivalentInput(t *testing.T) {
	pepper := []byte("0123456789abcdef0123456789abcdef")
	canonical := "中文ＡＢＣ"
	normalized, err := NormalizeCode(canonical)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, pepper)
	_, _ = mac.Write([]byte(normalized))
	contents := fmt.Sprintf(`{"pepperVersion":"test-v1","codes":{"code.test":"%s"}}`, hex.EncodeToString(mac.Sum(nil)))
	path := filepath.Join(t.TempDir(), "codes.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	book, err := LoadCodeBook(path, pepper, []string{"code.test"})
	if err != nil {
		t.Fatal(err)
	}
	key, ok := book.Match("  中文ABC  ")
	if !ok || key != "code.test" {
		t.Fatalf("Match = %q, %v", key, ok)
	}
}
