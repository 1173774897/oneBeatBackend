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
