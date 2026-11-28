package jobs

import (
	"testing"
	"time"
)

func TestTerminalStates(t *testing.T) {
	terminal := []Status{StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled, StatusResultUnknown}
	for _, status := range terminal {
		if !status.Terminal() {
			t.Fatalf("%q should be terminal", status)
		}
	}
	running := []Status{StatusQueued, StatusRunning, ""}
	for _, status := range running {
		if status.Terminal() {
			t.Fatalf("%q should not be terminal", status)
		}
	}
}

func TestExpired(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	retain := 15 * time.Minute

	if Expired(StatusRunning, now.Add(-time.Hour), now, retain) {
		t.Fatal("a running job never expires, however old")
	}
	if Expired(StatusSucceeded, time.Time{}, now, retain) {
		t.Fatal("a record without a finish time never expires")
	}
	if Expired(StatusSucceeded, now.Add(-5*time.Minute), now, retain) {
		t.Fatal("fresh terminal record must be kept")
	}
	if !Expired(StatusSucceeded, now.Add(-16*time.Minute), now, retain) {
		t.Fatal("old terminal record must be reaped")
	}
	// result_unknown 也要过期：它是终态，但“可能已生效”，保留期过后同样回收。
	if !Expired(StatusResultUnknown, now.Add(-16*time.Minute), now, retain) {
		t.Fatal("result_unknown must be reaped like other terminal states")
	}
}
