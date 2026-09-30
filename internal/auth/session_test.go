package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSessionRoundTripAndTamperDetection(t *testing.T) {
	manager := NewSessionManager([]byte(strings.Repeat("k", 32)), time.Hour)
	fixedNow := time.Date(2026, time.September, 30, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return fixedNow }

	token, expiresAt, err := manager.Issue("user-123")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if !expiresAt.Equal(fixedNow.Add(time.Hour)) {
		t.Fatalf("expiresAt = %v", expiresAt)
	}
	claims, err := manager.Verify(token)
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if claims.Subject != "user-123" {
		t.Fatalf("subject = %q", claims.Subject)
	}

	tampered := token[:len(token)-1] + "A"
	if _, err := manager.Verify(tampered); err == nil {
		t.Fatal("tampered token unexpectedly verified")
	}
}

func TestSessionExpiry(t *testing.T) {
	manager := NewSessionManager([]byte(strings.Repeat("k", 32)), time.Minute)
	now := time.Date(2026, time.September, 30, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	token, _, err := manager.Issue("user-123")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	manager.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := manager.Verify(token); err == nil {
		t.Fatal("expired token unexpectedly verified")
	}
}
