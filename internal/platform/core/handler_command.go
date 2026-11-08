package core

import (
	"strings"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
)

// 处理命令执行请求
func (s *Service) handleExecuteCommand(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if strings.TrimSpace(message.Command) == "" || len(message.Command) > s.config.CommandMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "command is required and bounded")
	}
	if len(message.WorkingDirectory) > s.config.WorkingDirMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "working_directory is too long")
	}
	timeoutSeconds := message.TimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	if timeoutSeconds < 1 || timeoutSeconds > 3600 {
		return newProtocolError("INVALID_ARGUMENT", "timeout_seconds must be between 1 and 3600")
	}

	sessionRecord, err := s.sessions.Authorize(message.SessionID, caller.ConnectionID, session.PermissionExecuteCommand)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	// 连接是否还活着不再在这里查：投递失败就是“设备不可用”，
	// 下面 send 失败的既有分支会把任务/传输收敛成终态。
	deviceConnectionID, ok := s.devices.OnlineConnection(sessionRecord.DeviceID)
	if !ok || deviceConnectionID != sessionRecord.DeviceConnectionID {
		return newProtocolError("DEVICE_OFFLINE", "device is no longer online")
	}

	commandTask, created, err := s.tasks.Start(sessionRecord.ID, message.RequestID, time.Duration(timeoutSeconds)*time.Second)
	if err != nil {
		return s.taskProtocolError(err)
	}
	if !created {
		return s.sendResponse(caller, message, protocol.Message{
			Type:      protocol.TypeCommandAccepted,
			SessionID: sessionRecord.ID,
			RequestID: commandTask.RequestID,
			Status:    string(commandTask.Status),
		})
	}
	if _, err := s.sessions.Activate(sessionRecord.ID, caller.ConnectionID); err != nil {
		s.tasks.Finish(sessionRecord.ID, message.RequestID, task.StatusResultUnknown, nil, false)
		return s.sessionProtocolError(err)
	}

	forward := protocol.Message{
		Type:             protocol.TypeExecuteCommand,
		SessionID:        sessionRecord.ID,
		RequestID:        commandTask.RequestID,
		Command:          message.Command,
		WorkingDirectory: message.WorkingDirectory,
		TimeoutSeconds:   timeoutSeconds,
	}
	if !s.send(sessionRecord.DeviceConnectionID, forward) {
		s.tasks.Finish(sessionRecord.ID, message.RequestID, task.StatusResultUnknown, nil, false)
		return newProtocolError("DEVICE_UNAVAILABLE", "could not deliver command to device")
	}
	s.audit.Event("command_submitted", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(commandTask.RequestID), "device", audit.RedactID(sessionRecord.DeviceID))
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeCommandAccepted,
		SessionID: sessionRecord.ID,
		RequestID: commandTask.RequestID,
		Status:    string(commandTask.Status),
	})
}

// 处理命令执行输出
func (s *Service) handleCommandOutput(caller *Caller, message protocol.Message) error {
	if caller.Role != RoleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can send command output")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.DeviceID != caller.DeviceID || sessionRecord.DeviceConnectionID != caller.ConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	commandTask, err := s.tasks.AppendOutput(sessionRecord.ID, message.RequestID, message.Stream, message.Data)
	if err != nil {
		return s.taskProtocolError(err)
	}
	sequence := uint64(0)
	data := ""
	if chunkCount := len(commandTask.Chunks); chunkCount > 0 {
		sequence = commandTask.Chunks[chunkCount-1].Sequence
		data = commandTask.Chunks[chunkCount-1].Data
	}
	if data == "" && !commandTask.Truncated {
		return nil
	}
	s.send(sessionRecord.OperatorConnectionID, protocol.Message{
		Type:        protocol.TypeCommandOutput,
		SessionID:   sessionRecord.ID,
		RequestID:   message.RequestID,
		Stream:      message.Stream,
		Data:        data,
		Sequence:    sequence,
		Truncated:   commandTask.Truncated,
		OutputBytes: commandTask.OutputBytes,
	})
	return nil
}

// 处理命令执行结果
func (s *Service) handleCommandResult(caller *Caller, message protocol.Message) error {
	if caller.Role != RoleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can report a command result")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.DeviceID != caller.DeviceID || sessionRecord.DeviceConnectionID != caller.ConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	status, err := parseTerminalTaskStatus(message.Status)
	if err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	resultKnown := message.ResultKnown || status != task.StatusResultUnknown
	commandTask, err := s.tasks.Finish(sessionRecord.ID, message.RequestID, status, message.ExitCode, resultKnown)
	if err != nil {
		return s.taskProtocolError(err)
	}
	s.send(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
	s.audit.Event("command_finished", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(commandTask.RequestID), "status", string(commandTask.Status), "truncated", commandTask.Truncated)
	return nil
}

// 处理获取命令执行结果
func (s *Service) handleGetCommandResult(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(caller, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	view, err := s.tasks.ResultFromCursor(sessionRecord.ID, message.RequestID, message.Cursor)
	if err != nil {
		return s.taskProtocolError(err)
	}
	chunks := make([]protocol.OutputChunk, 0, len(view.Chunks))
	for _, chunk := range view.Chunks {
		chunks = append(chunks, protocol.OutputChunk{Stream: chunk.Stream, Sequence: chunk.Sequence, Data: chunk.Data})
	}
	commandTask := view.Task
	return s.sendResponse(caller, message, protocol.Message{
		Type:        protocol.TypeCommandResult,
		SessionID:   commandTask.SessionID,
		RequestID:   commandTask.RequestID,
		Status:      string(commandTask.Status),
		ExitCode:    commandTask.ExitCode,
		ResultKnown: commandTask.ResultKnown,
		Truncated:   commandTask.Truncated,
		OutputBytes: commandTask.OutputBytes,
		Chunks:      chunks,
		NextCursor:  view.NextCursor,
	})
}

// 处理取消命令执行请求
func (s *Service) handleCancelCommand(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(caller, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	commandTask, err := s.tasks.RequestCancel(sessionRecord.ID, message.RequestID)
	if err != nil {
		return s.taskProtocolError(err)
	}
	// Device-initiated cancel means the local process is already stopping.
	if caller.Role == RoleDevice && sessionRecord.DeviceID == caller.DeviceID {
		commandTask, err = s.tasks.Finish(sessionRecord.ID, message.RequestID, task.StatusCancelled, nil, true)
		if err != nil {
			return s.taskProtocolError(err)
		}
		s.send(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
	} else {
		if !s.send(sessionRecord.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCancelCommand,
			SessionID: sessionRecord.ID,
			RequestID: message.RequestID,
		}) {
			commandTask, _ = s.tasks.Finish(sessionRecord.ID, message.RequestID, task.StatusResultUnknown, nil, false)
		}
	}
	s.audit.Event("command_cancel_requested", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(message.RequestID), "status", string(commandTask.Status))
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeCommandCanceled,
		SessionID: sessionRecord.ID,
		RequestID: message.RequestID,
		Status:    string(commandTask.Status),
	})
}

// 任务结果消息
func commandResultMessage(commandTask *task.Task) protocol.Message {
	if commandTask == nil {
		return protocol.Message{Type: protocol.TypeCommandResult}
	}
	return protocol.Message{
		Type:        protocol.TypeCommandResult,
		SessionID:   commandTask.SessionID,
		RequestID:   commandTask.RequestID,
		Status:      string(commandTask.Status),
		ExitCode:    commandTask.ExitCode,
		Truncated:   commandTask.Truncated,
		ResultKnown: commandTask.ResultKnown,
		OutputBytes: commandTask.OutputBytes,
	}
}
