package server

import (
	"errors"
	"sort"
	"strings"
	"time"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/auth"
	"yiuk-switchboard/internal/platform/device"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/task"
	"yiuk-switchboard/internal/platform/transfer"
)

const (
	maxTemporaryPasswordLen = 256
	downloadTransferTimeout = 5 * time.Minute
)

func (s *Server) handle(c *conn, m protocol.Message) error {
	if m.Version != protocol.Version {
		return ferr("BAD_VERSION", "unsupported protocol version")
	}
	switch m.Type {
	case protocol.TypeAuthenticatePlayer:
		return s.handleAuthenticate(c, m)
	case protocol.TypePing:
		if c.role == roleAnonymous {
			return ferr("AUTH_REQUIRED", "authenticate before sending other messages")
		}
		return s.sendOK(c, m, protocol.Message{Type: protocol.TypePong, Sequence: m.Sequence, ServerTime: time.Now().Unix()})
	case protocol.TypePong:
		return nil
	}
	if c.role == roleAnonymous {
		return ferr("AUTH_REQUIRED", "authenticate before sending other messages")
	}
	switch m.Type {
	case protocol.TypeRegisterDevice:
		return s.handleRegisterDevice(c, m)
	case protocol.TypeDeviceHeartbeat:
		return s.handleHeartbeat(c, m)
	case protocol.TypeUpdateTemporaryPassword:
		return s.handleUpdatePassword(c, m)
	case protocol.TypeRequestSession:
		return s.handleRequestSession(c, m)
	case protocol.TypeApproveSession:
		return s.handleApproveSession(c, m)
	case protocol.TypeRejectSession:
		return s.handleRejectSession(c, m)
	case protocol.TypeGetCommandResult:
		return s.handleGetCommandResult(c, m)
	case protocol.TypeDownloadFile:
		return s.handleDownloadFile(c, m)
	case protocol.TypeFileChunk:
		return s.handleFileChunk(c, m)
	case protocol.TypeFileDownloadResult:
		return s.handleFileDownloadResult(c, m)
	case protocol.TypeSessionStatus:
		return s.handleGetSessionStatus(c, m)
	case protocol.TypeOpenSessionChannel:
		return s.handleOpenSessionChannel(c, m)
	case protocol.TypeExecuteCommand:
		return s.handleExecuteCommand(c, m)
	case protocol.TypeCommandOutput:
		return s.handleCommandOutput(c, m)
	case protocol.TypeCommandResult:
		return s.handleCommandResult(c, m)
	case protocol.TypeCancelCommand:
		return s.handleCancelCommand(c, m)
	case protocol.TypeCloseSession:
		return s.handleCloseSession(c, m)
	case protocol.TypeListDevices:
		return s.handleListDevices(c, m)
	default:
		return perr("UNKNOWN_MESSAGE", "unsupported message type")
	}
}

func (s *Server) handleAuthenticate(c *conn, m protocol.Message) error {
	if c.role != roleAnonymous {
		return ferr("ALREADY_AUTHENTICATED", "connection is already authenticated")
	}
	business := m.BusinessType
	if business == "" {
		business = protocol.BusinessAgentAssist
	}
	if business != protocol.BusinessAgentAssist {
		return ferr("BUSINESS_TYPE_NOT_SUPPORTED", "platform listener only accepts agent_assist traffic")
	}
	if m.IdentityType != "player" {
		return ferr("BAD_IDENTITY", "identity_type must be player")
	}
	if err := s.auth.Authenticate(c.remote, m.PlayerPassword); err != nil {
		s.audit.Warn("player_auth_failed", "remote", c.remoteString(), "code", errorCode(err))
		if errors.Is(err, auth.ErrRateLimited) {
			return ferr("RATE_LIMITED", "too many authentication attempts")
		}
		return ferr("BAD_PASSWORD", "player password is incorrect")
	}
	c.role = rolePlayer
	c.business = business
	s.audit.Event("player_authenticated", "connection", audit.RedactID(c.id), "remote", c.remoteString())
	return s.sendOK(c, m, protocol.Message{
		Type:         protocol.TypeAuthenticated,
		ConnectionID: c.id,
		BusinessType: business,
		ServerTime:   time.Now().Unix(),
	})
}

