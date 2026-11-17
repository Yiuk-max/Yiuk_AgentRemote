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
	view, err := m.ResultFromCursor("s1", "r1", 0)
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
	page, err := m.ResultFromCursor("s1", "r1", 1)
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
	task, err := m.RequestCancel("s1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if !task.CancelRequested || task.Status != StatusRunning {
		t.Fatalf("unexpected cancel state: %#v", task)
	}
	if _, err := m.Finish("s1", "r1", StatusCancelled, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RequestCancel("s1", "missing"); !errors.Is(err, ErrNotFound) {
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
	if m.CountUnfinishedBySession("s1") != 0 {
		t.Fatalf("timed out task still counts as active")
	}
}

// 终态任务必须能被回收：否则每条大输出命令最多 2 MiB 永久常驻，长跑会 OOM。
// 回收有两个维度：保留时长，以及保留输出总字节上限（从最老的终态任务开始淘汰）。
func TestReapTerminalTasks(t *testing.T) {
	now := time.Unix(1000, 0)
	m := newManager(1<<20, 8, func() time.Time { return now })
	output := strings.Repeat("x", 64)

	finish := func(sessionID, requestID string) {
		t.Helper()
		if _, _, err := m.Start(sessionID, requestID, time.Minute); err != nil {
			t.Fatalf("start %s: %v", requestID, err)
		}
		if _, err := m.AppendOutput(sessionID, requestID, StreamStdout, output); err != nil {
			t.Fatalf("append %s: %v", requestID, err)
		}
		if _, err := m.Finish(sessionID, requestID, StatusSucceeded, nil, true); err != nil {
			t.Fatalf("finish %s: %v", requestID, err)
		}
	}

	// r1 结束在 t0，r2 结束在 t0+2min，r3 还在跑。
	finish("s1", "r1")
	now = now.Add(2 * time.Minute)
	finish("s1", "r2")
	if _, _, err := m.Start("s1", "r3", time.Minute); err != nil {
		t.Fatalf("start r3: %v", err)
	}

	// 按保留时长回收：只有 r1 超过 1 分钟。
	byAge, bySize := m.Reap(time.Minute, 0)
	if byAge != 1 || bySize != 0 {
		t.Fatalf("reap = (%d,%d), want (1,0)", byAge, bySize)
	}
	if _, ok := m.Get("s1", "r1"); ok {
		t.Fatal("r1 should be reaped")
	}
	if _, ok := m.Get("s1", "r2"); !ok {
		t.Fatal("r2 should still be retained")
	}
	if _, ok := m.Get("s1", "r3"); !ok {
		t.Fatal("running task must never be reaped")
	}

	// 回收后 request_id 可以重新使用（幂等只保证在保留期内）。
	if _, created, err := m.Start("s9", "r1", time.Minute); err != nil || !created {
		t.Fatalf("reuse reaped request id: created=%v err=%v", created, err)
	}

	// 按输出上限淘汰：两条终态任务各留 64 字节，上限压到 64，最老的先走。
	now = now.Add(2 * time.Minute)
	finish("s1", "r4") // 64 字节，结束时间最新
	byAge, bySize = m.Reap(time.Hour, 64)
	if byAge != 0 || bySize == 0 {
		t.Fatalf("byte-cap reap = (%d,%d), want bySize>0", byAge, bySize)
	}
	// 最老的终态任务（r2）应被淘汰，最新完成的 r4 必须还在。
	if _, ok := m.Get("s1", "r2"); ok {
		t.Fatal("oldest terminal task should be evicted by byte cap")
	}
	if _, ok := m.Get("s1", "r4"); !ok {
		t.Fatal("newest terminal task should survive byte-cap eviction")
	}

	// 输出上限兜不住时也不能动运行中的任务。
	if _, ok := m.Get("s1", "r3"); !ok {
		t.Fatal("running task must never be evicted by byte cap")
	}
}
