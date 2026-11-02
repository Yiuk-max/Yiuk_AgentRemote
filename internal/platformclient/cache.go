package platformclient

import (
	"strings"

	"agentremote/internal/platform/protocol"
	"encoding/base64"
)

// taskCache 保存命令执行期间收到的输出块及最终状态。
type taskCache struct {
	sessionID   string
	status      string
	exitCode    *int
	truncated   bool
	resultKnown bool
	outputBytes int
	chunks      []protocol.OutputChunk
}

// downloadCache 暂存一次文件下载的分块和平台返回的完整性信息。
type downloadCache struct {
	sessionID   string
	status      string
	fileName    string
	fileSHA256  string
	resultKnown bool
	bytes       int64
	chunks      [][]byte
}

// sessionIDFromResult 从结构化会话结果中提取会话标识。
func sessionIDFromResult(result map[string]any) string {
	id, _ := result["session_id"].(string)
	return id
}

// aggregateResult 将流式分块合并为 stdout/stderr 字符串，供不需要分页读取输出的调用方使用
func aggregateResult(result map[string]any) map[string]any {
	var stdout, stderr strings.Builder
	switch chunks := result["chunks"].(type) {
	case []protocol.OutputChunk:
		for _, chunk := range chunks {
			if chunk.Stream == "stderr" {
				stderr.WriteString(chunk.Data)
			} else {
				stdout.WriteString(chunk.Data)
			}
		}
	case []any:
		for _, raw := range chunks {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			data, _ := entry["data"].(string)
			if stream, _ := entry["stream"].(string); stream == "stderr" {
				stderr.WriteString(data)
			} else {
				stdout.WriteString(data)
			}
		}
	}
	exitCode := result["exit_code"]
	if pointer, ok := exitCode.(*int); ok && pointer != nil {
		exitCode = *pointer
	}
	return map[string]any{
		"session_id":   result["session_id"],
		"request_id":   result["request_id"],
		"status":       result["status"],
		"exit_code":    exitCode,
		"stdout":       stdout.String(),
		"stderr":       stderr.String(),
		"truncated":    result["truncated"],
		"result_known": result["result_known"],
		"output_bytes": result["output_bytes"],
	}
}

// observe 根据消息类型更新会话、命令、文件和设备缓存。
func (c *Client) observe(m protocol.Message) {
	switch m.Type {
	case protocol.TypeSessionCreated, protocol.TypeSessionStatus, protocol.TypeSessionRequest:
		if m.Session != nil {
			c.cacheMu.Lock()
			c.sessions[m.Session.SessionID] = *m.Session
			c.cacheMu.Unlock()
		}
	case protocol.TypeCommandOutput:
		c.observeOutput(m)
	case protocol.TypeCommandResult:
		c.observeResult(m)
	case protocol.TypeFileChunk:
		c.observeFileChunk(m)
	case protocol.TypeFileDownloadResult:
		c.observeFileResult(m)
	case protocol.TypeDeviceList:
		c.cacheMu.Lock()
		c.devices = append([]protocol.DeviceInfo(nil), m.Devices...)
		c.cacheMu.Unlock()
	}
}

// observeOutput 缓存增量命令输出，并按序列号丢弃重复分块。
func (c *Client) observeOutput(m protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	t := c.task(m.SessionID, m.RequestID)
	if m.Data != "" {
		last := uint64(0)
		if len(t.chunks) > 0 {
			last = t.chunks[len(t.chunks)-1].Sequence
		}
		if m.Sequence > last {
			t.chunks = append(t.chunks, protocol.OutputChunk{Stream: m.Stream, Sequence: m.Sequence, Data: m.Data})
		}
	}
	t.truncated = t.truncated || m.Truncated
	if m.OutputBytes > 0 {
		t.outputBytes = m.OutputBytes
	} else if m.Data != "" {
		t.outputBytes += len(m.Data)
	}
}

