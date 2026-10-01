package redemption

import "time"

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
