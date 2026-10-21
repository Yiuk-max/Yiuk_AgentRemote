// Package transfer owns in-memory file-download transfer state. It bounds the
// number of bytes a device may stream for one request and never stores file
// contents: the platform only validates chunk sequence and total size before
// forwarding each chunk to the operator.
package transfer

import (
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// Status is the transfer lifecycle state.
type Status string

const (
	StatusRunning       Status = "running"
	StatusSucceeded     Status = "succeeded"
	StatusFailed        Status = "failed"
	StatusCancelled     Status = "cancelled"
	StatusResultUnknown Status = "result_unknown"
)

// MaxChunkBytes bounds one decoded chunk. The protocol frame limit already
// bounds the encoded message; this keeps a single chunk small enough to stream
// through both peers without large allocations.
const (
	MaxChunkBytes = 96 << 10
	// MaxChunks bounds per-chunk bookkeeping on both peers even when a device
	// sends very small chunks. 4096 chunks still allow 32 MiB at 8 KiB average.
	MaxChunks = 4096
)

var (
	ErrNotFound        = errors.New("transfer not found")
	ErrTransferLimit   = errors.New("transfer limit reached for session")
	ErrRequestConflict = errors.New("request id is already used by another session")
	ErrSequence        = errors.New("file chunk sequence is not contiguous")
	ErrChunkTooLarge   = errors.New("file chunk exceeds the maximum chunk size")
	ErrEmptyChunk      = errors.New("file chunk is empty")
	ErrChunkLimit      = errors.New("file transfer exceeds the maximum chunk count")
	ErrInvalidBase64   = errors.New("file chunk is not valid base64")
	ErrSizeLimit       = errors.New("file download exceeds the configured size limit")
	ErrTerminal        = errors.New("transfer is already in a terminal state")
	ErrInvalidStatus   = errors.New("invalid transfer terminal status")
)

// Transfer is one bounded file download.
type Transfer struct {
	RequestID   string
	SessionID   string
	Correlation string
	Status      Status
	NextSeq     uint64
	Chunks      int
	Bytes       int64
	MaxBytes    int64
	FileName    string
	ResultKnown bool
	StartedAt   time.Time
	Deadline    time.Time
	FinishedAt  time.Time
}

func (t *Transfer) clone() *Transfer {
	if t == nil {
		return nil
	}
	out := *t
	return &out
}

// Manager owns all transfer state.
type Manager struct {
	mu            sync.Mutex
	transfers     map[string]*Transfer
	maxPerSession int
	now           func() time.Time
}

func NewManager(maxPerSession int) *Manager {
	return newManager(maxPerSession, time.Now)
}

func newManager(maxPerSession int, now func() time.Time) *Manager {
	if maxPerSession < 1 {
		maxPerSession = 1
	}
	return &Manager{transfers: make(map[string]*Transfer), maxPerSession: maxPerSession, now: now}
}

// Start creates a running transfer or returns the existing transfer for the
// same session and request id. maxBytes comes from server configuration.
func (m *Manager) Start(sessionID, requestID, correlation string, maxBytes int64, timeout time.Duration) (*Transfer, bool, error) {
	if requestID == "" || sessionID == "" {
		return nil, false, errors.New("session and request id are required")
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.transfers[requestID]; ok {
		if existing.SessionID != sessionID {
			return nil, false, ErrRequestConflict
		}
		return existing.clone(), false, nil
	}
	active := 0
	for _, t := range m.transfers {
		if t.SessionID == sessionID && !isTerminal(t.Status) {
			active++
		}
	}
	if active >= m.maxPerSession {
		return nil, false, ErrTransferLimit
	}
	now := m.now()
	t := &Transfer{
		RequestID:   requestID,
		SessionID:   sessionID,
		Correlation: correlation,
		Status:      StatusRunning,
		NextSeq:     1,
		MaxBytes:    maxBytes,
		StartedAt:   now,
	}
	if timeout > 0 {
		t.Deadline = now.Add(timeout)
	}
	m.transfers[requestID] = t
	return t.clone(), true, nil
}

// Append validates one encoded chunk and records its decoded size. File
// contents are intentionally discarded; the caller forwards the original
// message.
func (m *Manager) Append(sessionID, requestID string, sequence uint64, data string) (*Transfer, error) {
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, ErrInvalidBase64
	}
	if len(decoded) == 0 {
		return nil, ErrEmptyChunk
	}
	if len(decoded) > MaxChunkBytes {
		return nil, ErrChunkTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.transfers[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(t.Status) {
		return t.clone(), ErrTerminal
	}
	if sequence != t.NextSeq {
		return nil, ErrSequence
	}
	if t.Bytes+int64(len(decoded)) > t.MaxBytes {
		t.Status = StatusFailed
		t.FinishedAt = m.now()
		return t.clone(), ErrSizeLimit
	}
	if t.Chunks >= MaxChunks {
		t.Status = StatusFailed
		t.FinishedAt = m.now()
		return t.clone(), ErrChunkLimit
	}
	t.Bytes += int64(len(decoded))
	t.Chunks++
	t.NextSeq++
	return t.clone(), nil
}

// Finish records the terminal transfer status. Terminal transfers are returned
// unchanged so a device retry is idempotent.
func (m *Manager) Finish(sessionID, requestID string, status Status, fileName string, fileSize int64, resultKnown bool) (*Transfer, error) {
	if !isTerminal(status) {
		return nil, ErrInvalidStatus
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.transfers[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(t.Status) {
		return t.clone(), nil
	}
	if status == StatusSucceeded && fileSize != t.Bytes {
		status = StatusResultUnknown
		resultKnown = false
	}
	t.Status = status
	t.FileName = fileName
	t.FinishedAt = m.now()
	t.ResultKnown = resultKnown && status != StatusResultUnknown
	return t.clone(), nil
}

// FinishSession terminates every live transfer for a session and returns the
// terminal records so callers can notify the operator.
func (m *Manager) FinishSession(sessionID string, status Status) []*Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !isTerminal(status) {
		status = StatusResultUnknown
	}
	now := m.now()
	var out []*Transfer
	for _, t := range m.transfers {
		if t.SessionID != sessionID || isTerminal(t.Status) {
			continue
		}
		t.Status = status
		t.FinishedAt = now
		t.ResultKnown = status != StatusResultUnknown
		out = append(out, t.clone())
	}
	return out
}

// Expire terminates transfers whose deadline passed. The device may still be
// reading, so the result is reported as unknown rather than failed.
func (m *Manager) Expire() []*Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var out []*Transfer
	for _, t := range m.transfers {
		if isTerminal(t.Status) || t.Deadline.IsZero() || now.Before(t.Deadline) {
			continue
		}
		t.Status = StatusResultUnknown
		t.FinishedAt = now
		t.ResultKnown = false
		out = append(out, t.clone())
	}
	return out
}

func isTerminal(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusCancelled, StatusResultUnknown:
		return true
	default:
		return false
	}
}
