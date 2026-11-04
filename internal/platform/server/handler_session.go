package server

import (
	"strings"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
	"agentremote/internal/platform/transfer"
)

// 申请会话
func (s *Server) handleRequestSession(clientConn *clientConnection, message protocol.Message) error {
	if err := s.validateID("device_id", message.DeviceID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if clientConn.role == roleDevice && clientConn.deviceID == message.DeviceID {
		return newProtocolError("SELF_SESSION", "a device cannot request a session to itself")
	}
	if strings.TrimSpace(message.Purpose) == "" || len(message.Purpose) > s.config.PurposeMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "purpose is required and bounded")
	}
	if len(message.Permissions) == 0 {
		return newProtocolError("INVALID_ARGUMENT", "at least one permission is required")
	}
	if message.TemporaryPassword == "" {
		return newProtocolError("TEMPORARY_PASSWORD_REQUIRED", "a device temporary password is required")
	}

	info, err := s.devices.VerifyPassword(message.DeviceID, message.TemporaryPassword)
	if err != nil {
		s.audit.Warn("temporary_password_rejected", "device", audit.RedactID(message.DeviceID), "code", auditErrorCode(err))
		return s.deviceProtocolError(err)
	}
	deviceConnectionID, ok := s.devices.OnlineConnection(info.ID)
	if !ok || s.connectionByID(deviceConnectionID) == nil {
		return newProtocolError("DEVICE_OFFLINE", "device is not online")
	}

	sessionRecord, err := s.sessions.Create(newID(), session.CreateRequest{
		DeviceID:             info.ID,
		OperatorConnectionID: clientConn.id,
		DeviceConnectionID:   deviceConnectionID,
		OperatorLabel:        s.operatorDisplayName(clientConn),
		OperatorSource:       clientConn.remoteAddress(),
		Purpose:              message.Purpose,
		Permissions:          message.Permissions,
		Mode:                 info.Mode,
	})
	if err != nil {
		return s.sessionProtocolError(err)
	}
	s.devices.AddSession(info.ID, sessionRecord.ID)
	sessionInfo := protocolSessionInfo(sessionRecord)
	s.audit.Event("session_created", "session", audit.RedactID(sessionRecord.ID), "device", audit.RedactID(sessionRecord.DeviceID), "mode", string(sessionRecord.Mode), "status", string(sessionRecord.Status))

	// The device must always learn that a session exists, even in direct mode,
	// so its owner UI can show and stop it.
	if sessionRecord.Status == session.StatusPendingApproval {
		s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeSessionRequest,
			SessionID: sessionRecord.ID,
			DeviceID:  sessionRecord.DeviceID,
			Status:    string(sessionRecord.Status),
			Session:   &sessionInfo,
		})
	} else {
		s.notifySessionStatus(sessionRecord)
	}
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:      protocol.TypeSessionCreated,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 批准会话
func (s *Server) handleApproveSession(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can approve a session")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(message.SessionID); ok {
		if existing.DeviceID != clientConn.deviceID || existing.DeviceConnectionID != clientConn.id {
			return newProtocolError("SESSION_NOT_FOUND", "session not found")
		}
	}
	sessionRecord, err := s.sessions.Approve(message.SessionID, clientConn.id)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	s.notifySessionStatus(sessionRecord)
	s.audit.Event("session_approved", "session", audit.RedactID(sessionRecord.ID), "device", audit.RedactID(sessionRecord.DeviceID))
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 拒绝会话
func (s *Server) handleRejectSession(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can reject a session")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(message.SessionID); ok {
		if existing.DeviceID != clientConn.deviceID || existing.DeviceConnectionID != clientConn.id {
			return newProtocolError("SESSION_NOT_FOUND", "session not found")
		}
	}
	reason := message.Message
	if reason == "" {
		reason = "rejected_by_owner"
	}
	if len(reason) > s.config.CloseReasonMaxLen {
		reason = reason[:s.config.CloseReasonMaxLen]
	}
	sessionRecord, err := s.sessions.Reject(message.SessionID, clientConn.id, reason)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
	s.notifySessionStatus(sessionRecord)
	s.audit.Event("session_rejected", "session", audit.RedactID(sessionRecord.ID), "device", audit.RedactID(sessionRecord.DeviceID))
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 获取会话状态
func (s *Server) handleGetSessionStatus(clientConn *clientConnection, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(clientConn, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:           protocol.TypeSessionStatus,
		SessionID:      sessionRecord.ID,
		DeviceID:       sessionRecord.DeviceID,
		Status:         string(sessionRecord.Status),
		Session:        &sessionInfo,
		ActiveSessions: s.sessions.CountOpenByDevice(sessionRecord.DeviceID),
	})
}

