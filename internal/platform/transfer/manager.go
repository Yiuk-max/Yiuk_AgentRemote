// Transfers 追踪内存中的上传和下载状态。
// 它验证块序列、块计数和总大小，而不保留文件内容。
// 调用方将每个经过验证的块转发给接收端。
package transfer

import (
	"agentremote/internal/platform/jobs"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// Status 是 jobs.Status 的别名：状态集合与终态判定由 jobs 包统一维护。
// 传输没有 timed_out 状态，但别名不妨碍它与任务共用同一套终态判定。
type Status = jobs.Status

const (
	StatusRunning       = jobs.StatusRunning
	StatusSucceeded     = jobs.StatusSucceeded
	StatusFailed        = jobs.StatusFailed
	StatusCancelled     = jobs.StatusCancelled
	StatusResultUnknown = jobs.StatusResultUnknown
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

// Transfer tracks the metadata and progress of one bounded upload or download.
type Transfer struct {
	RequestID        string
	SessionID        string
	CorrelationID    string
	Status           Status
	NextSequence     uint64
	ChunkCount       int
	TransferredBytes int64
	MaxBytes         int64
	FileName         string
	ResultKnown      bool
	StartedAt        time.Time
	Deadline         time.Time
	FinishedAt       time.Time
}

func (t *Transfer) clone() *Transfer {
	if t == nil {
		return nil
	}
	snapshot := *t
	return &snapshot
}

// Manager owns all transfer state.
type Manager struct {
	mu                     sync.Mutex
	transfers              map[string]*Transfer
	maxTransfersPerSession int
	now                    func() time.Time
}

func NewManager(maxTransfersPerSession int) *Manager {
	return newManager(maxTransfersPerSession, time.Now)
}

func newManager(maxTransfersPerSession int, now func() time.Time) *Manager {
	if maxTransfersPerSession < 1 {
		maxTransfersPerSession = 1
	}
	return &Manager{transfers: make(map[string]*Transfer), maxTransfersPerSession: maxTransfersPerSession, now: now}
}

// Start creates a running transfer or returns the existing transfer for the
// same session and request id. maxBytes comes from server configuration.
func (m *Manager) Start(sessionID, requestID, correlationID string, maxBytes int64, timeout time.Duration) (*Transfer, bool, error) {
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
	unfinishedTransferCount := 0
	for _, fileTransfer := range m.transfers {
		if fileTransfer.SessionID == sessionID && !fileTransfer.Status.Terminal() {
			unfinishedTransferCount++
		}
	}
	if unfinishedTransferCount >= m.maxTransfersPerSession {
		return nil, false, ErrTransferLimit
	}
	now := m.now()
	fileTransfer := &Transfer{
		RequestID:     requestID,
		SessionID:     sessionID,
		CorrelationID: correlationID,
		Status:        StatusRunning,
		NextSequence:  1,
		MaxBytes:      maxBytes,
		StartedAt:     now,
	}
	if timeout > 0 {
		fileTransfer.Deadline = now.Add(timeout)
	}
	m.transfers[requestID] = fileTransfer
	return fileTransfer.clone(), true, nil
}

// RecordChunk validates one encoded chunk and records its decoded size. File
// contents are intentionally discarded; the caller forwards the original
// message.
func (m *Manager) RecordChunk(sessionID, requestID string, sequence uint64, encodedChunk string) (*Transfer, error) {
	decodedChunk, err := base64.StdEncoding.DecodeString(encodedChunk)
	if err != nil {
		return nil, ErrInvalidBase64
	}
	if len(decodedChunk) == 0 {
		return nil, ErrEmptyChunk
	}
	if len(decodedChunk) > MaxChunkBytes {
		return nil, ErrChunkTooLarge
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fileTransfer, ok := m.transfers[requestID]
	if !ok || fileTransfer.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if fileTransfer.Status.Terminal() {
		return fileTransfer.clone(), ErrTerminal
	}
	if sequence != fileTransfer.NextSequence {
		return nil, ErrSequence
	}
	if fileTransfer.TransferredBytes+int64(len(decodedChunk)) > fileTransfer.MaxBytes {
		fileTransfer.Status = StatusFailed
		fileTransfer.FinishedAt = m.now()
		return fileTransfer.clone(), ErrSizeLimit
	}
	if fileTransfer.ChunkCount >= MaxChunks {
		fileTransfer.Status = StatusFailed
		fileTransfer.FinishedAt = m.now()
		return fileTransfer.clone(), ErrChunkLimit
	}
	fileTransfer.TransferredBytes += int64(len(decodedChunk))
	fileTransfer.ChunkCount++
	fileTransfer.NextSequence++
	return fileTransfer.clone(), nil
}

// Finish records the terminal transfer status. Terminal transfers are returned
// unchanged so a device retry is idempotent.
func (m *Manager) Finish(sessionID, requestID string, status Status, fileName string, fileSize int64, resultKnown bool) (*Transfer, error) {
	if !status.Terminal() {
		return nil, ErrInvalidStatus
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	fileTransfer, ok := m.transfers[requestID]
	if !ok || fileTransfer.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if fileTransfer.Status.Terminal() {
		return fileTransfer.clone(), nil
	}
	if status == StatusSucceeded && fileSize != fileTransfer.TransferredBytes {
		status = StatusResultUnknown
		resultKnown = false
	}
	fileTransfer.Status = status
	fileTransfer.FileName = fileName
	fileTransfer.FinishedAt = m.now()
	fileTransfer.ResultKnown = resultKnown && status != StatusResultUnknown
	return fileTransfer.clone(), nil
}

// FinishSession terminates every live transfer for a session and returns the
// terminal records so callers can notify the operator.
func (m *Manager) FinishSession(sessionID string, status Status) []*Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !status.Terminal() {
		status = StatusResultUnknown
	}
	now := m.now()
	var finishedTransfers []*Transfer
	for _, fileTransfer := range m.transfers {
		if fileTransfer.SessionID != sessionID || fileTransfer.Status.Terminal() {
			continue
		}
		fileTransfer.Status = status
		fileTransfer.FinishedAt = now
		fileTransfer.ResultKnown = status != StatusResultUnknown
		finishedTransfers = append(finishedTransfers, fileTransfer.clone())
	}
	return finishedTransfers
}

// Expire terminates transfers whose deadline passed. The device may still be
// reading or writing, so the result is reported as unknown rather than failed.
func (m *Manager) Expire() []*Transfer {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var expiredTransfers []*Transfer
	for _, fileTransfer := range m.transfers {
		if fileTransfer.Status.Terminal() || fileTransfer.Deadline.IsZero() || now.Before(fileTransfer.Deadline) {
			continue
		}
		fileTransfer.Status = StatusResultUnknown
		fileTransfer.FinishedAt = now
		fileTransfer.ResultKnown = false
		expiredTransfers = append(expiredTransfers, fileTransfer.clone())
	}
	return expiredTransfers
}

// Reap 回收终态传输记录。
//
// 传输记录只存记账信息（序号、累计字节、名字、哈希），不存文件内容，
// 所以只需要按时间回收。回收后对应的 request_id 可以重新使用。
// 返回回收条数。
func (m *Manager) Reap(retain time.Duration) int {
	if retain <= 0 {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	removed := 0
	for requestID, fileTransfer := range m.transfers {
		if !jobs.Expired(fileTransfer.Status, fileTransfer.FinishedAt, now, retain) {
			continue
		}
		delete(m.transfers, requestID)
		removed++
	}
	return removed
}
