package platformclient

import (
	"strings"

	"agentremote/internal/platform/protocol"
	"encoding/base64"
)

// commandResultCache 保存命令执行期间收到的输出块及最终状态。
type commandResultCache struct {
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
	totalBytes  int64
	chunks      [][]byte
}

// sessionIDFromResult 从结构化会话结果中提取会话标识。
func sessionIDFromResult(result map[string]any) string {
	sessionID, _ := result["session_id"].(string)
	return sessionID
}

// aggregateCommandOutput 将命令输出分块合并为 stdout/stderr 字符串。
func aggregateCommandOutput(result map[string]any) map[string]any {
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

// cacheMessage 根据消息类型更新会话、命令、下载和设备缓存。
func (c *Client) cacheMessage(message protocol.Message) {
	switch message.Type {
	case protocol.TypeSessionCreated, protocol.TypeSessionStatus, protocol.TypeSessionRequest:
		if message.Session != nil {
			c.cacheMu.Lock()
			c.cachedSessions[message.Session.SessionID] = *message.Session
			c.cacheMu.Unlock()
		}
	case protocol.TypeCommandOutput:
		c.cacheCommandOutput(message)
	case protocol.TypeCommandResult:
		c.cacheCommandResult(message)
	case protocol.TypeFileChunk:
		c.cacheDownloadChunk(message)
	case protocol.TypeFileDownloadResult:
		c.cacheDownloadResult(message)
	case protocol.TypeDeviceList:
		c.cacheMu.Lock()
		c.cachedDevices = append([]protocol.DeviceInfo(nil), message.Devices...)
		c.cacheMu.Unlock()
	}
}

// cacheCommandOutput 缓存增量命令输出，并按序列号丢弃重复分块。
func (c *Client) cacheCommandOutput(message protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	commandCache := c.getOrCreateCommandCache(message.SessionID, message.RequestID)
	if message.Data != "" {
		lastSequence := uint64(0)
		if len(commandCache.chunks) > 0 {
			lastSequence = commandCache.chunks[len(commandCache.chunks)-1].Sequence
		}
		if message.Sequence > lastSequence {
			commandCache.chunks = append(commandCache.chunks, protocol.OutputChunk{Stream: message.Stream, Sequence: message.Sequence, Data: message.Data})
		}
	}
	commandCache.truncated = commandCache.truncated || message.Truncated
	if message.OutputBytes > 0 {
		commandCache.outputBytes = message.OutputBytes
	} else if message.Data != "" {
		commandCache.outputBytes += len(message.Data)
	}
}

// cacheDownloadChunk 解码并缓存一个下载文件分块。
func (c *Client) cacheDownloadChunk(message protocol.Message) {
	data, err := base64.StdEncoding.DecodeString(message.Data)
	if err != nil {
		return
	}
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	download := c.getOrCreateDownloadCache(message.SessionID, message.RequestID)
	download.chunks = append(download.chunks, data)
	download.totalBytes += int64(len(data))
}

// cacheDownloadResult 记录下载结束状态和平台提供的文件元数据。
func (c *Client) cacheDownloadResult(message protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	download := c.getOrCreateDownloadCache(message.SessionID, message.RequestID)
	download.status = message.Status
	download.fileName = message.FileName
	download.fileSHA256 = message.FileSHA256
	download.resultKnown = message.ResultKnown
	if message.FileSize > 0 {
		download.totalBytes = message.FileSize
	}
}

// getOrCreateDownloadCache 查找或创建下载缓存；调用方须持有 cacheMu。
func (c *Client) getOrCreateDownloadCache(sessionID, requestID string) *downloadCache {
	download, exists := c.cachedDownloads[requestID]
	if !exists || download.sessionID != sessionID {
		download = &downloadCache{sessionID: sessionID, status: "running"}
		c.cachedDownloads[requestID] = download
	}
	return download
}

// cacheCommandResult 合并结果响应中的输出块，并更新命令状态。
func (c *Client) cacheCommandResult(message protocol.Message) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	commandCache := c.getOrCreateCommandCache(message.SessionID, message.RequestID)
	lastSequence := uint64(0)
	if len(commandCache.chunks) > 0 {
		lastSequence = commandCache.chunks[len(commandCache.chunks)-1].Sequence
	}
	for _, chunk := range message.Chunks {
		if chunk.Sequence > lastSequence {
			commandCache.chunks = append(commandCache.chunks, chunk)
			lastSequence = chunk.Sequence
		}
	}
	commandCache.status = message.Status
	commandCache.exitCode = message.ExitCode
	commandCache.truncated = commandCache.truncated || message.Truncated
	commandCache.resultKnown = message.ResultKnown
	if message.OutputBytes > commandCache.outputBytes {
		commandCache.outputBytes = message.OutputBytes
	}
}

