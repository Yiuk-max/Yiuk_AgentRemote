package server

import (
	"strings"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/task"
	"yiuk-switchboard/internal/platform/transfer"
)

// 申请会话
func (s *Server) handleRequestSession(c *conn, m protocol.Message) error {
	if err := s.validID("device_id", m.DeviceID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if c.role == roleDevice && c.deviceID == m.DeviceID {
		return perr("SELF_SESSION", "a device cannot request a session to itself")
	}
	if strings.TrimSpace(m.Purpose) == "" || len(m.Purpose) > s.cfg.PurposeMaxLen {
		return perr("INVALID_ARGUMENT", "purpose is required and bounded")
	}
	if len(m.Permissions) == 0 {
		return perr("INVALID_ARGUMENT", "at least one permission is required")
	}
	if m.TemporaryPassword == "" {
		return perr("TEMPORARY_PASSWORD_REQUIRED", "a device temporary password is required")
	}

	info, err := s.devices.VerifyPassword(m.DeviceID, m.TemporaryPassword)
	if err != nil {
		s.audit.Warn("temporary_password_rejected", "device", audit.RedactID(m.DeviceID), "code", errorCode(err))
		return s.deviceError(err)
	}
	onlineID, ok := s.devices.OnlineConnection(info.ID)
	if !ok || s.lookupConn(onlineID) == nil {
		return perr("DEVICE_OFFLINE", "device is not online")
	}

	ses, err := s.sessions.Create(newID(), session.CreateRequest{
		DeviceID:             info.ID,
		OperatorConnectionID: c.id,
		DeviceConnectionID:   onlineID,
		OperatorLabel:        s.actorLabel(c),
		OperatorSource:       c.remoteString(),
		Purpose:              m.Purpose,
		Permissions:          m.Permissions,
		Mode:                 info.Mode,
	})
	if err != nil {
		return s.sessionError(err)
	}
	s.devices.AddSession(info.ID, ses.ID)
	si := sessionInfo(ses)
	s.audit.Event("session_created", "session", audit.RedactID(ses.ID), "device", audit.RedactID(ses.DeviceID), "mode", string(ses.Mode), "status", string(ses.Status))

	// The device must always learn that a session exists, even in direct mode,
	// so its owner UI can show and stop it.
	if ses.Status == session.StatusPendingApproval {
		s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeSessionRequest,
			SessionID: ses.ID,
			DeviceID:  ses.DeviceID,
			Status:    string(ses.Status),
			Session:   &si,
		})
	} else {
		s.notifySession(ses)
	}
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeSessionCreated,
		SessionID: ses.ID,
		DeviceID:  ses.DeviceID,
		Status:    string(ses.Status),
		Session:   &si,
	})
}

// 批准会话
func (s *Server) handleApproveSession(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can approve a session")
	}
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(m.SessionID); ok {
		if existing.DeviceID != c.deviceID || existing.DeviceConnectionID != c.id {
			return perr("SESSION_NOT_FOUND", "session not found")
		}
	}
	ses, err := s.sessions.Approve(m.SessionID, c.id)
	if err != nil {
		return s.sessionError(err)
	}
	s.notifySession(ses)
	s.audit.Event("session_approved", "session", audit.RedactID(ses.ID), "device", audit.RedactID(ses.DeviceID))
	si := sessionInfo(ses)
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: ses.ID,
		DeviceID:  ses.DeviceID,
		Status:    string(ses.Status),
		Session:   &si,
	})
}

// 拒绝会话
func (s *Server) handleRejectSession(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can reject a session")
	}
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(m.SessionID); ok {
		if existing.DeviceID != c.deviceID || existing.DeviceConnectionID != c.id {
			return perr("SESSION_NOT_FOUND", "session not found")
		}
	}
	reason := m.Message
	if reason == "" {
		reason = "rejected_by_owner"
	}
	if len(reason) > s.cfg.CloseReasonMaxLen {
		reason = reason[:s.cfg.CloseReasonMaxLen]
	}
	ses, err := s.sessions.Reject(m.SessionID, c.id, reason)
	if err != nil {
		return s.sessionError(err)
	}
	s.devices.RemoveSession(ses.DeviceID, ses.ID)
	s.notifySession(ses)
	s.audit.Event("session_rejected", "session", audit.RedactID(ses.ID), "device", audit.RedactID(ses.DeviceID))
	si := sessionInfo(ses)
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: ses.ID,
		DeviceID:  ses.DeviceID,
		Status:    string(ses.Status),
		Session:   &si,
	})
}

