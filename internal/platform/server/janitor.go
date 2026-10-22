package server

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
		for _, t := range s.transfers.FinishSession(ses.ID, transfer.StatusCancelled) {
			s.sendToConnection(ses.OperatorConnectionID, transferResultMessage(t))
		}
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
