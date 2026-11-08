package core

import (
	"strings"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/device"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
	"agentremote/internal/platform/transfer"
)

// 申请会话
func (s *Service) handleRequestSession(caller *Caller, message protocol.Message) error {
	if err := s.validateID("device_id", message.DeviceID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if caller.Role == RoleDevice && caller.DeviceID == message.DeviceID {
		return newProtocolError("SELF_SESSION", "a device cannot request a session to itself")
	}
	if strings.TrimSpace(message.Purpose) == "" || len(message.Purpose) > s.config.PurposeMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "purpose is required and bounded")
	}
	var err error
	info, ok := s.devices.Get(message.DeviceID)
	if !ok {
		return s.deviceProtocolError(device.ErrDeviceNotFound)
	}
	mode := device.ModeApproval
	if message.TemporaryPassword != "" {
		info, err = s.devices.VerifyPassword(message.DeviceID, message.TemporaryPassword)
		if err != nil {
			s.audit.Warn("temporary_password_rejected", "device", audit.RedactID(message.DeviceID), "code", auditErrorCode(err))
			return s.deviceProtocolError(err)
		}
		mode = device.ModeDirect
	}
	deviceConnectionID, ok := s.devices.OnlineConnection(info.ID)
	// 设备表里的在线连接可能是崩溃前残留的：直接问接入层这条连接还在不在。
	if !ok || !s.link.Alive(deviceConnectionID) {
		return newProtocolError("DEVICE_OFFLINE", "device is not online")
	}

	sessionRecord, err := s.sessions.Create(newID(), session.CreateRequest{
		DeviceID:             info.ID,
		OperatorConnectionID: caller.ConnectionID,
		DeviceConnectionID:   deviceConnectionID,
		OperatorLabel:        s.operatorDisplayName(caller),
		OperatorSource:       caller.RemoteAddr,
		Purpose:              message.Purpose,
		Permissions:          message.Permissions,
		// Possessing the current temporary password explicitly authorizes direct mode;
		// without it, create an approval request regardless of the target's configured mode.
		Mode: mode,
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
		s.send(sessionRecord.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeSessionRequest,
			SessionID: sessionRecord.ID,
			DeviceID:  sessionRecord.DeviceID,
			Status:    string(sessionRecord.Status),
			Session:   &sessionInfo,
		})
	} else {
		s.notifySessionStatus(sessionRecord)
	}
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeSessionCreated,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 批准会话
func (s *Service) handleApproveSession(caller *Caller, message protocol.Message) error {
	if caller.Role != RoleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can approve a session")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(message.SessionID); ok {
		if existing.DeviceID != caller.DeviceID || existing.DeviceConnectionID != caller.ConnectionID {
			return newProtocolError("SESSION_NOT_FOUND", "session not found")
		}
	}
	sessionRecord, err := s.sessions.Approve(message.SessionID, caller.ConnectionID)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	s.notifySessionStatus(sessionRecord)
	s.audit.Event("session_approved", "session", audit.RedactID(sessionRecord.ID), "device", audit.RedactID(sessionRecord.DeviceID))
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 拒绝会话
func (s *Service) handleRejectSession(caller *Caller, message protocol.Message) error {
	if caller.Role != RoleDevice {
		return newProtocolError("NOT_DEVICE", "only the target device can reject a session")
	}
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(message.SessionID); ok {
		if existing.DeviceID != caller.DeviceID || existing.DeviceConnectionID != caller.ConnectionID {
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
	sessionRecord, err := s.sessions.Reject(message.SessionID, caller.ConnectionID, reason)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
	s.notifySessionStatus(sessionRecord)
	s.audit.Event("session_rejected", "session", audit.RedactID(sessionRecord.ID), "device", audit.RedactID(sessionRecord.DeviceID))
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 获取会话状态
func (s *Service) handleGetSessionStatus(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(caller, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(caller, message, protocol.Message{
		Type:           protocol.TypeSessionStatus,
		SessionID:      sessionRecord.ID,
		DeviceID:       sessionRecord.DeviceID,
		Status:         string(sessionRecord.Status),
		Session:        &sessionInfo,
		ActiveSessions: s.sessions.CountOpenByDevice(sessionRecord.DeviceID),
	})
}

// 打开会话通道
func (s *Service) handleOpenSessionChannel(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(caller, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	if caller.ConnectionID == sessionRecord.OperatorConnectionID {
		var err error
		sessionRecord, err = s.sessions.Activate(sessionRecord.ID, caller.ConnectionID)
		if err != nil {
			return s.sessionProtocolError(err)
		}
		s.notifySessionStatus(sessionRecord)
	}
	sessionInfo := protocolSessionInfo(sessionRecord)
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &sessionInfo,
	})
}

// 关闭会话
func (s *Service) handleCloseSession(caller *Caller, message protocol.Message) error {
	if err := s.validateID("session_id", message.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	sessionRecord, ok := s.sessions.Get(message.SessionID)
	if !ok || !s.canAccessSession(caller, sessionRecord) {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	reason := "closed_by_operator"
	if caller.Role == RoleDevice {
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
	if caller.Role != RoleDevice {
		s.send(closedSession.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCloseSession,
			SessionID: closedSession.ID,
			DeviceID:  closedSession.DeviceID,
			Code:      "SESSION_CLOSED",
			Message:   reason,
		})
	}
	s.notifySessionStatus(closedSession)
	for _, commandTask := range finishedTasks {
		s.send(closedSession.OperatorConnectionID, commandResultMessage(commandTask))
	}
	for _, resultMessage := range cancelledTransfers {
		s.send(closedSession.OperatorConnectionID, resultMessage)
	}
	s.audit.Event("session_closed", "session", audit.RedactID(closedSession.ID), "device", audit.RedactID(closedSession.DeviceID), "reason", reason)
	sessionInfo := protocolSessionInfo(closedSession)
	return s.sendResponse(caller, message, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: closedSession.ID,
		DeviceID:  closedSession.DeviceID,
		Status:    string(closedSession.Status),
		Session:   &sessionInfo,
	})
}

// 撤销设备的所有会话
func (s *Service) revokeDeviceSessions(deviceID, reason string) {
	interruptedSessions := s.sessions.RevokeByDevice(deviceID, reason)
	for _, sessionRecord := range interruptedSessions {
		s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
		finishedTasks := s.tasks.FinishSession(sessionRecord.ID, task.StatusResultUnknown)
		s.notifySessionStatus(sessionRecord)
		for _, commandTask := range finishedTasks {
			s.send(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
		}
		for _, resultMessage := range s.finishSessionTransfers(sessionRecord.ID, transfer.StatusResultUnknown) {
			s.send(sessionRecord.OperatorConnectionID, resultMessage)
		}
	}
}

// 判断是否能直接访问会话
func (s *Service) canAccessSession(caller *Caller, sessionRecord *session.Session) bool {
	if sessionRecord == nil {
		return false
	}
	if caller.ConnectionID == sessionRecord.OperatorConnectionID {
		return true
	}
	return caller.Role == RoleDevice && caller.DeviceID == sessionRecord.DeviceID && caller.ConnectionID == sessionRecord.DeviceConnectionID
}

// 获取连接的标签
func (s *Service) operatorDisplayName(caller *Caller) string {
	if caller.DisplayName != "" {
		return caller.DisplayName
	}
	return audit.RedactID(caller.ConnectionID)
}
