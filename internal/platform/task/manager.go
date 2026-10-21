// Package task owns in-memory command task state, bounded output buffering and
// request idempotency. Tasks never survive a platform restart and are never
// automatically replayed.
package task

import (
	"errors"
	"sync"
	"time"
)

// Status is the task lifecycle state.
type Status string

const (
	StatusQueued        Status = "queued"
	StatusRunning       Status = "running"
	StatusSucceeded     Status = "succeeded"
	StatusFailed        Status = "failed"
	StatusTimedOut      Status = "timed_out"
	StatusCancelled     Status = "cancelled"
	StatusResultUnknown Status = "result_unknown"
)

// Stream names.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

var (
	ErrNotFound        = errors.New("task not found")
	ErrTaskLimit       = errors.New("task limit reached for session")
	ErrRequestConflict = errors.New("request id is already used by another session")
	ErrTerminal        = errors.New("task is already in a terminal state")
	ErrInvalidStatus   = errors.New("invalid task terminal status")
)

// Chunk is one bounded output chunk.
type Chunk struct {
	Stream   string
	Sequence uint64
	Data     string
}

// Task is the in-memory task record.
type Task struct {
	RequestID       string
	SessionID       string
	Status          Status
	StartedAt       time.Time
	Deadline        time.Time
	FinishedAt      time.Time
	ExitCode        *int
	Chunks          []Chunk
	OutputBytes     int
	Truncated       bool
	ResultKnown     bool
	CancelRequested bool
}

func (t *Task) clone() *Task {
	if t == nil {
		return nil
	}
	out := *t
	out.Chunks = append([]Chunk(nil), t.Chunks...)
	if t.ExitCode != nil {
		v := *t.ExitCode
		out.ExitCode = &v
	}
	return &out
}

// Manager owns all task state.
type Manager struct {
	mu            sync.Mutex
	tasks         map[string]*Task
	maxOutput     int
	maxPerSession int
	now           func() time.Time
}

// NewManager creates a task manager. maxOutput is the total stdout+stderr
// byte budget per task; maxPerSession bounds concurrently queued/running
// tasks.
func NewManager(maxOutput, maxPerSession int) *Manager {
	return newManager(maxOutput, maxPerSession, time.Now)
}

func newManager(maxOutput, maxPerSession int, now func() time.Time) *Manager {
	if maxOutput < 1 {
		maxOutput = 1 << 20
	}
	if maxPerSession < 1 {
		maxPerSession = 1
	}
	return &Manager{tasks: make(map[string]*Task), maxOutput: maxOutput, maxPerSession: maxPerSession, now: now}
}

// Start creates a running task or returns the existing task for a duplicate
// request id. The bool result is true when a new task was created. A positive
// timeout installs a server-side deadline so an unresponsive device cannot
// leak running task slots forever.
func (m *Manager) Start(sessionID, requestID string, timeout time.Duration) (*Task, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if requestID == "" {
		return nil, false, errors.New("request id is required")
	}
	if existing, ok := m.tasks[requestID]; ok {
		if existing.SessionID != sessionID {
			return nil, false, ErrRequestConflict
		}
		return existing.clone(), false, nil
	}
	active := 0
	for _, t := range m.tasks {
		if t.SessionID == sessionID && !isTerminal(t.Status) {
			active++
		}
	}
	if active >= m.maxPerSession {
		return nil, false, ErrTaskLimit
	}
	now := m.now()
	t := &Task{
		RequestID:   requestID,
		SessionID:   sessionID,
		Status:      StatusRunning,
		StartedAt:   now,
		ResultKnown: false,
	}
	if timeout > 0 {
		t.Deadline = now.Add(timeout)
	}
	m.tasks[requestID] = t
	return t.clone(), true, nil
}