// observeFileChunk 解码并缓存一个下载文件分块。
func (c *Client) observeFileChunk(m protocol.Message) {
	data, err := base64.StdEncoding.DecodeString(m.Data)
	if err != nil {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	d := c.download(m.SessionID, m.RequestID)
	d.chunks = append(d.chunks, data)
	d.bytes += int64(len(data))
}

// observeFileResult 记录下载结束状态和平台提供的文件元数据。
func (c *Client) observeFileResult(m protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	d := c.download(m.SessionID, m.RequestID)
	d.status = m.Status
	d.fileName = m.FileName
	d.fileSHA256 = m.FileSHA256
	d.resultKnown = m.ResultKnown
	if m.FileSize > 0 {
		d.bytes = m.FileSize
	}
}

// 调用 download 时必须持有 cacheMu
func (c *Client) download(sessionID, requestID string) *downloadCache {
	d, ok := c.downloads[requestID]
	if !ok || d.sessionID != sessionID {
		d = &downloadCache{sessionID: sessionID, status: "running"}
		c.downloads[requestID] = d
	}
	return d
}

// observeResult 合并最终响应中的输出块，并更新命令的终态信息。
func (c *Client) observeResult(m protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	t := c.task(m.SessionID, m.RequestID)
	last := uint64(0)
	if len(t.chunks) > 0 {
		last = t.chunks[len(t.chunks)-1].Sequence
	}
	for _, ch := range m.Chunks {
		if ch.Sequence > last {
			t.chunks = append(t.chunks, ch)
			last = ch.Sequence
		}
	}
	t.status = m.Status
	t.exitCode = m.ExitCode
	t.truncated = t.truncated || m.Truncated
	t.resultKnown = m.ResultKnown
	if m.OutputBytes > t.outputBytes {
		t.outputBytes = m.OutputBytes
	}
}

// 调用 task 时必须持有 cacheMu
func (c *Client) task(sessionID, requestID string) *taskCache {
	t, ok := c.tasks[requestID]
	if !ok || t.sessionID != sessionID {
		t = &taskCache{sessionID: sessionID, status: string(protocol.TaskRunning)}
		c.tasks[requestID] = t
	}
	return t
}

// cachedResult 在结果已进入终态时从本地缓存分页读取命令输出。
func (c *Client) cachedResult(sessionID, requestID string, cursor int) (map[string]any, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	t, ok := c.tasks[requestID]
	if !ok || t.sessionID != sessionID || !isTerminalTaskStatus(t.status) {
		return nil, false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(t.chunks) {
		cursor = len(t.chunks)
	}
	chunks := append([]protocol.OutputChunk(nil), t.chunks[cursor:]...)
	return map[string]any{
		"session_id":   sessionID,
		"request_id":   requestID,
		"status":       t.status,
		"exit_code":    t.exitCode,
		"truncated":    t.truncated,
		"result_known": t.resultKnown,
		"output_bytes": t.outputBytes,
		"chunks":       chunks,
		"next_cursor":  len(t.chunks),
	}, true
}

// sessionResult 将协议会话消息转换为稳定的工具返回字段。
func sessionResult(m protocol.Message) map[string]any {
	if m.Session != nil {
		return map[string]any{
			"session_id":      m.Session.SessionID,
			"device_id":       m.Session.DeviceID,
			"status":          m.Session.Status,
			"mode":            m.Session.Mode,
			"purpose":         m.Session.Purpose,
			"permissions":     m.Session.Permissions,
			"expires_at":      m.Session.ExpiresAt,
			"created_at":      m.Session.CreatedAt,
			"close_reason":    m.Session.CloseReason,
			"operator":        m.Session.Operator,
			"operator_device": m.Session.OperatorDevice,
		}
	}
	return map[string]any{"session_id": m.SessionID, "device_id": m.DeviceID, "status": m.Status}
}

// resultMap 将命令结果消息转换为工具返回结构。
func resultMap(m protocol.Message) map[string]any {
	return map[string]any{
		"session_id":   m.SessionID,
		"request_id":   m.RequestID,
		"status":       m.Status,
		"exit_code":    m.ExitCode,
		"truncated":    m.Truncated,
		"result_known": m.ResultKnown,
		"output_bytes": m.OutputBytes,
		"chunks":       m.Chunks,
		"next_cursor":  m.NextCursor,
	}
}

// isTerminalTaskStatus 判断命令状态是否已结束，不再需要等待后续结果。
func isTerminalTaskStatus(status string) bool {
	switch status {
	case string(protocol.TaskSucceeded), string(protocol.TaskFailed), string(protocol.TaskTimedOut), string(protocol.TaskCancelled), string(protocol.TaskResultUnknown):
		return true
	default:
		return false
	}
}

// isTerminalSession 判断会话状态是否已关闭、拒绝、过期或中断。
func isTerminalSession(status string) bool {
	switch status {
	case protocol.StatusClosed, protocol.StatusRejected, protocol.StatusExpired, protocol.StatusInterrupted:
		return true
	default:
		return false
	}
}
