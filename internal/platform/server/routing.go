package server

import (
	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
)

func (s *Server) send(c *conn, m protocol.Message) bool {
	if c == nil {
		return false
	}
	select {
	case <-c.done:
		return false
	default:
	}
	if m.Version == 0 {
		m.Version = protocol.Version
	}
	if m.BusinessType == "" {
		m.BusinessType = protocol.BusinessAgentAssist
	}
	select {
	case c.send <- m:
		return true
	case <-c.done:
		return false
	default:
		s.audit.Warn("slow_client_disconnected", "connection", audit.RedactID(c.id))
		c.close("slow_client")
		return false
	}
}

func (s *Server) lookupConn(id string) *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

func (s *Server) sendToConnection(id string, m protocol.Message) bool {
	if id == "" {
		return false
	}
	return s.send(s.lookupConn(id), m)
}

func (s *Server) sendToDevice(deviceID string, m protocol.Message) bool {
	connID, ok := s.devices.OnlineConnection(deviceID)
	if !ok {
		return false
	}
	return s.sendToConnection(connID, m)
}

func (s *Server) notifySession(ses *session.Session) {
	if ses == nil {
		return
	}
	info := sessionInfo(ses)
	msg := protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: ses.ID,
		DeviceID:  ses.DeviceID,
		Status:    string(ses.Status),
		Session:   &info,
	}
	seen := make(map[string]struct{}, 2)
	for _, id := range []string{ses.OperatorConnectionID, ses.DeviceConnectionID} {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		s.sendToConnection(id, msg)
	}
}

// sendOK sends a direct response and echoes the caller's correlation id.
// Asynchronous events do not pass through this helper.
func (s *Server) sendOK(c *conn, req, resp protocol.Message) error {
	resp.CorrelationID = req.CorrelationID
	s.send(c, resp)
	return nil
}