// AppendOutput records one output chunk while enforcing the per-task byte
// budget. Excess data is dropped and the task is marked truncated.
func (m *Manager) AppendOutput(sessionID, requestID, stream, data string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(t.Status) {
		return t.clone(), ErrTerminal
	}
	if stream != StreamStdout && stream != StreamStderr {
		return nil, errors.New("invalid output stream")
	}
	if data == "" {
		return t.clone(), nil
	}
	available := m.maxOutput - t.OutputBytes
	if available <= 0 {
		t.Truncated = true
		return t.clone(), nil
	}
	if len(data) > available {
		data = data[:available]
		t.Truncated = true
	}
	t.OutputBytes += len(data)
	t.Chunks = append(t.Chunks, Chunk{Stream: stream, Sequence: uint64(len(t.Chunks) + 1), Data: data})
	return t.clone(), nil
}

// Finish records the terminal status of a task. Terminal tasks are returned
// unchanged, which makes device retries idempotent.
func (m *Manager) Finish(sessionID, requestID string, status Status, exitCode *int, resultKnown bool) (*Task, error) {
	if !isTerminal(status) {
		return nil, ErrInvalidStatus
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(t.Status) {
		return t.clone(), nil
	}
	t.Status = status
	t.FinishedAt = m.now()
	t.ResultKnown = resultKnown && status != StatusResultUnknown
	if exitCode != nil {
		v := *exitCode
		t.ExitCode = &v
	}
	return t.clone(), nil
}

// CancelRequested marks that cancellation was requested. If the task has not
// started running yet it is immediately cancelled.
func (m *Manager) CancelRequested(sessionID, requestID string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(t.Status) {
		return t.clone(), nil
	}
	t.CancelRequested = true
	if t.Status == StatusQueued {
		t.Status = StatusCancelled
		t.FinishedAt = m.now()
	}
	return t.clone(), nil
}

// Get returns a task clone.
func (m *Manager) Get(sessionID, requestID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, false
	}
	return t.clone(), true
}

// ListBySession returns task clones for a session.
func (m *Manager) ListBySession(sessionID string) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Task
	for _, t := range m.tasks {
		if t.SessionID == sessionID {
			out = append(out, t.clone())
		}
	}
	return out
}

// ActiveCount returns the number of queued/running tasks for a session.
func (m *Manager) ActiveCount(sessionID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, t := range m.tasks {
		if t.SessionID == sessionID && !isTerminal(t.Status) {
			count++
		}
	}
	return count
}

// FinishSession transitions every live task for a session. finishStatus is
// used for tasks whose local outcome is known (for example cancelled after an
// owner stop); otherwise result_unknown is used for ambiguous disconnects.
func (m *Manager) FinishSession(sessionID string, finishStatus Status) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !isTerminal(finishStatus) {
		finishStatus = StatusResultUnknown
	}
	now := m.now()
	var out []*Task
	for _, t := range m.tasks {
		if t.SessionID != sessionID || isTerminal(t.Status) {
			continue
		}
		t.Status = finishStatus
		t.FinishedAt = now
		t.ResultKnown = finishStatus != StatusResultUnknown
		out = append(out, t.clone())
	}
	return out
}

// Expire marks running tasks whose server-side deadline has passed as
// timed_out. The local device may still be finishing the process; the result
// is therefore reported as not fully known.
func (m *Manager) Expire() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []*Task
	for _, t := range m.tasks {
		if isTerminal(t.Status) || t.Deadline.IsZero() || now.Before(t.Deadline) {
			continue
		}
		t.Status = StatusTimedOut
		t.FinishedAt = now
		t.ResultKnown = false
		out = append(out, t.clone())
	}
	return out
}

// ResultView is a bounded slice of output starting at a cursor.
type ResultView struct {
	Task       *Task
	Chunks     []Chunk
	NextCursor int
}

// Result returns output chunks after cursor. Cursor is a chunk index, not a
// byte offset, so clients can page deterministically.
func (m *Manager) Result(sessionID, requestID string, cursor int) (*ResultView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(t.Chunks) {
		cursor = len(t.Chunks)
	}
	chunks := append([]Chunk(nil), t.Chunks[cursor:]...)
	return &ResultView{Task: t.clone(), Chunks: chunks, NextCursor: len(t.Chunks)}, nil
}

func isTerminal(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled, StatusResultUnknown:
		return true
	default:
		return false
	}
}
