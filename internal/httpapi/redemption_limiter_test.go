package httpapi

import (
	"testing"
	"time"
)

func TestRedemptionLimiterOnlyBlocksRepeatedFailures(t *testing.T) {
	limiter := newRedemptionLimiter()
	now := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < redemptionAccountLimit; index++ {
		if !limiter.Allow("user", "127.0.0.1", now) {
			t.Fatalf("attempt %d blocked too early", index)
		}
		limiter.RecordFailure("user", "127.0.0.1", now)
	}
	if limiter.Allow("user", "127.0.0.2", now) {
		t.Fatal("account was not blocked after failure limit")
	}
	if !limiter.Allow("user", "127.0.0.2", now.Add(redemptionFailureWindow+time.Second)) {
		t.Fatal("account did not recover after failure window")
	}
}
