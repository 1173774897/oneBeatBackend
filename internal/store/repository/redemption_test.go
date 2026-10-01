package repository

import (
	"testing"
	"time"

	"onebeat/store-api/internal/store/redemption"
)

func TestRedemptionChainMonthDurationUsesConfiguredAcceleration(t *testing.T) {
	anchor := time.Date(2027, time.January, 31, 2, 3, 4, 5, time.UTC)
	latestEnd := redemption.GrantBoundary(anchor, 2, redemption.NonProductionMonthDuration)

	duration, ok := redemptionChainMonthDuration(
		anchor,
		2,
		latestEnd,
		redemption.NonProductionMonthDuration,
	)
	if !ok || duration != redemption.NonProductionMonthDuration {
		t.Fatalf("duration=%s ok=%t, want 300s true", duration, ok)
	}
}

func TestRedemptionChainMonthDurationPreservesLegacyCalendarChain(t *testing.T) {
	anchor := time.Date(2027, time.January, 31, 2, 3, 4, 5, time.UTC)
	latestEnd := redemption.CalendarBoundary(anchor, 2)

	duration, ok := redemptionChainMonthDuration(
		anchor,
		2,
		latestEnd,
		redemption.NonProductionMonthDuration,
	)
	if !ok || duration != 0 {
		t.Fatalf("duration=%s ok=%t, want calendar true", duration, ok)
	}
}

func TestRedemptionChainMonthDurationRejectsBrokenChain(t *testing.T) {
	anchor := time.Date(2027, time.January, 31, 2, 3, 4, 5, time.UTC)
	latestEnd := anchor.Add(123 * time.Second)

	if _, ok := redemptionChainMonthDuration(
		anchor,
		2,
		latestEnd,
		redemption.NonProductionMonthDuration,
	); ok {
		t.Fatal("broken chain was accepted")
	}
}
