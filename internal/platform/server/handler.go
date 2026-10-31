package server

//handler负责消息分发，只负责处理重要的事务，其它分发给具体的handler去处理
import (
	"errors"
	"sort"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/auth"
	"agentremote/internal/platform/device"
	"agentremote/internal/platform/protocol"
)

const (
	maxTemporaryPasswordLen = 256
	fileTransferTimeout     = 5 * time.Minute
)

func (s *Server) handle(c *conn, m protocol.Message) error {
	if m.Version != protocol.Version { //网络协议版本比对
		return ferr("BAD_VERSION", "unsupported protocol version")
	}
	switch m.Type {
	case protocol.TypeAuthenticatePlayer: //连接服务器使用服务
		return s.handleAuthenticate(c, m)
	case protocol.TypePing: //发心跳检测
		if c.role == roleAnonymous {
			return ferr("AUTH_REQUIRED", "authenticate before sending other messages")
		}
		return s.sendOK(c, m, protocol.Message{Type: protocol.TypePong, Sequence: m.Sequence, ServerTime: time.Now().Unix()})
	case protocol.TypePong: //收到心跳检测，不用管
		return nil
	}
	if c.role == roleAnonymous { //匿名状态不能使用，返回报错
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
	case protocol.TypeUploadFile:
		return s.handleUploadFile(c, m)
	case protocol.TypeFileUploadChunk:
		return s.handleFileUploadChunk(c, m)
	case protocol.TypeFileUploadResult:
		return s.handleFileUploadResult(c, m)
	case protocol.TypeCancelFileUpload:
		return s.handleCancelFileUpload(c, m)
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

// 处理注册设备
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

// 处理心跳包
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

// 处理更新密码
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

// 获取设备列表
func (s *Server) handleListDevices(c *conn, m protocol.Message) error {
	infos := s.devices.List()
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	devices := make([]protocol.DeviceInfo, 0, len(infos))
	for _, info := range infos {
		devices = append(devices, deviceInfo(info))
	}
	return s.sendOK(c, m, protocol.Message{Type: protocol.TypeDeviceList, Devices: devices})
}
