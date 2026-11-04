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

func (s *Server) dispatchMessage(clientConn *clientConnection, message protocol.Message) error {
	if message.Version != protocol.Version { //网络协议版本比对
		return newFatalProtocolError("BAD_VERSION", "unsupported protocol version")
	}
	switch message.Type {
	case protocol.TypeAuthenticatePlayer: //连接服务器使用服务
		return s.handleAuthenticatePlayer(clientConn, message)
	case protocol.TypePing: //发心跳检测
		if clientConn.role == roleAnonymous {
			return newFatalProtocolError("AUTH_REQUIRED", "authenticate before sending other messages")
		}
		return s.sendResponse(clientConn, message, protocol.Message{Type: protocol.TypePong, Sequence: message.Sequence, ServerTime: time.Now().Unix()})
	case protocol.TypePong: //收到心跳检测，不用管
		return nil
	}
	if clientConn.role == roleAnonymous { //匿名状态不能使用，返回报错
		return newFatalProtocolError("AUTH_REQUIRED", "authenticate before sending other messages")
	}
	switch message.Type {
	case protocol.TypeRegisterDevice:
		return s.handleRegisterDevice(clientConn, message)
	case protocol.TypeDeviceHeartbeat:
		return s.handleDeviceHeartbeat(clientConn, message)
	case protocol.TypeUpdateTemporaryPassword:
		return s.handleUpdateTemporaryPassword(clientConn, message)
	case protocol.TypeRequestSession:
		return s.handleRequestSession(clientConn, message)
	case protocol.TypeApproveSession:
		return s.handleApproveSession(clientConn, message)
	case protocol.TypeRejectSession:
		return s.handleRejectSession(clientConn, message)
	case protocol.TypeGetCommandResult:
		return s.handleGetCommandResult(clientConn, message)
	case protocol.TypeDownloadFile:
		return s.handleDownloadFile(clientConn, message)
	case protocol.TypeFileChunk:
		return s.handleFileDownloadChunk(clientConn, message)
	case protocol.TypeFileDownloadResult:
		return s.handleFileDownloadResult(clientConn, message)
	case protocol.TypeUploadFile:
		return s.handleUploadFile(clientConn, message)
	case protocol.TypeFileUploadChunk:
		return s.handleFileUploadChunk(clientConn, message)
	case protocol.TypeFileUploadResult:
		return s.handleFileUploadResult(clientConn, message)
	case protocol.TypeCancelFileUpload:
		return s.handleCancelFileUpload(clientConn, message)
	case protocol.TypeSessionStatus:
		return s.handleGetSessionStatus(clientConn, message)
	case protocol.TypeOpenSessionChannel:
		return s.handleOpenSessionChannel(clientConn, message)
	case protocol.TypeExecuteCommand:
		return s.handleExecuteCommand(clientConn, message)
	case protocol.TypeCommandOutput:
		return s.handleCommandOutput(clientConn, message)
	case protocol.TypeCommandResult:
		return s.handleCommandResult(clientConn, message)
	case protocol.TypeCancelCommand:
		return s.handleCancelCommand(clientConn, message)
	case protocol.TypeCloseSession:
		return s.handleCloseSession(clientConn, message)
	case protocol.TypeListDevices:
		return s.handleListDevices(clientConn, message)
	default:
		return newProtocolError("UNKNOWN_MESSAGE", "unsupported message type")
	}
}

