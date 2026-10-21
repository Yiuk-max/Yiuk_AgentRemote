package task

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestStartIsIdempotentAndBoundsConcurrency(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(8, 1, func() time.Time { return now })
	first, created, err := m.Start("s1", "r1", time.Minute)
	if err != nil || !created {
		t.Fatalf("start: %v created=%v", err, created)
	}
	if first.Status != StatusRunning {
		t.Fatalf("status = %s", first.Status)
	}
	dup, created, err := m.Start("s1", "r1", time.Minute)
	if err != nil || created {
		t.Fatalf("duplicate start: %v created=%v", err, created)
	}
	if dup.RequestID != "r1" {
		t.Fatalf("duplicate request id = %s", dup.RequestID)
	}
	if _, _, err := m.Start("s2", "r1", time.Minute); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("cross-session request id got %v, want ErrRequestConflict", err)
	}
	if _, _, err := m.Start("s1", "r2", time.Minute); !errors.Is(err, ErrTaskLimit) {
		t.Fatalf("task limit got %v, want ErrTaskLimit", err)
	}
}

func TestOutputBudgetAndCursor(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(10, 2, func() time.Time { return now })
	if _, _, err := m.Start("s1", "r1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendOutput("s1", "r1", StreamStdout, "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AppendOutput("s1", "r1", StreamStderr, strings.Repeat("x", 20)); err != nil {
		t.Fatal(err)
	}
	view, err := m.Result("s1", "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Task.Truncated {
		t.Fatal("task should be marked truncated")
	}
	if view.Task.OutputBytes != 10 {
		t.Fatalf("output bytes = %d, want 10", view.Task.OutputBytes)
	}
	if len(view.Chunks) != 2 || view.NextCursor != 2 {
		t.Fatalf("chunks=%d next=%d", len(view.Chunks), view.NextCursor)
	}
	page, err := m.Result("s1", "r1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Chunks) != 1 || page.Chunks[0].Stream != StreamStderr {
		t.Fatalf("unexpected page: %#v", page.Chunks)
	}
	if len(page.Chunks[0].Data) != 5 {
		t.Fatalf("second chunk length = %d, want 5", len(page.Chunks[0].Data))
	}
}

func TestFinishAndSessionTerminalStates(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(1024, 2, func() time.Time { return now })
	if _, _, err := m.Start("s1", "r1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Start("s1", "r2", time.Minute); err != nil {
		t.Fatal(err)
	}
	exit := 3
	finished, err := m.Finish("s1", "r1", StatusFailed, &exit, true)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != StatusFailed || !finished.ResultKnown || finished.ExitCode == nil || *finished.ExitCode != 3 {
		t.Fatalf("unexpected finish: %#v", finished)
	}
	again, err := m.Finish("s1", "r1", StatusSucceeded, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.Status != StatusFailed {
		t.Fatalf("terminal status changed to %s", again.Status)
	}
	unknown := m.FinishSession("s1", StatusResultUnknown)
	if len(unknown) != 1 || unknown[0].RequestID != "r2" || unknown[0].ResultKnown {
		t.Fatalf("unexpected unknown tasks: %#v", unknown)
	}
	if _, err := m.AppendOutput("s1", "r1", StreamStdout, "late"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("append to terminal got %v, want ErrTerminal", err)
	}
}

func TestCancelRequested(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(1024, 2, func() time.Time { return now })
	if _, _, err := m.Start("s1", "r1", time.Minute); err != nil {
		t.Fatal(err)
	}
	task, err := m.CancelRequested("s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !task.CancelRequested || task.Status != StatusRunning {
		t.Fatalf("unexpected cancel state: %#v", task)
	}
	if _, err := m.Finish("s1", "r1", StatusCancelled, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.CancelRequested("s1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestExpireMarksTimedOut(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(1024, 2, func() time.Time { return now })
	if _, _, err := m.Start("s1", "r1", time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(m.Expire()) != 0 {
		t.Fatal("task must not expire before its deadline")
	}
	now = now.Add(time.Minute)
	expired := m.Expire()
	if len(expired) != 1 || expired[0].Status != StatusTimedOut || expired[0].ResultKnown {
		t.Fatalf("unexpected expired task: %#v", expired)
	}
	if m.ActiveCount("s1") != 0 {
		t.Fatalf("timed out task still counts as active")
	}
}
