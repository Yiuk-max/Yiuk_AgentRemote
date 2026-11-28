package core

import (
	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/question"
	"agentremote/internal/platform/session"
)

// 出站消息统一走 send：编码一次，交给 Link 投递。
// 投递失败（连接已断、队列满被踢）返回 false，调用方按“设备不可用”处理，
// 不谎报成功。
func (s *Service) send(connectionID string, message protocol.Message) bool {
	if connectionID == "" || s.link == nil {
		return false
	}
	frame := s.encode(message) // 编码序列化
	if frame == nil {
		return false
	}
	// 把消息投递到网关
	if err := s.link.Deliver(connectionID, frame); err != nil {
		s.logger.Debug("platform message delivery failed", "connection", audit.RedactID(connectionID), "error", err)
		return false
	}
	return true
}

// encode 补齐协议元数据并编码成字节；编码失败时返回 nil 并记日志。
func (s *Service) encode(message protocol.Message) []byte {
	if message.Version == 0 {
		message.Version = protocol.Version
	}
	if message.BusinessType == "" {
		message.BusinessType = protocol.BusinessAgentAssist
	}
	frame, err := s.codec.Encode(message)
	if err != nil {
		s.logger.Error("platform message encode failed", "error", err)
		return nil
	}
	return frame
}

// notifySessionStatus 通知会话状态变化，向操作者和设备发送会话状态消息。
func (s *Service) notifySessionStatus(sessionRecord *session.Session) {
	if sessionRecord == nil {
		return
	}
	switch sessionRecord.Status {
	case session.StatusClosed, session.StatusExpired, session.StatusRejected:
		s.finishUserQuestions(sessionRecord.ID, question.Cancelled)
	case session.StatusInterrupted:
		s.finishUserQuestions(sessionRecord.ID, question.Unknown)
	}
	info := protocolSessionInfo(sessionRecord)
	statusMessage := protocol.Message{
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
		s.send(connectionID, statusMessage)
	}
}

// notifyOperatorApproval 把设备批准时交出的临时密码单独发给操作者。
//
// 这是唯一携带该密码的出站消息：notifySessionStatus 永远不带，
// 因为同一条通知也会发给设备，而凭据只该到操作者手里。
func (s *Service) notifyOperatorApproval(sessionRecord *session.Session, password string) {
	if sessionRecord == nil || sessionRecord.OperatorConnectionID == "" || password == "" {
		return
	}
	info := protocolSessionInfo(sessionRecord)
	s.send(sessionRecord.OperatorConnectionID, protocol.Message{
		Type:              protocol.TypeSessionStatus,
		SessionID:         sessionRecord.ID,
		DeviceID:          sessionRecord.DeviceID,
		Status:            string(sessionRecord.Status),
		Session:           &info,
		TemporaryPassword: password,
	})
}

// sendResponse 回显请求的 correlation_id 后投递直接响应。
// 异步事件不通过此方法发送。
func (s *Service) sendResponse(caller *Caller, request, response protocol.Message) error {
	response.CorrelationID = request.CorrelationID
	s.send(caller.ConnectionID, response)
	return nil
}