func (s *Server) handleAuthenticatePlayer(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleAnonymous {
		return newFatalProtocolError("ALREADY_AUTHENTICATED", "connection is already authenticated")
	}
	businessType := message.BusinessType
	if businessType == "" {
		businessType = protocol.BusinessAgentAssist
	}
	if businessType != protocol.BusinessAgentAssist {
		return newFatalProtocolError("BUSINESS_TYPE_NOT_SUPPORTED", "platform listener only accepts agent_assist traffic")
	}
	if message.IdentityType != "player" {
		return newFatalProtocolError("BAD_IDENTITY", "identity_type must be player")
	}
	if err := s.auth.Authenticate(clientConn.remoteAddr, message.PlayerPassword); err != nil {
		s.audit.Warn("player_auth_failed", "remote", clientConn.remoteAddress(), "code", auditErrorCode(err))
		if errors.Is(err, auth.ErrRateLimited) {
			return newFatalProtocolError("RATE_LIMITED", "too many authentication attempts")
		}
		return newFatalProtocolError("BAD_PASSWORD", "player password is incorrect")
	}
	clientConn.role = rolePlayer
	clientConn.businessType = businessType
	s.audit.Event("player_authenticated", "connection", audit.RedactID(clientConn.id), "remote", clientConn.remoteAddress())
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:         protocol.TypeAuthenticated,
		ConnectionID: clientConn.id,
		BusinessType: businessType,
		ServerTime:   time.Now().Unix(),
	})
}

// 处理注册设备
func (s *Server) handleRegisterDevice(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != rolePlayer {
		if clientConn.role == roleDevice {
			return newProtocolError("ALREADY_REGISTERED", "connection already owns a device")
		}
		return newFatalProtocolError("AUTH_REQUIRED", "authenticate before registering a device")
	}
	if err := s.validateID("device_id", message.DeviceID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if len(message.DeviceName) > s.config.IdentifierMaxLen {
		return newProtocolError("INVALID_ARGUMENT", "device_name is too long")
	}
	if len(message.Platform) > 32 {
		return newProtocolError("INVALID_ARGUMENT", "platform is too long")
	}
	mode := device.Mode(message.Mode)
	if message.Mode == "" {
		mode = device.ModeApproval
	}
	if mode != device.ModeDirect && mode != device.ModeApproval {
		return newProtocolError("INVALID_ARGUMENT", "mode must be direct or approval")
	}
	if message.TemporaryPassword != "" && len(message.TemporaryPassword) < 32 {
		return newProtocolError("WEAK_TEMPORARY_PASSWORD", "temporary password must be at least 32 characters")
	}
	if len(message.TemporaryPassword) > maxTemporaryPasswordLen {
		return newProtocolError("INVALID_ARGUMENT", "temporary password is too long")
	}

	// A stale online entry can survive a crash before the read loop notices
	// the disconnect. Clear it only when the owning connection is gone; the
	// device manager still requires the current temporary password below.
	if deviceConnectionID, ok := s.devices.OnlineConnection(message.DeviceID); ok && s.connectionByID(deviceConnectionID) == nil {
		s.devices.ForceOffline(message.DeviceID)
	}

	passwordTTL := s.config.TempPasswordTTL()
	if message.ExpiresAt > 0 {
		requestedPasswordTTL := time.Until(time.Unix(message.ExpiresAt, 0))
		if requestedPasswordTTL < s.config.MinTempPasswordTTL() {
			requestedPasswordTTL = s.config.MinTempPasswordTTL()
		}
		if requestedPasswordTTL > s.config.MaxTempPasswordTTL() {
			requestedPasswordTTL = s.config.MaxTempPasswordTTL()
		}
		passwordTTL = requestedPasswordTTL
	}

	info, generatedPassword, err := s.devices.Register(device.Registration{
		ID:              message.DeviceID,
		Name:            message.DeviceName,
		Platform:        message.Platform,
		TemporaryPass:   message.TemporaryPassword,
		PasswordVersion: message.PasswordVersion,
		Mode:            mode,
		TTL:             passwordTTL,
	})
	if err != nil {
		s.audit.Warn("device_registration_failed", "device", audit.RedactID(message.DeviceID), "code", auditErrorCode(err))
		return s.deviceProtocolError(err)
	}
	if _, err := s.devices.SetOnline(info.ID, clientConn.id); err != nil {
		s.audit.Warn("device_registration_failed", "device", audit.RedactID(info.ID), "code", auditErrorCode(err))
		return s.deviceProtocolError(err)
	}
	clientConn.role = roleDevice
	clientConn.deviceID = info.ID
	clientConn.displayName = info.Name
	s.audit.Event("device_registered", "device", audit.RedactID(info.ID), "connection", audit.RedactID(clientConn.id), "mode", string(info.Mode))

	deviceInfo := protocolDeviceInfo(info)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:              protocol.TypeDeviceRegistered,
		DeviceID:          info.ID,
		DeviceName:        info.Name,
		Mode:              string(info.Mode),
		PasswordVersion:   info.PasswordVersion,
		ExpiresAt:         info.PasswordExpiresAt.Unix(),
		TemporaryPassword: generatedPassword,
		Device:            &deviceInfo,
		ServerTime:        time.Now().Unix(),
	})
}

