package server

// 文件上传：操作者把本地文件推送到目标设备。平台只校验权限、分块序号与累计
// 字节数，不保存文件内容；分块按操作者 → 平台 → 设备的顺序转发。
import (
	"errors"
	"strings"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/transfer"
)

// 处理文件上传请求
func (s *Server) handleUploadFile(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if len(strings.TrimSpace(m.RemotePath)) > s.cfg.WorkingDirMaxLen {
		return perr("INVALID_ARGUMENT", "remote_path is too long")
	}
	if len(m.FileName) > 255 || len(m.FileSHA256) > 128 {
		return perr("INVALID_ARGUMENT", "file metadata is too long")
	}
	if m.FileSize < 0 || m.FileSize > int64(s.cfg.MaxUploadBytes) {
		return perr("FILE_TOO_LARGE", "file upload exceeds the configured size limit")
	}
	ses, err := s.sessions.Authorize(m.SessionID, c.id, session.PermissionWriteFile)
	if err != nil {
		return s.sessionError(err)
	}
	onlineID, ok := s.devices.OnlineConnection(ses.DeviceID)
	if !ok || onlineID != ses.DeviceConnectionID || s.lookupConn(onlineID) == nil {
		return perr("DEVICE_OFFLINE", "device is no longer online")
	}
	maxBytes := int64(s.cfg.MaxUploadBytes)
	if m.FileSize > 0 && m.FileSize < maxBytes {
		maxBytes = m.FileSize
	}
	t, created, err := s.uploads.Start(ses.ID, m.RequestID, m.CorrelationID, maxBytes, fileTransferTimeout)
	if err != nil {
		return s.transferError(err)
	}
	if !created {
		return perr("TRANSFER_IN_PROGRESS", "an upload with this request id is already in progress")
	}
	if _, err := s.sessions.Activate(ses.ID, c.id); err != nil {
		s.uploads.Finish(ses.ID, t.RequestID, transfer.StatusFailed, "", 0, true)
		return s.sessionError(err)
	}
	if !s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
		Type:       protocol.TypeUploadFile,
		SessionID:  ses.ID,
		RequestID:  t.RequestID,
		RemotePath: m.RemotePath,
		FileName:   m.FileName,
		FileSize:   m.FileSize,
		FileSHA256: m.FileSHA256,
		Overwrite:  m.Overwrite,
	}) {
		s.uploads.Finish(ses.ID, t.RequestID, transfer.StatusResultUnknown, "", 0, false)
		return perr("DEVICE_UNAVAILABLE", "could not deliver file upload to device")
	}
	s.audit.Event("file_upload_submitted", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID), "device", audit.RedactID(ses.DeviceID), "bytes", m.FileSize)
	return nil
}

// 处理文件上传分片（操作者 → 设备）
func (s *Server) handleFileUploadChunk(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || ses.OperatorConnectionID != c.id {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	t, err := s.uploads.Append(ses.ID, m.RequestID, m.Sequence, m.Data)
	if err != nil {
		if errors.Is(err, transfer.ErrSizeLimit) || errors.Is(err, transfer.ErrChunkLimit) {
			s.sendToConnection(ses.OperatorConnectionID, uploadResultMessage(t))
			s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
				Type: protocol.TypeCancelFileUpload, SessionID: ses.ID, RequestID: m.RequestID,
			})
			s.audit.Warn("file_upload_limit_exceeded", "session", audit.RedactID(ses.ID), "request", audit.RedactID(m.RequestID))
		}
		return s.transferError(err)
	}
	s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
		Type:      protocol.TypeFileUploadChunk,
		SessionID: ses.ID,
		RequestID: m.RequestID,
		Sequence:  m.Sequence,
		Data:      m.Data,
		EOF:       m.EOF,
	})
	return nil
}

// 处理文件上传结果（设备 → 操作者）
func (s *Server) handleFileUploadResult(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || ses.DeviceConnectionID != c.id {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	status, err := terminalTransferStatus(m.Status)
	if err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if len(m.RemotePath) > s.cfg.WorkingDirMaxLen || len(m.FileName) > 255 || len(m.FileSHA256) > 128 {
		return perr("INVALID_ARGUMENT", "file metadata is too long")
	}
	t, err := s.uploads.Finish(ses.ID, m.RequestID, status, m.FileName, m.FileSize, m.ResultKnown)
	if err != nil {
		return s.transferError(err)
	}
	msg := uploadResultMessage(t)
	msg.FileSHA256 = m.FileSHA256
	msg.RemotePath = m.RemotePath
	s.sendToConnection(ses.OperatorConnectionID, msg)
	s.audit.Event("file_upload_finished", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID), "status", string(t.Status), "bytes", t.Bytes)
	return nil
}

// 处理上传取消（操作者主动取消）
func (s *Server) handleCancelFileUpload(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || ses.OperatorConnectionID != c.id {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	t, err := s.uploads.Finish(ses.ID, m.RequestID, transfer.StatusCancelled, "", 0, true)
	if err != nil {
		return s.transferError(err)
	}
	s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
		Type: protocol.TypeCancelFileUpload, SessionID: ses.ID, RequestID: m.RequestID,
	})
	s.audit.Event("file_upload_cancelled", "session", audit.RedactID(ses.ID), "request", audit.RedactID(t.RequestID))
	return s.sendOK(c, m, uploadResultMessage(t))
}

// 上传终端状态
func uploadResultMessage(t *transfer.Transfer) protocol.Message {
	if t == nil {
		return protocol.Message{Type: protocol.TypeFileUploadResult}
	}
	return protocol.Message{
		Type:          protocol.TypeFileUploadResult,
		SessionID:     t.SessionID,
		RequestID:     t.RequestID,
		CorrelationID: t.Correlation,
		Status:        string(t.Status),
		FileName:      t.FileName,
		FileSize:      t.Bytes,
		ResultKnown:   t.ResultKnown,
	}
}

// sessionTransferResults terminates every live file transfer (download and
// upload) for a session and returns the terminal messages for the operator.
func (s *Server) sessionTransferResults(sessionID string, status transfer.Status) []protocol.Message {
	results := make([]protocol.Message, 0, 2)
	for _, t := range s.transfers.FinishSession(sessionID, status) {
		results = append(results, transferResultMessage(t))
	}
	for _, t := range s.uploads.FinishSession(sessionID, status) {
		results = append(results, uploadResultMessage(t))
	}
	return results
}