func (s *Server) handleRegisterDevice(c *conn, m protocol.Message) error {
	if c.role != rolePlayer {
		if c.role == roleDevice {
			return perr("ALREADY_REGISTERED", "connection already owns a device")
		}
		return ferr("AUTH_REQUIRED", "authenticate before registering a device")
	}
	if err := s.validID("device_id", m.DeviceID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if len(m.DeviceName) > s.cfg.IdentifierMaxLen {
		return perr("INVALID_ARGUMENT", "device_name is too long")
	}
	if len(m.Platform) > 32 {
		return perr("INVALID_ARGUMENT", "platform is too long")
	}
	mode := device.Mode(m.Mode)
	if m.Mode == "" {
		mode = device.ModeApproval
	}
	if mode != device.ModeDirect && mode != device.ModeApproval {
		return perr("INVALID_ARGUMENT", "mode must be direct or approval")
	}
	if m.TemporaryPassword != "" && len(m.TemporaryPassword) < 32 {
		return perr("WEAK_TEMPORARY_PASSWORD", "temporary password must be at least 32 characters")
	}
	if len(m.TemporaryPassword) > maxTemporaryPasswordLen {
		return perr("INVALID_ARGUMENT", "temporary password is too long")
	}

	// A stale online entry can survive a crash before the read loop notices
	// the disconnect. Clear it only when the owning connection is gone; the
	// device manager still requires the current temporary password below.
	if onlineID, ok := s.devices.OnlineConnection(m.DeviceID); ok && s.lookupConn(onlineID) == nil {
		s.devices.ForceOffline(m.DeviceID)
	}

	ttl := s.cfg.TempPasswordTTL()
	if m.ExpiresAt > 0 {
		requested := time.Until(time.Unix(m.ExpiresAt, 0))
		if requested < s.cfg.MinTempPasswordTTL() {
			requested = s.cfg.MinTempPasswordTTL()
		}
		if requested > s.cfg.MaxTempPasswordTTL() {
			requested = s.cfg.MaxTempPasswordTTL()
		}
		ttl = requested
	}

	info, generated, err := s.devices.Register(device.Registration{
		ID:              m.DeviceID,
		Name:            m.DeviceName,
		Platform:        m.Platform,
		TemporaryPass:   m.TemporaryPassword,
		PasswordVersion: m.PasswordVersion,
		Mode:            mode,
		TTL:             ttl,
	})
	if err != nil {
		s.audit.Warn("device_registration_failed", "device", audit.RedactID(m.DeviceID), "code", errorCode(err))
		return s.deviceError(err)
	}
	if _, err := s.devices.SetOnline(info.ID, c.id); err != nil {
		s.audit.Warn("device_registration_failed", "device", audit.RedactID(info.ID), "code", errorCode(err))
		return s.deviceError(err)
	}
	c.role = roleDevice
	c.deviceID = info.ID
	c.label = info.Name
	s.audit.Event("device_registered", "device", audit.RedactID(info.ID), "connection", audit.RedactID(c.id), "mode", string(info.Mode))

	dev := deviceInfo(info)
	return s.sendOK(c, m, protocol.Message{
		Type:              protocol.TypeDeviceRegistered,
		DeviceID:          info.ID,
		DeviceName:        info.Name,
		Mode:              string(info.Mode),
		PasswordVersion:   info.PasswordVersion,
		ExpiresAt:         info.PasswordExpiresAt.Unix(),
		TemporaryPassword: generated,
		Device:            &dev,
		ServerTime:        time.Now().Unix(),
	})
}

func (s *Server) handleHeartbeat(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only a registered device can send heartbeats")
	}
	info, err := s.devices.Heartbeat(c.deviceID, c.id)
	if err != nil {
		return s.deviceError(err)
	}
	return s.sendOK(c, m, protocol.Message{
		Type:           protocol.TypeHeartbeatAck,
		DeviceID:       info.ID,
		ActiveSessions: s.sessions.CountActiveByDevice(info.ID),
		ServerTime:     time.Now().Unix(),
	})
}