// 处理心跳包
func (s *Server) handleDeviceHeartbeat(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleDevice {
		return newProtocolError("NOT_DEVICE", "only a registered device can send heartbeats")
	}
	info, err := s.devices.Heartbeat(clientConn.deviceID, clientConn.id)
	if err != nil {
		return s.deviceProtocolError(err)
	}
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:           protocol.TypeHeartbeatAck,
		DeviceID:       info.ID,
		ActiveSessions: s.sessions.CountOpenByDevice(info.ID),
		ServerTime:     time.Now().Unix(),
	})
}

// 处理更新密码
func (s *Server) handleUpdateTemporaryPassword(clientConn *clientConnection, message protocol.Message) error {
	if clientConn.role != roleDevice {
		return newProtocolError("NOT_DEVICE", "only a registered device can rotate its temporary password")
	}
	mode := device.Mode(message.Mode)
	if message.Mode == "" {
		mode = device.ModeApproval
	}
	if mode != device.ModeDirect && mode != device.ModeApproval {
		return newProtocolError("INVALID_ARGUMENT", "mode must be direct or approval")
	}
	if message.TemporaryPassword != "" && len(message.TemporaryPassword) < 32 {
		return newProtocolError("WEAK_TEMPORARY_PASSWORD", "temporary password must be at least 32 characters")
	}
	if len(message.TemporaryPassword) > maxTemporaryPasswordLen {
		return newProtocolError("INVALID_ARGUMENT", "temporary password is too long")
	}
	passwordTTL := s.config.TempPasswordTTL()
	if message.ExpiresAt > 0 {
		requestedPasswordTTL := time.Until(time.Unix(message.ExpiresAt, 0))
		if requestedPasswordTTL < s.config.MinTempPasswordTTL() {
			requestedPasswordTTL = s.config.MinTempPasswordTTL()
		}
		if requestedPasswordTTL > s.config.MaxTempPasswordTTL() {
			requestedPasswordTTL = s.config.MaxTempPasswordTTL()
		}
		passwordTTL = requestedPasswordTTL
	}
	info, generatedPassword, err := s.devices.UpdatePassword(clientConn.deviceID, message.TemporaryPassword, message.PasswordVersion, mode, passwordTTL)
	if err != nil {
		return s.deviceProtocolError(err)
	}
	s.revokeDeviceSessions(info.ID, "temporary_password_updated")
	s.audit.Event("temporary_password_updated", "device", audit.RedactID(info.ID), "version", info.PasswordVersion)

	deviceInfo := protocolDeviceInfo(info)
	return s.sendResponse(clientConn, message, protocol.Message{
		Type:              protocol.TypeTemporaryPasswordUpdated,
		DeviceID:          info.ID,
		Mode:              string(info.Mode),
		PasswordVersion:   info.PasswordVersion,
		ExpiresAt:         info.PasswordExpiresAt.Unix(),
		TemporaryPassword: generatedPassword,
		Device:            &deviceInfo,
		ServerTime:        time.Now().Unix(),
	})
}

// 获取设备列表
func (s *Server) handleListDevices(clientConn *clientConnection, message protocol.Message) error {
	infos := s.devices.List()
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	devices := make([]protocol.DeviceInfo, 0, len(infos))
	for _, info := range infos {
		devices = append(devices, protocolDeviceInfo(info))
	}
	return s.sendResponse(clientConn, message, protocol.Message{Type: protocol.TypeDeviceList, Devices: devices})
}
