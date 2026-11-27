package core

import (
	"time"

	"agentremote/internal/platform/audit"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/question"
	"agentremote/internal/platform/session"
)

func questionError(err error) error {
	code := "INVALID_ARGUMENT"
	switch err {
	case question.ErrBusy:
		code = "QUESTION_BUSY"
	case question.ErrNotFound:
		code = "QUESTION_NOT_FOUND"
	case question.ErrConflict:
		code = "REQUEST_CONFLICT"
	case question.ErrLimit:
		code = "QUESTION_LIMIT_REACHED"
	}
	return newProtocolError(code, err.Error())
}
func questionMessage(r question.Record) protocol.Message {
	return protocol.Message{Type: protocol.TypeUserReply, SessionID: r.SessionID, RequestID: r.RequestID, Status: r.Status, ExpiresAt: r.ExpiresAt.Unix(), AnswerSource: r.AnswerSource, Answer: r.Answer, SelectedIndex: r.SelectedIndex}
}
func (s *Service) handleAskUser(caller *Caller, msg protocol.Message) error {
	if err := s.validateID("session_id", msg.SessionID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	if err := s.validateID("request_id", msg.RequestID); err != nil {
		return newProtocolError("INVALID_ARGUMENT", err.Error())
	}
	record, err := s.sessions.Authorize(msg.SessionID, caller.ConnectionID, session.PermissionExecuteCommand)
	if err != nil {
		return s.sessionProtocolError(err)
	}
	connectionID, online := s.devices.OnlineConnection(record.DeviceID)
	if !online || connectionID != record.DeviceConnectionID {
		return newProtocolError("DEVICE_OFFLINE", "device is no longer online")
	}
	timeout := msg.TimeoutSeconds
	if timeout == 0 {
		timeout = 120
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	if record.ExpiresAt.Before(deadline) {
		deadline = record.ExpiresAt
	}
	// Qt's wire deadline is whole seconds; use exactly the same deadline here.
	deadline = deadline.Truncate(time.Second)
	s.expireUserQuestions()
	q, created, err := s.questions.Start(record.ID, msg.RequestID, record.DeviceID, msg.Question, msg.Options, timeout, deadline)
	if err != nil {
		return questionError(err)
	}
	if created {
		s.audit.Event("user_question_submitted", "session", audit.RedactID(record.ID), "request", audit.RedactID(q.RequestID))
		// Activate before forwarding, so Qt sees an authorized session even for direct connections.
		active, err := s.sessions.Activate(record.ID, caller.ConnectionID)
		if err != nil {
			s.questions.FinishSession(record.ID, question.Cancelled)
			return s.sessionProtocolError(err)
		}
		s.notifySessionStatus(active)
		if !s.send(connectionID, protocol.Message{Type: protocol.TypeAskUser, SessionID: record.ID, RequestID: q.RequestID, Question: q.Question, Options: q.Options, ExpiresAt: q.ExpiresAt.Unix(), TimeoutSeconds: timeout}) {
			s.questions.FinishSession(record.ID, question.Unknown)
			return newProtocolError("DEVICE_UNAVAILABLE", "could not deliver question")
		}
	}
	s.expireUserQuestions()
	q, err = s.questions.Get(record.ID, msg.RequestID)
	if err != nil {
		return questionError(err)
	}
	response := questionMessage(q)
	response.Type = protocol.TypeUserQuestionAccepted
	return s.sendResponse(caller, msg, response)
}
func (s *Service) handleGetUserReply(caller *Caller, msg protocol.Message) error {
	record, ok := s.sessions.Get(msg.SessionID)
	// Terminal results remain readable by the original operator after session close.
	if !ok || record.OperatorConnectionID != caller.ConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	s.expireUserQuestions()
	q, err := s.questions.Get(msg.SessionID, msg.RequestID)
	if err != nil {
		return questionError(err)
	}
	return s.sendResponse(caller, msg, questionMessage(q))
}
func (s *Service) handleUserReply(caller *Caller, msg protocol.Message) error {
	record, ok := s.sessions.Get(msg.SessionID)
	if !ok || caller.Role != RoleDevice || caller.DeviceID != record.DeviceID || caller.ConnectionID != record.DeviceConnectionID {
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	}
	if record.Status != session.StatusApproved && record.Status != session.StatusActive || !time.Now().Before(record.ExpiresAt) {
		s.finishUserQuestions(record.ID, question.Cancelled)
		return newProtocolError("SESSION_EXPIRED", "session is no longer authorized")
	}
	if msg.Type == protocol.TypeUserQuestionFailed {
		q, err := s.questions.Get(record.ID, msg.RequestID)
		if err != nil {
			return questionError(err)
		}
		if q.Status == question.Pending {
			s.finishUserQuestions(record.ID, question.Unknown)
		}
		return nil
	}
	q, err := s.questions.Reply(record.ID, msg.RequestID, msg.Status, msg.AnswerSource, msg.Answer, msg.SelectedIndex)
	if err != nil {
		return questionError(err)
	}
	if q.Status == question.TimedOut {
		s.notifyQuestionFinished(q)
	} else {
		s.send(record.OperatorConnectionID, questionMessage(q))
	}
	return nil
}
func (s *Service) finishUserQuestions(sessionID, status string) {
	for _, q := range s.questions.FinishSession(sessionID, status) {
		s.notifyQuestionFinished(q)
	}
}
func (s *Service) notifyQuestionFinished(q question.Record) {
	record, ok := s.sessions.Get(q.SessionID)
	if !ok {
		return
	}
	s.send(record.DeviceConnectionID, protocol.Message{Type: protocol.TypeCancelUserQuestion, SessionID: q.SessionID, RequestID: q.RequestID, Status: q.Status})
	s.send(record.OperatorConnectionID, questionMessage(q))
}
func (s *Service) expireUserQuestions() {
	// Also reconcile a close/submit race: authorization and question insertion
	// live in separate small managers, so closure can precede insertion.
	for _, q := range s.questions.PendingRecords() {
		r, ok := s.sessions.Get(q.SessionID)
		if !ok || r.Status == session.StatusInterrupted {
			s.finishUserQuestions(q.SessionID, question.Unknown)
		} else if r.Status != session.StatusActive && r.Status != session.StatusApproved || !time.Now().Before(r.ExpiresAt) {
			s.finishUserQuestions(q.SessionID, question.Cancelled)
		}
	}
	for _, q := range s.questions.Expire() {
		s.notifyQuestionFinished(q)
	}
}