func (s *Server) handleUpdatePassword(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only a registered device can rotate its temporary password")
	}
	mode := device.Mode(m.Mode)
	if m.Mode == "" {
		mode = device.ModeApproval
	}
	if mode != device.ModeDirect && mode != device.ModeApproval {
		return perr("INVALID_ARGUMENT", "mode must be direct or approval")
	}
	if m.TemporaryPassword != "" && len(m.TemporaryPassword) < 32 {
		return perr("WEAK_TEMPORARY_PASSWORD", "temporary password must be at least 32 characters")
	}
	if len(m.TemporaryPassword) > maxTemporaryPasswordLen {
		return perr("INVALID_ARGUMENT", "temporary password is too long")
	}
	ttl := s.cfg.TempPasswordTTL()
	if m.ExpiresAt > 0 {
		requested := time.Until(time.Unix(m.ExpiresAt, 0))
		if requested < s.cfg.MinTempPasswordTTL() {
			requested = s.cfg.MinTempPasswordTTL()
		}
		if requested > s.cfg.MaxTempPasswordTTL() {
			requested = s.cfg.MaxTempPasswordTTL()
		}
		ttl = requested
	}
	info, generated, err := s.devices.UpdatePassword(c.deviceID, m.TemporaryPassword, m.PasswordVersion, mode, ttl)
	if err != nil {
		return s.deviceError(err)
	}
	s.revokeDeviceSessions(info.ID, "temporary_password_updated")
	s.audit.Event("temporary_password_updated", "device", audit.RedactID(info.ID), "version", info.PasswordVersion)

	dev := deviceInfo(info)
	return s.sendOK(c, m, protocol.Message{
		Type:              protocol.TypeTemporaryPasswordUpdated,
		DeviceID:          info.ID,
		Mode:              string(info.Mode),
		PasswordVersion:   info.PasswordVersion,
		ExpiresAt:         info.PasswordExpiresAt.Unix(),
		TemporaryPassword: generated,
		Device:            &dev,
		ServerTime:        time.Now().Unix(),
	})
}

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

