// Package core 是平台的全部业务：认证、设备、会话、命令、文件传输与过期清理。
//
// 它刻意不持有任何 socket 和连接表：所有出站帧都交给 Link，
// 连接的身份由接入层（gateway）在每次处理时带进来。
// 状态仍然是纯内存的，重启即丢弃。
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	baseauth "agentremote/internal/auth"
	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/auth"
	"agentremote/internal/platform/config"
	"agentremote/internal/platform/device"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
	"agentremote/internal/platform/transfer"
)

// Service 管理设备、会话、命令与文件传输的业务状态。
type Service struct {
	config config.Config
	logger *slog.Logger
	codec  protocol.Codec // JSON 协议帧编解码器

	devices   *device.Manager
	sessions  *session.Manager
	tasks     *task.Manager
	downloads *transfer.Manager // 下载传输状态；不保存文件内容
	uploads   *transfer.Manager // 上传管理器
	auth      *auth.Authenticator
	// registrations 对设备注册做按 IP 限流。注册发生在认证之后，
	// auth 的限流覆盖不到；而“密码不匹配就新建条目”使得刷设备槽的成本很低。
	registrations *baseauth.Limiter
	audit         *audit.Logger // 用于记录事件和警告的审计日志记录器

	link Link // 出站帧的唯一出口，由接入层提供
}

// New 创建业务层。link 不能为空：core 自己不写 socket。
func New(serverConfig config.Config, logger *slog.Logger, link Link) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		config:        serverConfig,
		logger:        logger,
		codec:         protocol.NewCodec(serverConfig.MaxFrameBytes),
		devices:       device.NewManager(serverConfig.MaxDevices, serverConfig.TempPasswordTTL()),
		sessions:      session.NewManager(serverConfig.MaxSessions, serverConfig.MaxSessionsPerDevice, serverConfig.SessionTimeout(), serverConfig.ApprovalTimeout()),
		tasks:         task.NewManager(serverConfig.MaxOutputBytes, serverConfig.MaxTasksPerSession),
		downloads:     transfer.NewManager(serverConfig.MaxTasksPerSession),
		uploads:       transfer.NewManager(serverConfig.MaxTasksPerSession),
		auth:          auth.New(serverConfig.PlayerPassword, serverConfig.AuthRatePerMin),
		registrations: baseauth.NewLimiter(serverConfig.RegisterRatePerMin, time.Minute),
		audit:         audit.New(logger),
		link:          link,
	}
}

// Run 每秒钟收敛一次过期资源，直到上下文取消。
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.expireOperationsAndPruneDevices()
		}
	}
}

// Handle 处理接入层送来的一帧，返回 nil 表示连接可以继续使用。
//
// 直接响应与异步通知都通过 Link 投递，返回值只表达“这条连接该不该关”：
// 返回 *FatalError 时接入层先写出 Frame（如果有），再关闭连接。
func (s *Service) Handle(caller *Caller, frame []byte) error {
	if caller == nil {
		return &FatalError{Err: errors.New("caller is required")}
	}
	message, err := s.codec.Decode(frame)
	if err != nil {
		s.logger.Debug("platform message decode failed", "connection", audit.RedactID(caller.ConnectionID), "error", err)
		return &FatalError{Err: err}
	}
	if err := s.dispatch(caller, message); err != nil {
		var protocolErr *protocolError
		if errors.As(err, &protocolErr) {
			errorMessage := protocol.ErrorMessage(protocolErr.code, protocolErr.message)
			errorMessage.CorrelationID = message.CorrelationID
			if protocolErr.fatal {
				// 致命错误的响应不经过发送队列：先写出去，再关连接。
				return &FatalError{Frame: s.encode(errorMessage), Err: protocolErr}
			}
			s.send(caller.ConnectionID, errorMessage)
			return nil
		}
		s.logger.Error("platform request failed", "connection", audit.RedactID(caller.ConnectionID), "error", err)
		errorMessage := protocol.ErrorMessage("INTERNAL", "request failed")
		errorMessage.CorrelationID = message.CorrelationID
		return &FatalError{Frame: s.encode(errorMessage), Err: err}
	}
	return nil
}

// Detach 在某条连接断开后收敛它名下的设备与操作者会话。
func (s *Service) Detach(caller *Caller) {
	if caller == nil {
		return
	}
	if caller.Role == RoleAnonymous {
		// 认证前就断开的连接，通常是 TLS 握手或端口配错，不是密码问题。
		s.audit.Warn("connection_closed_unauthenticated", "remote", caller.RemoteAddr)
		return
	}
	if caller.Role == RoleDevice {
		s.interruptDeviceSessionsOnDisconnect(caller)
	}
	// 注册成设备的连接也可能同时当过操作者，两种角色的会话都要清理。
	s.interruptOperatorSessionsOnDisconnect(caller)
}

// interruptDeviceSessionsOnDisconnect 将设备标记为离线，中断其会话并上报未知结果。
func (s *Service) interruptDeviceSessionsOnDisconnect(caller *Caller) {
	if caller.DeviceID == "" {
		return
	}
	_, wasOnlineConnectionOwner := s.devices.SetOffline(caller.DeviceID, caller.ConnectionID)
	if !wasOnlineConnectionOwner {
		return
	}
	s.audit.Event("device_disconnected", "device", audit.RedactID(caller.DeviceID), "connection", audit.RedactID(caller.ConnectionID))
	interruptedSessions := s.sessions.InterruptByDevice(caller.DeviceID, "device_disconnected")
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

// interruptOperatorSessionsOnDisconnect 中断该操作者的会话，取消任务并通知设备停止执行。
func (s *Service) interruptOperatorSessionsOnDisconnect(caller *Caller) {
	interruptedSessions := s.sessions.InterruptByOperator(caller.ConnectionID, "operator_disconnected")
	for _, sessionRecord := range interruptedSessions {
		s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
		finishedTasks := s.tasks.FinishSession(sessionRecord.ID, task.StatusCancelled)
		for _, resultMessage := range s.finishSessionTransfers(sessionRecord.ID, transfer.StatusCancelled) {
			s.send(sessionRecord.OperatorConnectionID, resultMessage)
		}
		s.notifySessionStatus(sessionRecord)
		if sessionRecord.DeviceConnectionID != "" {
			s.send(sessionRecord.DeviceConnectionID, protocol.Message{
				Type:      protocol.TypeCloseSession,
				SessionID: sessionRecord.ID,
				DeviceID:  sessionRecord.DeviceID,
				Code:      "OPERATOR_DISCONNECTED",
				Message:   "operator disconnected",
			})
		}
		for _, commandTask := range finishedTasks {
			s.send(sessionRecord.OperatorConnectionID, commandResultMessage(commandTask))
		}
	}
}

func newID() string {
	var randomBytes [16]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		// crypto/rand failure is unrecoverable for identity generation.
		panic("platform: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(randomBytes[:])
}