// 获取会话状态
func (s *Server) handleGetSessionStatus(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	si := sessionInfo(ses)
	return s.sendOK(c, m, protocol.Message{
		Type:           protocol.TypeSessionStatus,
		SessionID:      ses.ID,
		DeviceID:       ses.DeviceID,
		Status:         string(ses.Status),
		Session:        &si,
		ActiveSessions: s.sessions.CountActiveByDevice(ses.DeviceID),
	})
}

// 打开会话通道
func (s *Server) handleOpenSessionChannel(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	if c.id == ses.OperatorConnectionID {
		var err error
		ses, err = s.sessions.Activate(ses.ID, c.id)
		if err != nil {
			return s.sessionError(err)
		}
		s.notifySession(ses)
	}
	si := sessionInfo(ses)
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: ses.ID,
		DeviceID:  ses.DeviceID,
		Status:    string(ses.Status),
		Session:   &si,
	})
}

// 关闭会话
func (s *Server) handleCloseSession(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("SESSION_NOT_FOUND", "session not found")
	}
	reason := "closed_by_operator"
	if c.role == roleDevice {
		reason = "closed_by_device"
	}
	closed, err := s.sessions.Close(ses.ID, reason)
	if err != nil {
		return s.sessionError(err)
	}
	s.devices.RemoveSession(closed.DeviceID, closed.ID)
	finished := s.tasks.FinishSession(closed.ID, task.StatusCancelled)
	cancelledTransfers := s.transfers.FinishSession(closed.ID, transfer.StatusCancelled)
	// Ask the device to stop local processes when the operator closed the
	// session, and tell the operator when the device closed it.
	if c.role != roleDevice {
		s.sendToConnection(closed.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCloseSession,
			SessionID: closed.ID,
			DeviceID:  closed.DeviceID,
			Code:      "SESSION_CLOSED",
			Message:   reason,
		})
	}
	s.notifySession(closed)
	for _, t := range finished {
		s.sendToConnection(closed.OperatorConnectionID, taskResultMessage(t))
	}
	for _, t := range cancelledTransfers {
		s.sendToConnection(closed.OperatorConnectionID, transferResultMessage(t))
	}
	s.audit.Event("session_closed", "session", audit.RedactID(closed.ID), "device", audit.RedactID(closed.DeviceID), "reason", reason)
	si := sessionInfo(closed)
	return s.sendOK(c, m, protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: closed.ID,
		DeviceID:  closed.DeviceID,
		Status:    string(closed.Status),
		Session:   &si,
	})
}

// 撤销设备的所有会话
func (s *Server) revokeDeviceSessions(deviceID, reason string) {
	interrupted := s.sessions.RevokeByDevice(deviceID, reason)
	for _, ses := range interrupted {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		finished := s.tasks.FinishSession(ses.ID, task.StatusResultUnknown)
		s.notifySession(ses)
		for _, t := range finished {
			s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		}
		for _, t := range s.transfers.FinishSession(ses.ID, transfer.StatusResultUnknown) {
			s.sendToConnection(ses.OperatorConnectionID, transferResultMessage(t))
		}
	}
}

// 判断是否能直接访问会话
func (s *Server) canAccessSession(c *conn, ses *session.Session) bool {
	if ses == nil {
		return false
	}
	if c.id == ses.OperatorConnectionID {
		return true
	}
	return c.role == roleDevice && c.deviceID == ses.DeviceID && c.id == ses.DeviceConnectionID
}

// 获取连接的标签
func (s *Server) actorLabel(c *conn) string {
	if c.label != "" {
		return c.label
	}
	return audit.RedactID(c.id)
}