// 打开会话通道
func (s *Server) handleOpenSessionChannel(clientConn *clientConnection, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(clientConn, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	if clientConn.id == sessionRecord.OperatorConnectionID {
		var err error
		sessionRecord, err = s.sessions.Activate(sessionRecord.ID, clientConn.id)
		if err != nil {
			return s.sessionProtocolError(err)
		}
		s.notifySessionStatus(sessionRecord)
	}
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 关闭会话
func (s *Server) handleCloseSession(clientConn *clientConnection, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(clientConn, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	reason := "closed_by_operator"
	if clientConn.role == roleDevice {
		reason = "closed_by_device"
	}
	closedSession, err := s.sessions.Close(sessionRecord.ID, reason)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	s.devices.RemoveSession(closedSession.DeviceID, closedSession.ID)
	finishedTasks := s.tasks.FinishSession(closedSession.ID, task.StatusCancelled)
	cancelledTransfers := s.finishSessionTransfers(closedSession.ID, transfer.StatusCancelled)
	// Ask the device to stop local processes when the operator closed the
	// session, and tell the operator when the device closed it.
	if clientConn.role != roleDevice {
		s.sendToConnection(closedSession.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCloseSession,
			SessionID: closedSession.ID,
			DeviceID:  closedSession.DeviceID,
			Code:      "SESSION_CLOSED",
			Message:   reason,
		})
	}
	s.notifySessionStatus(closedSession)
	for _, commandTask := range finishedTasks {
		s.sendToConnection(closedSession.OperatorConnectionID, commandResultMessage(commandTask))
	}
	for _, resultMessage := range cancelledTransfers {
		s.sendToConnection(closedSession.OperatorConnectionID, resultMessage)
	}
	s.audit.Event("session_closed", "session", audit.RedactID(closedSession.ID), "device", audit.RedactID(closedSession.DeviceID), "reason", reason)
	sessionInfo := protocolSessionInfo(closedSession)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: closedSession.ID,
		DeviceID:  closedSession.DeviceID,
		Status:    string(closedSession.Status),
		Session:   &sessionInfo,
	})
}

// 撤销设备的所有会话
func (s *Server) revokeDeviceSessions(deviceID, reason string) {
	interruptedSessions := s.sessions.RevokeByDevice(deviceID, reason)
	for _, sessionRecord := range interruptedSessions {
		s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
		finishedTasks := s.tasks.FinishSession(sessionRecord.ID, task.StatusResultUnknown)
		s.notifySessionStatus(sessionRecord)
		for _, commandTask := range finishedTasks {
			s.sendToConnection(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
		}
		for _, resultMessage := range s.finishSessionTransfers(sessionRecord.ID, transfer.StatusResultUnknown) {
			s.sendToConnection(sessionRecord.OperatorConnectionID, resultMessage)
		}
	}
}

// 判断是否能直接访问会话
func (s *Server) canAccessSession(clientConn *clientConnection, sessionRecord *session.Session) bool {
	if sessionRecord == nil {
		return false
	}
	if clientConn.id == sessionRecord.OperatorConnectionID {
		return true
	}
	return clientConn.role == roleDevice && clientConn.deviceID == sessionRecord.DeviceID && clientConn.id == sessionRecord.DeviceConnectionID
}

// 获取连接的标签
func (s *Server) operatorDisplayName(clientConn *clientConnection) string {
	if clientConn.displayName != "" {
		return clientConn.displayName
	}
	return audit.RedactID(clientConn.id)
}
