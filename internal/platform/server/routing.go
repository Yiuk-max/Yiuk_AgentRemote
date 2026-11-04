package server

// 消息通过有界发送队列转发；队列满时断开慢客户端。
// 直接响应使用 sendResponse 回显 correlation_id。
import (
	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/session"
)

// enqueueMessage 补齐协议元数据并尝试入队；队列满时断开慢客户端。
func (s *Server) enqueueMessage(clientConn *clientConnection, message protocol.Message) bool {
	if clientConn == nil {
		return false
	}
	select {
	case <-clientConn.closed:
		return false
	default:
	}
	if message.Version == 0 {
		message.Version = protocol.Version
	}
	if message.BusinessType == "" {
		message.BusinessType = protocol.BusinessAgentAssist
	}
	select { // 入队、连接关闭，或队列已满时断开慢客户端
	case clientConn.outgoingMessages <- message:
		return true
	case <-clientConn.closed:
		return false
	default:
		s.audit.Warn("slow_client_disconnected", "connection", audit.RedactID(clientConn.id))
		clientConn.close("slow_client")
		return false
	}
}

// connectionByID 返回已注册的连接；未找到时返回 nil。
func (s *Server) connectionByID(connectionID string) *clientConnection {
	s.connectionsMu.Lock()
	defer s.connectionsMu.Unlock()
	return s.connections[connectionID]
}

// 发送信息到指定连接
func (s *Server) sendToConnection(connectionID string, message protocol.Message) bool {
	if connectionID == "" {
		return false
	}
	return s.enqueueMessage(s.connectionByID(connectionID), message)
}

// 发送信息到指定设备，找到设备的在线连接，发送信息
func (s *Server) sendToDevice(deviceID string, message protocol.Message) bool {
	connectionID, ok := s.devices.OnlineConnection(deviceID)
	if !ok {
		return false
	}
	return s.sendToConnection(connectionID, message)
}

// 通知会话状态变化，向操作者和设备发送会话状态消息
func (s *Server) notifySessionStatus(sessionRecord *session.Session) {
	if sessionRecord == nil {
		return
	}
	info := protocolSessionInfo(sessionRecord)
	resultMessage := protocol.Message{
		Type:      protocol.TypeSessionStatus,
		SessionID: sessionRecord.ID,
		DeviceID:  sessionRecord.DeviceID,
		Status:    string(sessionRecord.Status),
		Session:   &info,
	}
	notifiedConnections := make(map[string]struct{}, 2)
	for _, connectionID := range []string{sessionRecord.OperatorConnectionID, sessionRecord.DeviceConnectionID} {
		if connectionID == "" {
			continue
		}
		if _, ok := notifiedConnections[connectionID]; ok {
			continue
		}
		notifiedConnections[connectionID] = struct{}{}
		s.sendToConnection(connectionID, resultMessage)
	}
}

// sendResponse 将直接响应入队并回显请求的关联 ID，响应可以携带成功或失败结果。
// 异步事件不通过此方法发送。
func (s *Server) sendResponse(clientConn *clientConnection, request, response protocol.Message) error {
	response.CorrelationID = request.CorrelationID
	s.enqueueMessage(clientConn, response)
	return nil
}