// getOrCreateCommandCache 查找或创建命令结果缓存；调用方须持有 cacheMu。
func (c *Client) getOrCreateCommandCache(sessionID, requestID string) *commandResultCache {
	commandCache, exists := c.cachedCommands[requestID]
	if !exists || commandCache.sessionID != sessionID {
		commandCache = &commandResultCache{sessionID: sessionID, status: string(protocol.TaskRunning)}
		c.cachedCommands[requestID] = commandCache
	}
	return commandCache
}

// cachedFinalCommandResult 在结果已进入终态时从本地缓存分页读取命令输出。
func (c *Client) cachedFinalCommandResult(sessionID, requestID string, cursor int) (map[string]any, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	commandCache, exists := c.cachedCommands[requestID]
	if !exists || commandCache.sessionID != sessionID || !isTerminalTaskStatus(commandCache.status) {
		return nil, false
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(commandCache.chunks) {
		cursor = len(commandCache.chunks)
	}
	chunks := append([]protocol.OutputChunk(nil), commandCache.chunks[cursor:]...)
	return map[string]any{
		"session_id":   sessionID,
		"request_id":   requestID,
		"status":       commandCache.status,
		"exit_code":    commandCache.exitCode,
		"truncated":    commandCache.truncated,
		"result_known": commandCache.resultKnown,
		"output_bytes": commandCache.outputBytes,
		"chunks":       chunks,
		"next_cursor":  len(commandCache.chunks),
	}, true
}

// sessionToolResult 将协议会话消息转换为稳定的工具返回字段。
func sessionToolResult(message protocol.Message) map[string]any {
	if message.Session != nil {
		return map[string]any{
			"session_id":      message.Session.SessionID,
			"device_id":       message.Session.DeviceID,
			"status":          message.Session.Status,
			"mode":            message.Session.Mode,
			"purpose":         message.Session.Purpose,
			"permissions":     message.Session.Permissions,
			"expires_at":      message.Session.ExpiresAt,
			"created_at":      message.Session.CreatedAt,
			"close_reason":    message.Session.CloseReason,
			"operator":        message.Session.Operator,
			"operator_device": message.Session.OperatorDevice,
		}
	}
	return map[string]any{"session_id": message.SessionID, "device_id": message.DeviceID, "status": message.Status}
}

// commandToolResult 将命令结果消息转换为工具返回结构。
func commandToolResult(message protocol.Message) map[string]any {
	return map[string]any{
		"session_id":   message.SessionID,
		"request_id":   message.RequestID,
		"status":       message.Status,
		"exit_code":    message.ExitCode,
		"truncated":    message.Truncated,
		"result_known": message.ResultKnown,
		"output_bytes": message.OutputBytes,
		"chunks":       message.Chunks,
		"next_cursor":  message.NextCursor,
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

// isTerminalSessionStatus 判断会话状态是否已关闭、拒绝、过期或中断。
func isTerminalSessionStatus(status string) bool {
	switch status {
	case protocol.StatusClosed, protocol.StatusRejected, protocol.StatusExpired, protocol.StatusInterrupted:
		return true
	default:
		return false
	}
}
