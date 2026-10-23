package server

//send/sendToConnection/sendToDevice/notifySession；有界发送队列满就直接断开慢客户端；sendOK 回显 correlation_id
import (
	"yiuk-switchboard/internal/platform/audit"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/platform/session"
)

// 发送信息
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

// 发送信息到指定连接，找到连接，上锁，发送信息
func (s *Server) lookupConn(id string) *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[id]
}

// 发送信息到指定连接，找到连接，上锁，发送信息
func (s *Server) sendToConnection(id string, m protocol.Message) bool {
	if id == "" {
		return false
	}
	return s.send(s.lookupConn(id), m)
}

// 发送信息到指定设备，找到设备的在线连接，发送信息
func (s *Server) sendToDevice(deviceID string, m protocol.Message) bool {
	connID, ok := s.devices.OnlineConnection(deviceID)
	if !ok {
		return false
	}
	return s.sendToConnection(connID, m)
}

// 通知会话状态变化，向操作者和设备发送会话状态消息
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
