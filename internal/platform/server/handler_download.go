package server

import (
	"errors"
	"strings"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/transfer"
)

// 处理文件下载请求
func (s *Server) handleDownloadFile(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if strings.TrimSpace(m.RemotePath) == "" || len(m.RemotePath) > s.cfg.WorkingDirMaxLen {
		return perr("INVALID_ARGUMENT", "remote_path is required and bounded")
	}
	ses, err := s.sessions.Authorize(m.SessionID, c.id, session.PermissionReadFile)
	if err != nil {
		return s.sessionError(err)
	}
	onlineID, ok := s.devices.OnlineConnection(ses.DeviceID)
	if !ok || onlineID != ses.DeviceConnectionID || s.lookupConn(onlineID) == nil {
		return perr("DEVICE_OFFLINE", "device is no longer online")
	}
	t, created, err := s.transfers.Start(ses.ID, m.RequestID, m.CorrelationID, int64(s.cfg.MaxDownloadBytes), downloadTransferTimeout)
	if err != nil {
		return s.transferError(err)
	}
	if !created {
		return perr("TRANSFER_IN_PROGRESS", "a download with this request id is already in progress")
	}
	if _, err := s.sessions.Activate(ses.ID, c.id); err != nil {
		s.transfers.Finish(ses.ID, t.RequestID, transfer.StatusFailed, "", 0, true)
		return s.sessionError(err)
	}
	if !s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
		Type:       protocol.TypeDownloadFile,
		SessionID:  ses.ID,
		RequestID:  t.RequestID,
		RemotePath: m.RemotePath,
	}) {
		s.transfers.Finish(ses.ID, t.RequestID, transfer.StatusResultUnknown, "", 0, false)
		return perr("DEVICE_UNAVAILABLE", "could not deliver file request to device")
	}
	s.audit.Event("file_download_submitted", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID), "device", audit.RedactID(ses.DeviceID))
	return nil
}

// 处理文件下载分片
func (s *Server) handleFileChunk(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can send file chunks")
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
	t, err := s.transfers.Append(ses.ID, m.RequestID, m.Sequence, m.Data)
	if err != nil {
		if errors.Is(err, transfer.ErrSizeLimit) || errors.Is(err, transfer.ErrChunkLimit) {
			s.sendToConnection(ses.OperatorConnectionID, transferResultMessage(t))
			s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
				Type: protocol.TypeCancelFileDownload, SessionID: ses.ID, RequestID: m.RequestID,
			})
			s.audit.Warn("file_download_limit_exceeded", "session", audit.RedactID(ses.ID), "request", audit.RedactID(m.RequestID))
		}
		return s.transferError(err)
	}
	s.sendToConnection(ses.OperatorConnectionID, protocol.Message{
		Type:      protocol.TypeFileChunk,
		SessionID: ses.ID,
		RequestID: m.RequestID,
		Sequence:  m.Sequence,
		Data:      m.Data,
		EOF:       m.EOF,
	})
	return nil
}

// 处理文件下载结果
func (s *Server) handleFileDownloadResult(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can report a file download result")
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
	status, err := terminalTransferStatus(m.Status)
	if err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if len(m.FileName) > 255 || len(m.FileSHA256) > 128 {
		return perr("INVALID_ARGUMENT", "file metadata is too long")
	}
	t, err := s.transfers.Finish(ses.ID, m.RequestID, status, m.FileName, m.FileSize, m.ResultKnown)
	if err != nil {
		return s.transferError(err)
	}
	msg := transferResultMessage(t)
	msg.FileSHA256 = m.FileSHA256
	s.sendToConnection(ses.OperatorConnectionID, msg)
	s.audit.Event("file_download_finished", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID), "status", string(t.Status), "bytes", t.Bytes)
	return nil
}

// 传输终端状态
func transferResultMessage(t *transfer.Transfer) protocol.Message {
	if t == nil {
		return protocol.Message{Type: protocol.TypeFileDownloadResult}
	}
	msg := protocol.Message{
		Type:          protocol.TypeFileDownloadResult,
		SessionID:     t.SessionID,
		RequestID:     t.RequestID,
		CorrelationID: t.Correlation,
		Status:        string(t.Status),
		FileName:      t.FileName,
		FileSize:      t.Bytes,
		ResultKnown:   t.ResultKnown,
	}
	if t.Status == transfer.StatusFailed {
		msg.Code = "FILE_TRANSFER_FAILED"
		msg.Message = "file download failed"
	}
	return msg
}
