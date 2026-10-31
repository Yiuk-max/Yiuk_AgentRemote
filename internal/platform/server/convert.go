package server

//转换
//基本信息转换、时间戳、设备信息、会话信息
import (
	"time"

	"agentremote/internal/platform/device"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
)

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
