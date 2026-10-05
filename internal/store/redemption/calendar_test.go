package redemption

import (
	"testing"
	"time"
)

func TestCalendarBoundaryKeepsAnchorDayAcrossShortMonths(t *testing.T) {
	anchor := time.Date(2027, time.January, 31, 2, 3, 4, 5, time.UTC)
	wants := []time.Time{
		anchor,
		time.Date(2027, time.February, 28, 2, 3, 4, 5, time.UTC),
		time.Date(2027, time.March, 31, 2, 3, 4, 5, time.UTC),
		time.Date(2027, time.April, 30, 2, 3, 4, 5, time.UTC),
	}
	for months, want := range wants {
		if got := CalendarBoundary(anchor, months); !got.Equal(want) {
			t.Fatalf("CalendarBoundary(%d) = %s, want %s", months, got, want)
		}
	}
}

func TestMonthDurationForEnvironmentAcceleratesOnlyNonProduction(t *testing.T) {
	for _, environment := range []string{"prod", "production", " PROD "} {
		if got := MonthDurationForEnvironment(environment); got != 0 {
			t.Fatalf("MonthDurationForEnvironment(%q) = %s, want calendar month", environment, got)
		}
	}
	for _, environment := range []string{"test", "development", "staging", ""} {
		if got := MonthDurationForEnvironment(environment); got != NonProductionMonthDuration {
			t.Fatalf("MonthDurationForEnvironment(%q) = %s, want %s", environment, got, NonProductionMonthDuration)
		}
	}
}

func TestGrantBoundaryUsesAcceleratedMonthsWhenConfigured(t *testing.T) {
	anchor := time.Date(2027, time.January, 31, 2, 3, 4, 5, time.UTC)
	for months, want := range []time.Time{
		anchor,
		anchor.Add(NonProductionMonthDuration),
		anchor.Add(2 * NonProductionMonthDuration),
	} {
		if got := GrantBoundary(anchor, months, NonProductionMonthDuration); !got.Equal(want) {
			t.Fatalf("GrantBoundary(%d) = %s, want %s", months, got, want)
		}
	}
	if got, want := GrantBoundary(anchor, 1, 0), CalendarBoundary(anchor, 1); !got.Equal(want) {
		t.Fatalf("production GrantBoundary = %s, want %s", got, want)
	}
}
