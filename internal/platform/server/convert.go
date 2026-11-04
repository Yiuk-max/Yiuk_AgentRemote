package server

//转换
//基本信息转换、时间戳、设备信息、会话信息
import (
	"time"

	"agentremote/internal/platform/device"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
)

func protocolDeviceInfo(info device.Info) protocol.DeviceInfo {
	return protocol.DeviceInfo{
		DeviceID:          info.ID,
		Name:              info.Name,
		Platform:          info.Platform,
		Mode:              string(info.Mode),
		Online:            info.Online,
		PasswordVersion:   info.PasswordVersion,
		PasswordExpiresAt: unixTimestampOrZero(info.PasswordExpiresAt),
		LastHeartbeat:     unixTimestampOrZero(info.LastHeartbeat),
		ActiveSessions:    info.ActiveSessions,
	}
}

func protocolSessionInfo(sessionRecord *session.Session) protocol.SessionInfo {
	info := protocol.SessionInfo{
		SessionID:      sessionRecord.ID,
		DeviceID:       sessionRecord.DeviceID,
		Operator:       sessionRecord.OperatorLabel,
		OperatorDevice: sessionRecord.OperatorSource,
		Purpose:        sessionRecord.Purpose,
		Permissions:    sessionRecord.PermissionList(),
		Mode:           string(sessionRecord.Mode),
		Status:         string(sessionRecord.Status),
		CreatedAt:      unixTimestampOrZero(sessionRecord.CreatedAt),
		ExpiresAt:      unixTimestampOrZero(sessionRecord.ExpiresAt),
		ApprovedAt:     unixTimestampOrZero(sessionRecord.ApprovedAt),
		ClosedAt:       unixTimestampOrZero(sessionRecord.ClosedAt),
		CloseReason:    sessionRecord.CloseReason,
	}
	return info
}

func unixTimestampOrZero(timestamp time.Time) int64 {
	if timestamp.IsZero() {
		return 0
	}
	return timestamp.Unix()
}
