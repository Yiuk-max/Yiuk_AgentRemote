// Package question keeps bounded, ephemeral user questions. No UI or transport dependencies.
package question

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	Pending   = "pending"
	Answered  = "answered"
	Cancelled = "cancelled"
	TimedOut  = "timed_out"
	Unknown   = "result_unknown"
)

var (
	ErrInvalid  = errors.New("invalid question or reply")
	ErrNotFound = errors.New("question not found")
	ErrBusy     = errors.New("device already has a pending question")
	ErrConflict = errors.New("request ID already used with different content")
	ErrLimit    = errors.New("session question limit reached")
)

type Record struct {
	SessionID, RequestID, DeviceID, Question string
	Options                                  []string
	TimeoutSeconds                           int
	ExpiresAt, FinishedAt                    time.Time
	Status, AnswerSource, Answer             string
	SelectedIndex                            *int
}

func clone(r *Record) Record {
	c := *r
	c.Options = append([]string(nil), r.Options...)
	if r.SelectedIndex != nil {
		i := *r.SelectedIndex
		c.SelectedIndex = &i
	}
	return c
}
func Normalize(text string, options []string) (string, []string, error) {
	text = strings.TrimSpace(text)
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 1000 || len(options) > 6 {
		return "", nil, ErrInvalid
	}
	normalized := make([]string, len(options))
	seen := map[string]bool{}
	for i, option := range options {
		option = strings.TrimSpace(option)
		if !utf8.ValidString(option) || option == "" || utf8.RuneCountInString(option) > 100 || seen[option] {
			return "", nil, ErrInvalid
		}
		normalized[i] = option
		seen[option] = true
	}
	return text, normalized, nil
}

type Manager struct {
	mu      sync.Mutex
	records map[string]*Record
	limit   int
}

func New(limit int) *Manager {
	if limit < 1 {
		limit = 32
	}
	return &Manager{records: map[string]*Record{}, limit: limit}
}
func (m *Manager) Start(sessionID, requestID, deviceID, text string, options []string, timeout int, deadline time.Time) (Record, bool, error) {
	text, options, err := Normalize(text, options)
	if err != nil || timeout < 15 || timeout > 300 || !deadline.After(time.Now()) {
		return Record{}, false, ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.records[requestID]; ok {
		if r.SessionID != sessionID || r.Question != text || !slices.Equal(r.Options, options) || r.TimeoutSeconds != timeout {
			return Record{}, false, ErrConflict
		}
		return clone(r), false, nil
	}
	count := 0
	for _, r := range m.records {
		if r.SessionID == sessionID {
			count++
		}
		if r.DeviceID == deviceID && r.Status == Pending {
			return Record{}, false, ErrBusy
		}
	}
	if count >= m.limit {
		return Record{}, false, ErrLimit
	}
	r := &Record{SessionID: sessionID, RequestID: requestID, DeviceID: deviceID, Question: text, Options: options, TimeoutSeconds: timeout, ExpiresAt: deadline, Status: Pending}
	m.records[requestID] = r
	return clone(r), true, nil
}
func (m *Manager) Get(sessionID, requestID string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[requestID]
	if r == nil || r.SessionID != sessionID {
		return Record{}, ErrNotFound
	}
	return clone(r), nil
}
func finish(r *Record, status string) { r.Status = status; r.FinishedAt = time.Now() }
func (m *Manager) Reply(sessionID, requestID, status, source, answer string, index *int) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.records[requestID]
	if r == nil || r.SessionID != sessionID {
		return Record{}, ErrNotFound
	}
	if r.Status != Pending {
		return clone(r), nil
	}
	if !time.Now().Before(r.ExpiresAt) {
		finish(r, TimedOut)
		return clone(r), nil
	}
	if status == Cancelled {
		if source != "" || answer != "" || index != nil {
			return Record{}, ErrInvalid
		}
		finish(r, Cancelled)
		return clone(r), nil
	}
	if status != Answered {
		return Record{}, ErrInvalid
	}
	switch source {
	case "option":
		if index == nil || *index < 0 || *index >= len(r.Options) {
			return Record{}, ErrInvalid
		}
		i := *index
		r.SelectedIndex = &i
		r.Answer = r.Options[i]
	case "text":
		answer = strings.TrimSpace(answer)
		if index != nil || answer == "" || !utf8.ValidString(answer) || utf8.RuneCountInString(answer) > 1000 {
			return Record{}, ErrInvalid
		}
		r.Answer = answer
	default:
		return Record{}, ErrInvalid
	}
	r.AnswerSource = source
	finish(r, Answered)
	return clone(r), nil
}
func (m *Manager) FinishSession(sessionID, status string) []Record {
	if status != Cancelled && status != Unknown {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var changed []Record
	for _, r := range m.records {
		if r.SessionID == sessionID && r.Status == Pending {
			finish(r, status)
			changed = append(changed, clone(r))
		}
	}
	return changed
}
func (m *Manager) Expire() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var changed []Record
	for _, r := range m.records {
		if r.Status == Pending && !time.Now().Before(r.ExpiresAt) {
			finish(r, TimedOut)
			changed = append(changed, clone(r))
		}
	}
	return changed
}
func (m *Manager) PendingRecords() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	var records []Record
	for _, r := range m.records {
		if r.Status == Pending {
			records = append(records, clone(r))
		}
	}
	return records
}

func (m *Manager) Reap(retain time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.records {
		if r.Status != Pending && time.Since(r.FinishedAt) > retain {
			delete(m.records, id)
		}
	}
}
