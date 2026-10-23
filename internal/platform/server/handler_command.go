package server

import (
	"strings"
	"time"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/task"
)

// 处理命令执行请求
func (s *Server) handleExecuteCommand(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if strings.TrimSpace(m.Command) == "" || len(m.Command) > s.cfg.CommandMaxLen {
		return perr("INVALID_ARGUMENT", "command is required and bounded")
	}
	if len(m.WorkingDirectory) > s.cfg.WorkingDirMaxLen {
		return perr("INVALID_ARGUMENT", "working_directory is too long")
	}
	timeout := m.TimeoutSeconds
	if timeout <= 0 {
		timeout = 60
	}
	if timeout < 1 || timeout > 3600 {
		return perr("INVALID_ARGUMENT", "timeout_seconds must be between 1 and 3600")
	}

	ses, err := s.sessions.Authorize(m.SessionID, c.id, session.PermissionExecuteCommand)
	if err != nil {
		return s.sessionError(err)
	}
	onlineID, ok := s.devices.OnlineConnection(ses.DeviceID)
	if !ok || onlineID != ses.DeviceConnectionID || s.lookupConn(onlineID) == nil {
		return perr("DEVICE_OFFLINE", "device is no longer online")
	}

	t, created, err := s.tasks.Start(ses.ID, m.RequestID, time.Duration(timeout)*time.Second)
	if err != nil {
		return s.taskError(err)
	}
	if !created {
		return s.sendOK(c, m, protocol.Message{
			Type:      protocol.TypeCommandAccepted,
			SessionID: ses.ID,
			RequestID: t.RequestID,
			Status:    string(t.Status),
		})
	}
	if _, err := s.sessions.Activate(ses.ID, c.id); err != nil {
		s.tasks.Finish(ses.ID, m.RequestID, task.StatusResultUnknown, nil, false)
		return s.sessionError(err)
	}

	forward := protocol.Message{
		Type:             protocol.TypeExecuteCommand,
		SessionID:        ses.ID,
		RequestID:        t.RequestID,
		Command:          m.Command,
		WorkingDirectory: m.WorkingDirectory,
		TimeoutSeconds:   timeout,
	}
	if !s.sendToConnection(ses.DeviceConnectionID, forward) {
		s.tasks.Finish(ses.ID, m.RequestID, task.StatusResultUnknown, nil, false)
		return perr("DEVICE_UNAVAILABLE", "could not deliver command to device")
	}
	s.audit.Event("command_submitted", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID), "device", audit.RedactID(ses.DeviceID))
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeCommandAccepted,
		SessionID: ses.ID,
		RequestID: t.RequestID,
		Status:    string(t.Status),
	})
}

// 处理命令执行输出
func (s *Server) handleCommandOutput(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can send command output")
	}
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || ses.DeviceID != c.deviceID || ses.DeviceConnectionID != c.id {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	t, err := s.tasks.AppendOutput(ses.ID, m.RequestID, m.Stream, m.Data)
	if err != nil {
		return s.taskError(err)
	}
	sequence := uint64(0)
	data := ""
	if n := len(t.Chunks); n > 0 {
		sequence = t.Chunks[n-1].Sequence
		data = t.Chunks[n-1].Data
	}
	if data == "" && !t.Truncated {
		return nil
	}
	s.sendToConnection(ses.OperatorConnectionID, protocol.Message{
		Type:        protocol.TypeCommandOutput,
		SessionID:   ses.ID,
		RequestID:   m.RequestID,
		Stream:      m.Stream,
		Data:        data,
		Sequence:    sequence,
		Truncated:   t.Truncated,
		OutputBytes: t.OutputBytes,
	})
	return nil
}

// 处理命令执行结果
func (s *Server) handleCommandResult(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can report a command result")
	}
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || ses.DeviceID != c.deviceID || ses.DeviceConnectionID != c.id {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	status, err := terminalTaskStatus(m.Status)
	if err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	resultKnown := m.ResultKnown || status != task.StatusResultUnknown
	t, err := s.tasks.Finish(ses.ID, m.RequestID, status, m.ExitCode, resultKnown)
	if err != nil {
		return s.taskError(err)
	}
	s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
	s.audit.Event("command_finished", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID), "status", string(t.Status), "truncated", t.Truncated)
	return nil
}

// 处理获取命令执行结果
func (s *Server) handleGetCommandResult(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	view, err := s.tasks.Result(ses.ID, m.RequestID, m.Cursor)
	if err != nil {
		return s.taskError(err)
	}
	chunks := make([]protocol.OutputChunk, 0, len(view.Chunks))
	for _, ch := range view.Chunks {
		chunks = append(chunks, protocol.OutputChunk{Stream: ch.Stream, Sequence: ch.Sequence, Data: ch.Data})
	}
	t := view.Task
	return s.sendOK(c, m, protocol.Message{
		Type:        protocol.TypeCommandResult,
		SessionID:   t.SessionID,
		RequestID:   t.RequestID,
		Status:      string(t.Status),
		ExitCode:    t.ExitCode,
		ResultKnown: t.ResultKnown,
		Truncated:   t.Truncated,
		OutputBytes: t.OutputBytes,
		Chunks:      chunks,
		NextCursor:  view.NextCursor,
	})
}

// 处理取消命令执行请求
func (s *Server) handleCancelCommand(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	t, err := s.tasks.CancelRequested(ses.ID, m.RequestID)
	if err != nil {
		return s.taskError(err)
	}
	// Device-initiated cancel means the local process is already stopping.
	if c.role == roleDevice && ses.DeviceID == c.deviceID {
		t, err = s.tasks.Finish(ses.ID, m.RequestID, task.StatusCancelled, nil, true)
		if err != nil {
			return s.taskError(err)
		}
		s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
	} else {
		if !s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCancelCommand,
			SessionID: ses.ID,
			RequestID: m.RequestID,
		}) {
			t, _ = s.tasks.Finish(ses.ID, m.RequestID, task.StatusResultUnknown, nil, false)
		}
	}
	s.audit.Event("command_cancel_requested", "session", audit.RedactID(ses.ID), "request", audit.RedactID(m.RequestID), "status", string(t.Status))
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeCommandCanceled,
		SessionID: ses.ID,
		RequestID: m.RequestID,
		Status:    string(t.Status),
	})
}

// 任务结果消息
func taskResultMessage(t *task.Task) protocol.Message {
	if t == nil {
		return protocol.Message{Type: protocol.TypeCommandResult}
	}
	return protocol.Message{
		Type:        protocol.TypeCommandResult,
		SessionID:   t.SessionID,
		RequestID:   t.RequestID,
		Status:      string(t.Status),
		ExitCode:    t.ExitCode,
		Truncated:   t.Truncated,
		ResultKnown: t.ResultKnown,
		OutputBytes: t.OutputBytes,
	}
}
