package server

import (
	"errors"
	"strings"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/transfer"
)

// 处理文件下载请求
func (s *Server) handleDownloadFile(clientConn *clientConnection, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if strings.TrimSpace(message.RemotePath) == "" || len(message.RemotePath) > s.config.WorkingDirMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "remote_path is required and bounded")
	}
	sessionRecord, err := s.sessions.Authorize(message.SessionID, clientConn.id, session.PermissionReadFile)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	deviceConnectionID, ok := s.devices.OnlineConnection(sessionRecord.DeviceID)
	if !ok || deviceConnectionID != sessionRecord.DeviceConnectionID || s.connectionByID(deviceConnectionID) == nil {
		return newProtocolError("DEVICE_OFFLINE", "device is no longer online")
	}
	fileTransfer, created, err := s.downloads.Start(sessionRecord.ID, message.RequestID, message.CorrelationID, int64(s.config.MaxDownloadBytes), fileTransferTimeout)
	if err != nil {
		return s.transferProtocolError(err)
	}
	if !created {
		return newProtocolError("TRANSFER_IN_PROGRESS", "a download with this request id is already in progress")
	}
	if _, err := s.sessions.Activate(sessionRecord.ID, clientConn.id); err != nil {
		s.downloads.Finish(sessionRecord.ID, fileTransfer.RequestID, transfer.StatusFailed, "", 0, true)
		return s.sessionProtocolError(err)
	}
	if !s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
		Type:       protocol.TypeDownloadFile,
		SessionID:  sessionRecord.ID,
		RequestID:  fileTransfer.RequestID,
		RemotePath: message.RemotePath,
	}) {
		s.downloads.Finish(sessionRecord.ID, fileTransfer.RequestID, transfer.StatusResultUnknown, "", 0, false)
		return newProtocolError("DEVICE_UNAVAILABLE", "could not deliver file request to device")
	}
	s.audit.Event("file_download_submitted", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(fileTransfer.RequestID), "device", audit.RedactID(sessionRecord.DeviceID))
	return nil
}

// 处理文件下载分片
func (s *Server) handleFileDownloadChunk(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can send file chunks")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.DeviceID != clientConn.deviceID || sessionRecord.DeviceConnectionID != clientConn.id {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	fileTransfer, err := s.downloads.RecordChunk(sessionRecord.ID, message.RequestID, message.Sequence, message.Data)
	if err != nil {
		if errors.Is(err, transfer.ErrSizeLimit) || errors.Is(err, transfer.ErrChunkLimit) {
			s.sendToConnection(sessionRecord.OperatorConnectionID, downloadResultMessage(fileTransfer))
			s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
				Type: protocol.TypeCancelFileDownload, SessionID: sessionRecord.ID, RequestID: message.RequestID,
			})
			s.audit.Warn("file_download_limit_exceeded", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(message.RequestID))
		}
		return s.transferProtocolError(err)
	}
	s.sendToConnection(sessionRecord.OperatorConnectionID, protocol.Message{
		Type:      protocol.TypeFileChunk,
		SessionID: sessionRecord.ID,
		RequestID: message.RequestID,
		Sequence:  message.Sequence,
		Data:      message.Data,
		EOF:       message.EOF,
	})
	return nil
}

// 处理文件下载结果
func (s *Server) handleFileDownloadResult(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can report a file download result")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.DeviceID != clientConn.deviceID || sessionRecord.DeviceConnectionID != clientConn.id {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	status, err := parseTerminalTransferStatus(message.Status)
	if err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if len(message.FileName) > 255 || len(message.FileSHA256) > 128 {
		return newProtocolError("INVALID_ARGUMENT", "file metadata is too long")
	}
	fileTransfer, err := s.downloads.Finish(sessionRecord.ID, message.RequestID, status, message.FileName, message.FileSize, message.ResultKnown)
	if err != nil {
		return s.transferProtocolError(err)
	}
	resultMessage := downloadResultMessage(fileTransfer)
	resultMessage.FileSHA256 = message.FileSHA256
	s.sendToConnection(sessionRecord.OperatorConnectionID, resultMessage)
	s.audit.Event("file_download_finished", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(fileTransfer.RequestID), "status", string(fileTransfer.Status), "bytes", fileTransfer.TransferredBytes)
	return nil
}

// downloadResultMessage 将下载传输记录转换为携带原请求关联 ID 的结果消息。
func downloadResultMessage(fileTransfer *transfer.Transfer) protocol.Message {
	if fileTransfer == nil {
		return protocol.Message{Type: protocol.TypeFileDownloadResult}
	}
	resultMessage := protocol.Message{
		Type:          protocol.TypeFileDownloadResult,
		SessionID:     fileTransfer.SessionID,
		RequestID:     fileTransfer.RequestID,
		CorrelationID: fileTransfer.CorrelationID,
		Status:        string(fileTransfer.Status),
		FileName:      fileTransfer.FileName,
		FileSize:      fileTransfer.TransferredBytes,
		ResultKnown:   fileTransfer.ResultKnown,
	}
	if fileTransfer.Status == transfer.StatusFailed {
		resultMessage.Code = "FILE_TRANSFER_FAILED"
		resultMessage.Message = "file download failed"
	}
	return resultMessage
}
