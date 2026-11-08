package core

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
func (s *Service) handleUploadFile(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if len(strings.TrimSpace(message.RemotePath)) > s.config.WorkingDirMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "remote_path is too long")
	}
	if len(message.FileName) > 255 || len(message.FileSHA256) > 128 {
		return newProtocolError("INVALID_ARGUMENT", "file metadata is too long")
	}
	if message.FileSize < 0 || message.FileSize > int64(s.config.MaxUploadBytes) {
		return newProtocolError("FILE_TOO_LARGE", "file upload exceeds the configured size limit")
	}
	sessionRecord, err := s.sessions.Authorize(message.SessionID, caller.ConnectionID, session.PermissionWriteFile)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	// 连接是否还活着不再在这里查：投递失败就是“设备不可用”，
	// 下面 send 失败的既有分支会把任务/传输收敛成终态。
	deviceConnectionID, ok := s.devices.OnlineConnection(sessionRecord.DeviceID)
	if !ok || deviceConnectionID != sessionRecord.DeviceConnectionID {
		return newProtocolError("DEVICE_OFFLINE", "device is no longer online")
	}
	maxBytes := int64(s.config.MaxUploadBytes)
	if message.FileSize > 0 && message.FileSize < maxBytes {
		maxBytes = message.FileSize
	}
	fileTransfer, created, err := s.uploads.Start(sessionRecord.ID, message.RequestID, message.CorrelationID, maxBytes, fileTransferTimeout)
	if err != nil {
		return s.transferProtocolError(err)
	}
	if !created {
		return newProtocolError("TRANSFER_IN_PROGRESS", "an upload with this request id is already in progress")
	}
	if _, err := s.sessions.Activate(sessionRecord.ID, caller.ConnectionID); err != nil {
		s.uploads.Finish(sessionRecord.ID, fileTransfer.RequestID, transfer.StatusFailed, "", 0, true)
		return s.sessionProtocolError(err)
	}
	if !s.send(sessionRecord.DeviceConnectionID, protocol.Message{
		Type:       protocol.TypeUploadFile,
		SessionID:  sessionRecord.ID,
		RequestID:  fileTransfer.RequestID,
		RemotePath: message.RemotePath,
		FileName:   message.FileName,
		FileSize:   message.FileSize,
		FileSHA256: message.FileSHA256,
		Overwrite:  message.Overwrite,
	}) {
		s.uploads.Finish(sessionRecord.ID, fileTransfer.RequestID, transfer.StatusResultUnknown, "", 0, false)
		return newProtocolError("DEVICE_UNAVAILABLE", "could not deliver file upload to device")
	}
	s.audit.Event("file_upload_submitted", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(fileTransfer.RequestID), "device", audit.RedactID(sessionRecord.DeviceID), "bytes", message.FileSize)
	return nil
}

// 处理文件上传分片（操作者 → 设备）
func (s *Service) handleFileUploadChunk(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.OperatorConnectionID != caller.ConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	fileTransfer, err := s.uploads.RecordChunk(sessionRecord.ID, message.RequestID, message.Sequence, message.Data)
	if err != nil {
		if errors.Is(err, transfer.ErrSizeLimit) || errors.Is(err, transfer.ErrChunkLimit) {
			s.send(sessionRecord.OperatorConnectionID, uploadResultMessage(fileTransfer))
			s.send(sessionRecord.DeviceConnectionID, protocol.Message{
				Type: protocol.TypeCancelFileUpload, SessionID: sessionRecord.ID, RequestID: message.RequestID,
			})
			s.audit.Warn("file_upload_limit_exceeded", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(message.RequestID))
		}
		return s.transferProtocolError(err)
	}
	s.send(sessionRecord.DeviceConnectionID, protocol.Message{
		Type:      protocol.TypeFileUploadChunk,
		SessionID: sessionRecord.ID,
		RequestID: message.RequestID,
		Sequence:  message.Sequence,
		Data:      message.Data,
		EOF:       message.EOF,
	})
	return nil
}

// 处理文件上传结果（设备 → 操作者）
func (s *Service) handleFileUploadResult(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.DeviceConnectionID != caller.ConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	status, err := parseTerminalTransferStatus(message.Status)
	if err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if len(message.RemotePath) > s.config.WorkingDirMaxLen || len(message.FileName) > 255 || len(message.FileSHA256) > 128 {
		return newProtocolError("INVALID_ARGUMENT", "file metadata is too long")
	}
	fileTransfer, err := s.uploads.Finish(sessionRecord.ID, message.RequestID, status, message.FileName, message.FileSize, message.ResultKnown)
	if err != nil {
		return s.transferProtocolError(err)
	}
	resultMessage := uploadResultMessage(fileTransfer)
	resultMessage.FileSHA256 = message.FileSHA256
	resultMessage.RemotePath = message.RemotePath
	s.send(sessionRecord.OperatorConnectionID, resultMessage)
	s.audit.Event("file_upload_finished", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(fileTransfer.RequestID), "status", string(fileTransfer.Status), "bytes", fileTransfer.TransferredBytes)
	return nil
}

// 处理上传取消（操作者主动取消）
func (s *Service) handleCancelFileUpload(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", message.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || sessionRecord.OperatorConnectionID != caller.ConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	fileTransfer, err := s.uploads.Finish(sessionRecord.ID, message.RequestID, transfer.StatusCancelled, "", 0, true)
	if err != nil {
		return s.transferProtocolError(err)
	}
	s.send(sessionRecord.DeviceConnectionID, protocol.Message{
		Type: protocol.TypeCancelFileUpload, SessionID: sessionRecord.ID, RequestID: message.RequestID,
	})
	s.audit.Event("file_upload_cancelled", "session", audit.RedactID(sessionRecord.ID), "request", audit.RedactID(fileTransfer.RequestID))
	return s.sendResponse(caller, message, uploadResultMessage(fileTransfer))
}

// uploadResultMessage 将上传传输记录转换为携带原请求关联 ID 的结果消息。
func uploadResultMessage(fileTransfer *transfer.Transfer) protocol.Message {
	if fileTransfer == nil {
		return protocol.Message{Type: protocol.TypeFileUploadResult}
	}
	return protocol.Message{
		Type:          protocol.TypeFileUploadResult,
		SessionID:     fileTransfer.SessionID,
		RequestID:     fileTransfer.RequestID,
		CorrelationID: fileTransfer.CorrelationID,
		Status:        string(fileTransfer.Status),
		FileName:      fileTransfer.FileName,
		FileSize:      fileTransfer.TransferredBytes,
		ResultKnown:   fileTransfer.ResultKnown,
	}
}

// finishSessionTransfers 结束会话中所有未完成的上传和下载，返回发给操作者的结果消息。
func (s *Service) finishSessionTransfers(sessionID string, status transfer.Status) []protocol.Message {
	resultMessages := make([]protocol.Message, 0, 2)
	for _, fileTransfer := range s.downloads.FinishSession(sessionID, status) {
		resultMessages = append(resultMessages, downloadResultMessage(fileTransfer))
	}
	for _, fileTransfer := range s.uploads.FinishSession(sessionID, status) {
		resultMessages = append(resultMessages, uploadResultMessage(fileTransfer))
	}
	return resultMessages
}