func (s *Server) handleApproveSession(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can approve a session")
	}
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(m.SessionID); ok {
		if existing.DeviceID != c.deviceID || existing.DeviceConnectionID != c.id {
			return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleRejectSession(c *conn, m protocol.Message) error {
	if c.role != roleDevice {
		return perr("NOT_DEVICE", "only the target device can reject a session")
	}
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if existing, ok := s.sessions.Get(m.SessionID); ok {
		if existing.DeviceID != c.deviceID || existing.DeviceConnectionID != c.id {
			return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleGetSessionStatus(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleOpenSessionChannel(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("NOT_FOUND", "session not found")
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
		return perr("NOT_FOUND", "session not found")
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
		return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleGetCommandResult(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("NOT_FOUND", "session not found")
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
		return perr("NOT_FOUND", "session not found")
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
		return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleCancelCommand(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validID("request_id", m.RequestID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleCloseSession(c *conn, m protocol.Message) error {
	if err := s.validID("session_id", m.SessionID); err != nil {
		return perr("INVALID_ARGUMENT", err.Error())
	}
	ses, ok := s.sessions.Get(m.SessionID)
	if !ok || !s.canAccessSession(c, ses) {
		return perr("NOT_FOUND", "session not found")
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

func (s *Server) handleListDevices(c *conn, m protocol.Message) error {
	infos := s.devices.List()
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	devices := make([]protocol.DeviceInfo, 0, len(infos))
	for _, info := range infos {
		devices = append(devices, deviceInfo(info))
	}
	return s.sendOK(c, m, protocol.Message{Type: protocol.TypeDeviceList, Devices: devices})
}

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

func (s *Server) canAccessSession(c *conn, ses *session.Session) bool {
	if ses == nil {
		return false
	}
	if c.id == ses.OperatorConnectionID {
		return true
	}
	return c.role == roleDevice && c.deviceID == ses.DeviceID && c.id == ses.DeviceConnectionID
}

func (s *Server) actorLabel(c *conn) string {
	if c.label != "" {
		return c.label
	}
	return audit.RedactID(c.id)
}

func (s *Server) validID(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New(field + " is required")
	}
	if len(value) > s.cfg.IdentifierMaxLen {
		return errors.New(field + " is too long")
	}
	return nil
}

func (s *Server) deviceError(err error) error {
	switch {
	case errors.Is(err, device.ErrDeviceNotFound):
		return perr("DEVICE_NOT_FOUND", "device not found")
	case errors.Is(err, device.ErrDeviceLimit):
		return perr("DEVICE_LIMIT", "device limit reached")
	case errors.Is(err, device.ErrDeviceOnline):
		return perr("DEVICE_ALREADY_ONLINE", "device is already online")
	case errors.Is(err, device.ErrBadPassword):
		return perr("BAD_TEMPORARY_PASSWORD", "temporary password is incorrect")
	case errors.Is(err, device.ErrPasswordExpiry):
		return perr("TEMPORARY_PASSWORD_EXPIRED", "temporary password has expired")
	case errors.Is(err, device.ErrPasswordRevoke):
		return perr("TEMPORARY_PASSWORD_REVOKED", "temporary password has been revoked")
	case errors.Is(err, device.ErrStaleVersion):
		return perr("STALE_PASSWORD_VERSION", "password version is stale")
	case errors.Is(err, device.ErrInvalidMode):
		return perr("INVALID_ARGUMENT", "invalid authorization mode")
	default:
		return perr("INVALID_ARGUMENT", "device request failed")
	}
}

func (s *Server) sessionError(err error) error {
	switch {
	case errors.Is(err, session.ErrNotFound):
		return perr("SESSION_NOT_FOUND", "session not found")
	case errors.Is(err, session.ErrSessionLimit):
		return perr("SESSION_LIMIT", "session limit reached")
	case errors.Is(err, session.ErrNotOwner):
		return perr("PERMISSION_DENIED", "connection does not own the session")
	case errors.Is(err, session.ErrNoPermission):
		return perr("PERMISSION_DENIED", "session does not grant this permission")
	case errors.Is(err, session.ErrExpired):
		return perr("SESSION_EXPIRED", "session has expired")
	case errors.Is(err, session.ErrInvalidState):
		return perr("INVALID_SESSION_STATE", "session is not in a state that permits this operation")
	case errors.Is(err, session.ErrInvalidPurpose):
		return perr("INVALID_ARGUMENT", "purpose is required")
	default:
		return perr("INVALID_ARGUMENT", "session request failed")
	}
}

func (s *Server) transferError(err error) error {
	switch {
	case errors.Is(err, transfer.ErrNotFound):
		return perr("TRANSFER_NOT_FOUND", "file transfer not found")
	case errors.Is(err, transfer.ErrTransferLimit):
		return perr("TRANSFER_LIMIT", "transfer limit reached for session")
	case errors.Is(err, transfer.ErrRequestConflict):
		return perr("REQUEST_ID_CONFLICT", "request id is already used by another session")
	case errors.Is(err, transfer.ErrSequence):
		return perr("INVALID_CHUNK_SEQUENCE", "file chunk sequence is not contiguous")
	case errors.Is(err, transfer.ErrChunkTooLarge):
		return perr("CHUNK_TOO_LARGE", "file chunk exceeds the maximum chunk size")
	case errors.Is(err, transfer.ErrEmptyChunk):
		return perr("INVALID_ARGUMENT", "file chunk is empty")
	case errors.Is(err, transfer.ErrChunkLimit):
		return perr("CHUNK_LIMIT", "file transfer exceeds the maximum chunk count")
	case errors.Is(err, transfer.ErrInvalidBase64):
		return perr("INVALID_ARGUMENT", "file chunk is not valid base64")
	case errors.Is(err, transfer.ErrSizeLimit):
		return perr("FILE_TOO_LARGE", "file download exceeds the configured size limit")
	case errors.Is(err, transfer.ErrTerminal):
		return perr("TRANSFER_ALREADY_FINISHED", "file transfer is already finished")
	case errors.Is(err, transfer.ErrInvalidStatus):
		return perr("INVALID_ARGUMENT", "invalid transfer status")
	default:
		return perr("INVALID_ARGUMENT", "file transfer request failed")
	}
}

func terminalTransferStatus(status string) (transfer.Status, error) {
	switch transfer.Status(status) {
	case transfer.StatusSucceeded, transfer.StatusFailed, transfer.StatusCancelled, transfer.StatusResultUnknown:
		return transfer.Status(status), nil
	default:
		return "", errors.New("status must be a terminal transfer status")
	}
}

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

func (s *Server) taskError(err error) error {
	switch {
	case errors.Is(err, task.ErrNotFound):
		return perr("TASK_NOT_FOUND", "task not found")
	case errors.Is(err, task.ErrTaskLimit):
		return perr("TASK_LIMIT", "task limit reached for session")
	case errors.Is(err, task.ErrRequestConflict):
		return perr("REQUEST_ID_CONFLICT", "request id is already used by another session")
	case errors.Is(err, task.ErrTerminal):
		return perr("TASK_ALREADY_FINISHED", "task is already in a terminal state")
	case errors.Is(err, task.ErrInvalidStatus):
		return perr("INVALID_ARGUMENT", "invalid task status")
	default:
		return perr("INVALID_ARGUMENT", "task request failed")
	}
}

func terminalTaskStatus(status string) (task.Status, error) {
	switch task.Status(status) {
	case task.StatusSucceeded, task.StatusFailed, task.StatusTimedOut, task.StatusCancelled, task.StatusResultUnknown:
		return task.Status(status), nil
	default:
		return "", errors.New("status must be a terminal task status")
	}
}

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

func deviceInfo(info device.Info) protocol.DeviceInfo {
	return protocol.DeviceInfo{
		DeviceID:          info.ID,
		Name:              info.Name,
		Platform:          info.Platform,
		Mode:              string(info.Mode),
		Online:            info.Online,
		PasswordVersion:   info.PasswordVersion,
		PasswordExpiresAt: unixOrZero(info.PasswordExpiresAt),
		LastHeartbeat:     unixOrZero(info.LastHeartbeat),
		ActiveSessions:    info.ActiveSessions,
	}
}

func sessionInfo(ses *session.Session) protocol.SessionInfo {
	info := protocol.SessionInfo{
		SessionID:      ses.ID,
		DeviceID:       ses.DeviceID,
		Operator:       ses.OperatorLabel,
		OperatorDevice: ses.OperatorSource,
		Purpose:        ses.Purpose,
		Permissions:    ses.PermissionList(),
		Mode:           string(ses.Mode),
		Status:         string(ses.Status),
		CreatedAt:      unixOrZero(ses.CreatedAt),
		ExpiresAt:      unixOrZero(ses.ExpiresAt),
		ApprovedAt:     unixOrZero(ses.ApprovedAt),
		ClosedAt:       unixOrZero(ses.ClosedAt),
		CloseReason:    ses.CloseReason,
	}
	return info
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// errorCode maps internal errors to the stable protocol error code used in
// audit logs. Logs must never contain free-form internal error text.
func errorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, auth.ErrRateLimited):
		return "RATE_LIMITED"
	case errors.Is(err, auth.ErrBadPassword):
		return "BAD_PASSWORD"
	case errors.Is(err, device.ErrDeviceNotFound):
		return "DEVICE_NOT_FOUND"
	case errors.Is(err, device.ErrBadPassword):
		return "BAD_TEMPORARY_PASSWORD"
	case errors.Is(err, device.ErrPasswordExpiry):
		return "TEMPORARY_PASSWORD_EXPIRED"
	case errors.Is(err, device.ErrPasswordRevoke):
		return "TEMPORARY_PASSWORD_REVOKED"
	default:
		return "INTERNAL"
	}
}
