package graph

import (
	"testing"
	"time"
)

func TestAfterDurationScheduleFormatsUTCStartAndExpiration(t *testing.T) {
	now := time.Date(2026, 7, 16, 14, 30, 0, 0, time.FixedZone("CEST", 2*3600))

	got := AfterDurationSchedule(now, "PT2H")

	if got.StartDateTime != "2026-07-16T12:30:00Z" {
		t.Fatalf("expected UTC RFC3339 startDateTime, got %q", got.StartDateTime)
	}
	if got.Expiration.Type != "AfterDuration" {
		t.Fatalf("expected AfterDuration expiration type, got %q", got.Expiration.Type)
	}
	if got.Expiration.Duration != "PT2H" {
		t.Fatalf("expected PT2H duration, got %q", got.Expiration.Duration)
	}
}
