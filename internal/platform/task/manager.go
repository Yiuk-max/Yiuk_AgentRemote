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

// 命令任务、request_id 幂等、输出字节预算、deadline、游标分页
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

// Chunk 是一个有界的输出块
type Chunk struct {
	Stream   string
	Sequence uint64
	Data     string
}

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

// Manager 拥有所有的任务状态
type Manager struct {
	mu            sync.Mutex
	tasks         map[string]*Task
	maxOutput     int
	maxPerSession int
	now           func() time.Time
}

// NewManager 创建一个任务管理器。
// maxOutput 是每个任务的 stdout 和 stderr 总字节预算；
// maxPerSession 限制同时排队/运行的任务数
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

// Start 会创建一个正在运行的任务，或者返回重复请求 ID 的已有任务
// 当创建了新任务时，bool 结果为 true。正的超时时间会设置服务器端的截止时间，这样无法响应的设备就不会永远占用
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
	//找到设备，已激活任务数量+1
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

// AppendOutput 会记录一个输出块，同时执行每个任务的字节额度
// 超出部分的数据会被丢弃，任务会被标记为截断
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

// Finish 记录了任务的终端状态。终端任务会原样返
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

// CancelRequested 表示已请求取消。如果任务尚未开始执行，它会立即被取消
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

// 获取任务信息副本
func (m *Manager) Get(sessionID, requestID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[requestID]
	if !ok || t.SessionID != sessionID {
		return nil, false
	}
	return t.clone(), true
}

// 给session返回任务副本
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

// 返回一个会话正在排队、处理的任务数量
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

// FinishSession 会切换某个会话的每个实时任务。
// finishStatus 用于本地结果已知的任务（例如在所有者停止后被取消）；
// 否则，对于不明确的断开情况则使用 result_unknown
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

// 将服务器端截止时间已过的正在运行任务标记为超时
// 本地设备可能仍在完成该进程；因此结果会被报告为不完全确定
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

// ResultView 是从光标开始的输出的有界切片
type ResultView struct {
	Task       *Task
	Chunks     []Chunk
	NextCursor int
}

// Result 会返回光标后的输出块。
// 光标是块索引，而不是字节偏移量，所以客户端可以确定性地分页
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
