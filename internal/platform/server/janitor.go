package server

//1 秒一次 sweep：会话过期、任务 deadline、传输 5 分钟超时、离线设备清理；均向设备下发取消
import (
	"context"
	"time"

	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/task"
	"yiuk-switchboard/internal/platform/transfer"
)

func (s *Server) janitor(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

// sweep是一个周期性清理函数，每秒执行一次，主要负责以下任务：
// 1. 会话过期处理：检查所有会话是否过期，如果过期，则从设备中移除该会话，并向设备发送关闭会话的消息，同时将任务状态标记为取消。
// 2. 传输过期处理：检查所有文件传输任务是否过期，如果过期，则向操作端发送传输结果消息，并向设备发送取消文件下载的消息。
// 3. 任务过期处理：检查所有命令执行任务是否过期，如果过期，则向操作端发送任务结果消息，并向设备发送取消命令的消息。
// 4. 离线设备清理：清理长时间离线的设备，并记录清理的数量。
func (s *Server) sweep() {
	expired := s.sessions.Expire()
	for _, ses := range expired {
		s.devices.RemoveSession(ses.DeviceID, ses.ID)
		// Expiry is also a device-side control event. Stop remote processes
		// before finalizing the task records so every client gets a chance to
		// terminate work that is still running locally.
		s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCloseSession,
			SessionID: ses.ID,
			DeviceID:  ses.DeviceID,
			Code:      "SESSION_EXPIRED",
			Message:   "session expired",
		})
		finished := s.tasks.FinishSession(ses.ID, task.StatusCancelled)
		s.notifySession(ses)
		for _, t := range finished {
			s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		}
		for _, msg := range s.sessionTransferResults(ses.ID, transfer.StatusCancelled) {
			s.sendToConnection(ses.OperatorConnectionID, msg)
		}
	}
	for _, t := range s.uploads.Expire() {
		ses, ok := s.sessions.Get(t.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(ses.OperatorConnectionID, uploadResultMessage(t))
		s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type: protocol.TypeCancelFileUpload, SessionID: t.SessionID, RequestID: t.RequestID,
		})
		s.audit.Event("file_upload_timed_out", "session", audit.RedactID(t.SessionID), "request", audit.RedactID(t.RequestID))
	}
	for _, t := range s.transfers.Expire() {
		ses, ok := s.sessions.Get(t.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(ses.OperatorConnectionID, transferResultMessage(t))
		s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type: protocol.TypeCancelFileDownload, SessionID: t.SessionID, RequestID: t.RequestID,
		})
		s.audit.Event("file_download_timed_out", "session", audit.RedactID(t.SessionID), "request", audit.RedactID(t.RequestID))
	}
	for _, t := range s.tasks.Expire() {
		ses, ok := s.sessions.Get(t.SessionID)
		if !ok {
			continue
		}
		s.sendToConnection(ses.OperatorConnectionID, taskResultMessage(t))
		s.sendToConnection(ses.DeviceConnectionID, protocol.Message{
			Type:      protocol.TypeCancelCommand,
			SessionID: t.SessionID,
			RequestID: t.RequestID,
		})
		s.audit.Event("command_timed_out", "session", audit.RedactID(t.SessionID), "request", audit.RedactID(t.RequestID))
	}
	if removed := s.devices.Cleanup(time.Duration(s.cfg.OfflineDeviceKeepSec) * time.Second); removed > 0 {
		s.audit.Event("offline_devices_cleaned", "count", removed)
	}
}
