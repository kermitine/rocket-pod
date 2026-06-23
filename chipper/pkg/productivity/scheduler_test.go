package productivity

import (
	"testing"
	"time"
)

func TestTimeInProductivityTimezone(t *testing.T) {
	now := time.Date(2026, time.June, 21, 15, 30, 0, 0, time.UTC)
	got := timeInProductivityTimezone(now, "America/Los_Angeles")

	if got.Format("2006-01-02 15:04 MST") != "2026-06-21 08:30 PDT" {
		t.Fatalf("converted time = %s, want 2026-06-21 08:30 PDT", got.Format("2006-01-02 15:04 MST"))
	}
}

func TestTimeInProductivityTimezoneFallsBackForInvalidZone(t *testing.T) {
	now := time.Date(2026, time.June, 21, 15, 30, 0, 0, time.UTC)
	if got := timeInProductivityTimezone(now, "not/a-timezone"); !got.Equal(now) || got.Location() != now.Location() {
		t.Fatalf("invalid timezone returned %v, want unchanged %v", got, now)
	}
}

func TestEndOfProductivityDayUsesConfiguredTimezone(t *testing.T) {
	location, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.June, 22, 23, 59, 0, 0, location)
	want := time.Date(2026, time.June, 23, 0, 0, 0, 0, location)

	if got := endOfProductivityDay(now); !got.Equal(want) || got.Location() != location {
		t.Fatalf("endOfProductivityDay(%v) = %v, want %v", now, got, want)
	}
}
