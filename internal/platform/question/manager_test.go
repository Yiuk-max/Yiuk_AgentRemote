package question

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func start(t *testing.T, m *Manager, id string, options []string) Record {
	t.Helper()
	r, _, err := m.Start("session", id, "device", "选择什么？", options, 120, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestRepliesAndDeduplication(t *testing.T) {
	m := New(32)
	r := start(t, m, "one", []string{" 默认 ", "其他"})
	if _, created, err := m.Start("session", "one", "device", "选择什么？", []string{"默认", "其他"}, 120, r.ExpiresAt); err != nil || created {
		t.Fatalf("dedup: %v %v", created, err)
	}
	if _, _, err := m.Start("session", "one", "device", "different", nil, 120, r.ExpiresAt); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, _, err := m.Start("session", "two", "device", "question", nil, 120, r.ExpiresAt); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	invalid := 5
	if _, err := m.Reply("session", "one", Answered, "option", "spoof", &invalid); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	index := 0
	answer, err := m.Reply("session", "one", Answered, "option", "spoof", &index)
	if err != nil || answer.Answer != "默认" {
		t.Fatalf("%+v %v", answer, err)
	}
	duplicate, _ := m.Reply("session", "one", Cancelled, "", "", nil)
	if duplicate.Status != Answered {
		t.Fatal(duplicate)
	}
	if _, err := m.Get("wrong", "one"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	start(t, m, "text", nil)
	text, err := m.Reply("session", "text", Answered, "text", " 自定义😀 ", nil)
	if err != nil || text.Answer != "自定义😀" || text.SelectedIndex != nil {
		t.Fatalf("%+v %v", text, err)
	}
	start(t, m, "cancel", nil)
	cancelled, err := m.Reply("session", "cancel", Cancelled, "", "", nil)
	if err != nil || cancelled.Status != Cancelled {
		t.Fatal(cancelled, err)
	}
}
func TestValidation(t *testing.T) {
	for _, opts := range [][]string{{" "}, {"one", " one "}, {strings.Repeat("😀", 101)}, {"1", "2", "3", "4", "5", "6", "7"}} {
		if _, _, err := Normalize("question", opts); err == nil {
			t.Fatal(opts)
		}
	}
	if _, _, err := Normalize(strings.Repeat("😀", 1000), nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Normalize(strings.Repeat("😀", 1001), nil); err == nil {
		t.Fatal("accepted oversized question")
	}
	m := New(1)
	start(t, m, "one", nil)
	m.FinishSession("session", Cancelled)
	if _, _, err := m.Start("session", "two", "device", "question", nil, 120, time.Now().Add(time.Minute)); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	m.Reap(-time.Second)
	if _, err := m.Get("session", "one"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestTimeoutAndConcurrentTerminal(t *testing.T) {
	m := New(32)
	start(t, m, "expired", nil)
	m.records["expired"].ExpiresAt = time.Now().Add(-time.Second)
	r, err := m.Reply("session", "expired", Answered, "text", "late", nil)
	if err != nil || r.Status != TimedOut {
		t.Fatal(r, err)
	}
	start(t, m, "race", nil)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				m.Reply("session", "race", Answered, "text", "yes", nil)
			} else {
				m.FinishSession("session", Unknown)
			}
		}(i)
	}
	wg.Wait()
	r, _ = m.Get("session", "race")
	if r.Status != Answered && r.Status != Unknown {
		t.Fatal(r)
	}
	before := r
	m.Reply("session", "race", Cancelled, "", "", nil)
	r, _ = m.Get("session", "race")
	if before.Status != r.Status {
		t.Fatal(r)
	}
	start(t, m, "timer", nil)
	m.records["timer"].ExpiresAt = time.Now().Add(-time.Second)
	if len(m.Expire()) != 1 {
		t.Fatal("expiry")
	}
}
