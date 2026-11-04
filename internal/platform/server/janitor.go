package server

// 定期收敛过期会话、命令任务和文件传输，并清理过期的离线设备。
import (
	"context"
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/task"
	"agentremote/internal/platform/transfer"
)

// runExpirationCleanup 每秒处理一次过期资源，直到上下文取消。
func (s *Server) runExpirationCleanup(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireOperationsAndPruneDevices()
		}
	}
}

// expireOperationsAndPruneDevices 结束过期操作并清理离线设备：
// 1. 会话过期处理：检查所有会话是否过期，如果过期，则从设备中移除该会话，并向设备发送关闭会话的消息，同时将任务状态标记为取消
// 2. 传输过期处理：检查所有文件传输任务是否过期，如果过期，则向操作端发送传输结果消息，并向设备发送取消文件下载的消息
// 3. 任务过期处理：检查所有命令执行任务是否过期，如果过期，则向操作端发送任务结果消息，并向设备发送取消命令的消息
// 4. 离线设备清理：清理长时间离线的设备，并记录清理的数量
func (s *Server) expireOperationsAndPruneDevices() {
	expiredSessions := s.sessions.Expire()
	for _, sessionRecord := range expiredSessions {
		s.devices.RemoveSession(sessionRecord.DeviceID, sessionRecord.ID)
		// Expiry is also a device-side control event. Stop remote processes
		// before finalizing the task records so every client gets a chance to
		// terminate work that is still running locally.
		s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCloseSession,
			SessionID: sessionRecord.ID,
			DeviceID:  sessionRecord.DeviceID,
			Code:      "SESSION_EXPIRED",
			Message:   "session expired",
		})
		finishedTasks := s.tasks.FinishSession(sessionRecord.ID, task.StatusCancelled)
		s.notifySessionStatus(sessionRecord)
		for _, expiredOperation := range finishedTasks {
			s.sendToConnection(sessionRecord.OperatorConnectionID, commandResultMessage(expiredOperation))
		}
		for _, resultMessage := range s.finishSessionTransfers(sessionRecord.ID, transfer.StatusCancelled) {
			s.sendToConnection(sessionRecord.OperatorConnectionID, resultMessage)
		}
	}
	for _, expiredOperation := range s.uploads.Expire() {
		sessionRecord, ok := s.sessions.Get(expiredOperation.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(sessionRecord.OperatorConnectionID, uploadResultMessage(expiredOperation))
		s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
			Type: protocol.TypeCancelFileUpload, SessionID: expiredOperation.SessionID, RequestID: expiredOperation.RequestID,
		})
		s.audit.Event("file_upload_timed_out", "session", audit.RedactID(expiredOperation.SessionID), "request", audit.RedactID(expiredOperation.RequestID))
	}
	for _, expiredOperation := range s.downloads.Expire() {
		sessionRecord, ok := s.sessions.Get(expiredOperation.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(sessionRecord.OperatorConnectionID, downloadResultMessage(expiredOperation))
		s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
			Type: protocol.TypeCancelFileDownload, SessionID: expiredOperation.SessionID, RequestID: expiredOperation.RequestID,
		})
		s.audit.Event("file_download_timed_out", "session", audit.RedactID(expiredOperation.SessionID), "request", audit.RedactID(expiredOperation.RequestID))
	}
	for _, expiredOperation := range s.tasks.Expire() {
		sessionRecord, ok := s.sessions.Get(expiredOperation.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(sessionRecord.OperatorConnectionID, commandResultMessage(expiredOperation))
		s.sendToConnection(sessionRecord.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCancelCommand,
			SessionID: expiredOperation.SessionID,
			RequestID: expiredOperation.RequestID,
		})
		s.audit.Event("command_timed_out", "session", audit.RedactID(expiredOperation.SessionID), "request", audit.RedactID(expiredOperation.RequestID))
	}
	if removed := s.devices.RemoveExpiredOfflineDevices(time.Duration(s.config.OfflineDeviceKeepSec) * time.Second); removed > 0 {
		s.audit.Event("offline_devices_cleaned", "count", removed)
	}
}
