package redemption

import (
	"strings"
	"time"
)

const (
	// NonProductionDayDuration is one accelerated "product day" in non-production
	// redemption grants (30 such days form one test month).
	NonProductionDayDuration   = time.Hour
	NonProductionMonthDuration = 30 * NonProductionDayDuration
)

// MonthDurationForEnvironment returns zero when redemption grants must use real
// calendar boundaries. Every non-production environment uses a fixed accelerated
// month (30 × NonProductionDayDuration) so expiry and repeated redemption can
// be exercised without waiting for real calendar months.
func MonthDurationForEnvironment(environment string) time.Duration {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "prod", "production":
		return 0
	default:
		return NonProductionMonthDuration
	}
}

// GrantBoundary returns the boundary for a redemption chain. A zero duration
// preserves production's calendar-month semantics; a positive duration enables
// the non-production accelerated clock.
func GrantBoundary(anchor time.Time, months int, acceleratedMonthDuration time.Duration) time.Time {
	if acceleratedMonthDuration > 0 {
		return anchor.UTC().Add(time.Duration(months) * acceleratedMonthDuration)
	}
	return CalendarBoundary(anchor, months)
}

// CalendarBoundary keeps the anchor's UTC wall-clock time and clamps its day in shorter months.
func CalendarBoundary(anchor time.Time, months int) time.Time {
	anchor = anchor.UTC()
	year, month, day := anchor.Date()
	targetMonthIndex := int(month) - 1 + months
	targetYear := year + targetMonthIndex/12
	targetMonthIndex %= 12
	if targetMonthIndex < 0 {
		targetMonthIndex += 12
		targetYear--
	}
	targetMonth := time.Month(targetMonthIndex + 1)
	lastDay := time.Date(targetYear, targetMonth+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}
	hour, minute, second := anchor.Clock()
	return time.Date(targetYear, targetMonth, day, hour, minute, second, anchor.Nanosecond(), time.UTC)
}
