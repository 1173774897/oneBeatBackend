package redemption

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const MaxCodeRunes = 64

type codeFile struct {
	PepperVersion string            `json:"pepperVersion"`
	Codes         map[string]string `json:"codes"`
}

type codeEntry struct {
	key    string
	digest [sha256.Size]byte
}

// CodeBook maps secret HMAC digests to public code keys without retaining a plaintext code.
type CodeBook struct {
	pepper  []byte
	entries []codeEntry
}

// LoadCodeBook validates the private digest file against all code keys required
// by the compiled campaign configuration. Unknown keys fail closed so a typo or
// stale secret cannot silently activate an unintended code.
func LoadCodeBook(path string, pepper []byte, requiredKeys []string) (*CodeBook, error) {
	if strings.TrimSpace(path) == "" || len(pepper) < 32 {
		return nil, errors.New("redemption code path and a 32-character pepper are required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read redemption code file: %w", err)
	}
	var file codeFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("decode redemption code file: %w", err)
	}
	if strings.TrimSpace(file.PepperVersion) == "" {
		return nil, errors.New("redemption code file has no pepperVersion")
	}
	required := make(map[string]struct{}, len(requiredKeys))
	for _, key := range requiredKeys {
		required[key] = struct{}{}
	}
	entries := make([]codeEntry, 0, len(file.Codes))
	seenDigests := make(map[string]string, len(file.Codes))
	for key, encoded := range file.Codes {
		if _, ok := required[key]; !ok {
			return nil, fmt.Errorf("redemption code file contains unknown code key %q", key)
		}
		digestBytes, err := hex.DecodeString(encoded)
		if err != nil || len(digestBytes) != sha256.Size {
			return nil, fmt.Errorf("redemption code %q must be a SHA-256 hex digest", key)
		}
		canonical := hex.EncodeToString(digestBytes)
		if previous, exists := seenDigests[canonical]; exists {
			return nil, fmt.Errorf("redemption code keys %q and %q share a digest", previous, key)
		}
		seenDigests[canonical] = key
		var digest [sha256.Size]byte
		copy(digest[:], digestBytes)
		entries = append(entries, codeEntry{key: key, digest: digest})
		delete(required, key)
	}
	if len(required) != 0 {
		missing := make([]string, 0, len(required))
		for key := range required {
			missing = append(missing, key)
		}
		return nil, fmt.Errorf("redemption code file is missing required keys: %s", strings.Join(missing, ", "))
	}
	return &CodeBook{pepper: append([]byte(nil), pepper...), entries: entries}, nil
}

// NormalizeCode applies the same Unicode normalization used when generating
// configured digests. It preserves meaningful whitespace inside the phrase.
func NormalizeCode(input string) (string, error) {
	code := strings.TrimSpace(norm.NFKC.String(input))
	if code == "" || !utf8.ValidString(code) || utf8.RuneCountInString(code) > MaxCodeRunes {
		return "", errors.New("invalid redemption code")
	}
	return code, nil
}

// Match scans the whole code book so timing does not reveal the matching entry position.
func (book *CodeBook) Match(input string) (string, bool) {
	if book == nil {
		return "", false
	}
	normalized, err := NormalizeCode(input)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, book.pepper)
	_, _ = mac.Write([]byte(normalized))
	wanted := mac.Sum(nil)
	matchedKey := ""
	matched := 0
	for _, entry := range book.entries {
		equal := hmac.Equal(wanted, entry.digest[:])
		if equal {
			matchedKey = entry.key
			matched++
		}
	}
	return matchedKey, matched == 1
}
