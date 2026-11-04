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

func (commandTask *Task) clone() *Task {
	if commandTask == nil {
		return nil
	}
	snapshot := *commandTask
	snapshot.Chunks = append([]Chunk(nil), commandTask.Chunks...)
	if commandTask.ExitCode != nil {
		exitCodeCopy := *commandTask.ExitCode
		snapshot.ExitCode = &exitCodeCopy
	}
	return &snapshot
}

// Manager 拥有所有的任务状态
type Manager struct {
	mu                 sync.Mutex
	tasks              map[string]*Task
	maxOutputBytes     int
	maxTasksPerSession int
	now                func() time.Time
}

// NewManager 创建一个任务管理器。
// maxOutputBytes 是每个任务的 stdout 和 stderr 总字节预算；
// maxTasksPerSession 限制同时排队或运行的任务数。
func NewManager(maxOutputBytes, maxTasksPerSession int) *Manager {
	return newManager(maxOutputBytes, maxTasksPerSession, time.Now)
}

func newManager(maxOutputBytes, maxTasksPerSession int, now func() time.Time) *Manager {
	if maxOutputBytes < 1 {
		maxOutputBytes = 1 << 20
	}
	if maxTasksPerSession < 1 {
		maxTasksPerSession = 1
	}
	return &Manager{tasks: make(map[string]*Task), maxOutputBytes: maxOutputBytes, maxTasksPerSession: maxTasksPerSession, now: now}
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
	unfinishedTaskCount := 0
	// 统计该会话尚未结束的任务。
	for _, commandTask := range m.tasks {
		if commandTask.SessionID == sessionID && !isTerminal(commandTask.Status) {
			unfinishedTaskCount++
		}
	}
	if unfinishedTaskCount >= m.maxTasksPerSession {
		return nil, false, ErrTaskLimit
	}
	now := m.now()
	commandTask := &Task{
		RequestID:   requestID,
		SessionID:   sessionID,
		Status:      StatusRunning,
		StartedAt:   now,
		ResultKnown: false,
	}
	if timeout > 0 {
		commandTask.Deadline = now.Add(timeout)
	}
	m.tasks[requestID] = commandTask
	return commandTask.clone(), true, nil
}

// AppendOutput 会记录一个输出块，同时执行每个任务的字节额度
// 超出部分的数据会被丢弃，任务会被标记为截断
func (m *Manager) AppendOutput(sessionID, requestID, stream, data string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	commandTask, ok := m.tasks[requestID]
	if !ok || commandTask.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(commandTask.Status) {
		return commandTask.clone(), ErrTerminal
	}
	if stream != StreamStdout && stream != StreamStderr {
		return nil, errors.New("invalid output stream")
	}
	if data == "" {
		return commandTask.clone(), nil
	}
	remainingOutputBytes := m.maxOutputBytes - commandTask.OutputBytes
	if remainingOutputBytes <= 0 {
		commandTask.Truncated = true
		return commandTask.clone(), nil
	}
	if len(data) > remainingOutputBytes {
		data = data[:remainingOutputBytes]
		commandTask.Truncated = true
	}
	commandTask.OutputBytes += len(data)
	commandTask.Chunks = append(commandTask.Chunks, Chunk{Stream: stream, Sequence: uint64(len(commandTask.Chunks) + 1), Data: data})
	return commandTask.clone(), nil
}

// Finish 记录了任务的终端状态。终端任务会原样返
func (m *Manager) Finish(sessionID, requestID string, status Status, exitCode *int, resultKnown bool) (*Task, error) {
	if !isTerminal(status) {
		return nil, ErrInvalidStatus
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	commandTask, ok := m.tasks[requestID]
	if !ok || commandTask.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(commandTask.Status) {
		return commandTask.clone(), nil
	}
	commandTask.Status = status
	commandTask.FinishedAt = m.now()
	commandTask.ResultKnown = resultKnown && status != StatusResultUnknown
	if exitCode != nil {
		exitCodeCopy := *exitCode
		commandTask.ExitCode = &exitCodeCopy
	}
	return commandTask.clone(), nil
}

// RequestCancel 标记取消请求；排队任务立即取消，运行中的任务等待设备确认。
func (m *Manager) RequestCancel(sessionID, requestID string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	commandTask, ok := m.tasks[requestID]
	if !ok || commandTask.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if isTerminal(commandTask.Status) {
		return commandTask.clone(), nil
	}
	commandTask.CancelRequested = true
	if commandTask.Status == StatusQueued {
		commandTask.Status = StatusCancelled
		commandTask.FinishedAt = m.now()
	}
	return commandTask.clone(), nil
}

// 获取任务信息副本
func (m *Manager) Get(sessionID, requestID string) (*Task, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	commandTask, ok := m.tasks[requestID]
	if !ok || commandTask.SessionID != sessionID {
		return nil, false
	}
	return commandTask.clone(), true
}

// 给session返回任务副本
func (m *Manager) ListBySession(sessionID string) []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	var taskSnapshots []*Task
	for _, commandTask := range m.tasks {
		if commandTask.SessionID == sessionID {
			taskSnapshots = append(taskSnapshots, commandTask.clone())
		}
	}
	return taskSnapshots
}

// CountUnfinishedBySession 返回该会话尚未结束的任务数。
func (m *Manager) CountUnfinishedBySession(sessionID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, commandTask := range m.tasks {
		if commandTask.SessionID == sessionID && !isTerminal(commandTask.Status) {
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
	var finishedTasks []*Task
	for _, commandTask := range m.tasks {
		if commandTask.SessionID != sessionID || isTerminal(commandTask.Status) {
			continue
		}
		commandTask.Status = finishStatus
		commandTask.FinishedAt = now
		commandTask.ResultKnown = finishStatus != StatusResultUnknown
		finishedTasks = append(finishedTasks, commandTask.clone())
	}
	return finishedTasks
}

// 将服务器端截止时间已过的正在运行任务标记为超时
// 本地设备可能仍在完成该进程；因此结果会被报告为不完全确定
func (m *Manager) Expire() []*Task {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	var timedOutTasks []*Task
	for _, commandTask := range m.tasks {
		if isTerminal(commandTask.Status) || commandTask.Deadline.IsZero() || now.Before(commandTask.Deadline) {
			continue
		}
		commandTask.Status = StatusTimedOut
		commandTask.FinishedAt = now
		commandTask.ResultKnown = false
		timedOutTasks = append(timedOutTasks, commandTask.clone())
	}
	return timedOutTasks
}

// ResultView 是从光标开始的输出的有界切片
type ResultView struct {
	Task       *Task
	Chunks     []Chunk
	NextCursor int
}

// ResultFromCursor 返回游标开始处的输出块及任务快照。
// 游标是块索引，不是字节偏移量。
func (m *Manager) ResultFromCursor(sessionID, requestID string, cursor int) (*ResultView, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	commandTask, ok := m.tasks[requestID]
	if !ok || commandTask.SessionID != sessionID {
		return nil, ErrNotFound
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(commandTask.Chunks) {
		cursor = len(commandTask.Chunks)
	}
	chunks := append([]Chunk(nil), commandTask.Chunks[cursor:]...)
	return &ResultView{Task: commandTask.clone(), Chunks: chunks, NextCursor: len(commandTask.Chunks)}, nil
}

func isTerminal(status Status) bool {
	switch status {
	case StatusSucceeded, StatusFailed, StatusTimedOut, StatusCancelled, StatusResultUnknown:
		return true
	default:
		return false
	}
}
