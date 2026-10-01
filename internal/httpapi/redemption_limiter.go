package httpapi

import (
	"net"
	"strings"
	"sync"
	"time"
)

const (
	redemptionFailureWindow = 10 * time.Minute
	redemptionAccountLimit  = 8
	redemptionIPLimit       = 30
)

type redemptionLimiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
}

// newRedemptionLimiter creates a process-local abuse guard. It is deliberately
// lightweight; deployments with multiple API replicas need shared storage.
func newRedemptionLimiter() *redemptionLimiter {
	return &redemptionLimiter{failures: make(map[string][]time.Time)}
}

func (limiter *redemptionLimiter) Allow(userID string, clientIP string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.activeCount("account:"+userID, now) < redemptionAccountLimit &&
		limiter.activeCount("ip:"+clientIP, now) < redemptionIPLimit
}

func (limiter *redemptionLimiter) RecordFailure(userID string, clientIP string, now time.Time) {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.failures["account:"+userID] = append(limiter.prune("account:"+userID, now), now)
	limiter.failures["ip:"+clientIP] = append(limiter.prune("ip:"+clientIP, now), now)
}

func (limiter *redemptionLimiter) activeCount(key string, now time.Time) int {
	active := limiter.prune(key, now)
	if len(active) == 0 {
		delete(limiter.failures, key)
	} else {
		limiter.failures[key] = active
	}
	return len(active)
}

func (limiter *redemptionLimiter) prune(key string, now time.Time) []time.Time {
	entries := limiter.failures[key]
	cutoff := now.Add(-redemptionFailureWindow)
	firstActive := 0
	for firstActive < len(entries) && entries[firstActive].Before(cutoff) {
		firstActive++
	}
	return entries[firstActive:]
}

// remoteIP uses the transport peer address. The API must not trust forwarded
// headers until a known reverse-proxy allowlist is configured.
func remoteIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(remoteAddress)
}
