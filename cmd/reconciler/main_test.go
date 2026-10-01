package main

import (
	"testing"
	"time"

	"onebeat/store-api/internal/store/repository"
)

func TestDailyResumeStartUsesYesterdayForFreshCheckpoint(t *testing.T) {
	today := time.Date(2026, 10, 2, 0, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	got := dailyResumeStart(repository.ReconciliationCheckpoint{}, today)
	want := today.AddDate(0, 0, -1)
	if !got.Equal(want) {
		t.Fatalf("start = %v, want %v", got, want)
	}
}

func TestDailyResumeStartCatchesUpFromLastCompletedWindow(t *testing.T) {
	today := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -3)
	end := start.AddDate(0, 0, 1)
	got := dailyResumeStart(repository.ReconciliationCheckpoint{
		WindowStart: &start,
		WindowEnd:   &end,
		Status:      "COMPLETED",
	}, today)
	if !got.Equal(end) {
		t.Fatalf("start = %v, want %v", got, end)
	}
}

func TestDailyResumeStartRetriesFailedWindow(t *testing.T) {
	today := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	start := today.AddDate(0, 0, -2)
	end := start.AddDate(0, 0, 1)
	got := dailyResumeStart(repository.ReconciliationCheckpoint{
		WindowStart: &start,
		WindowEnd:   &end,
		Status:      "FAILED",
	}, today)
	if !got.Equal(start) {
		t.Fatalf("start = %v, want %v", got, start)
	}
}
