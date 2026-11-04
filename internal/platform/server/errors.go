package server

import (
	"errors"
	"strings"

	"agentremote/internal/platform/auth"
	"agentremote/internal/platform/device"
	"agentremote/internal/platform/session"
	"agentremote/internal/platform/task"
	"agentremote/internal/platform/transfer"
)

func (s *Server) validateID(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New(field + " is required")
	}
	if len(value) > s.config.IdentifierMaxLen {
		return errors.New(field + " is too long")
	}
	return nil
}

func (s *Server) deviceProtocolError(err error) error {
	switch {
	case errors.Is(err, device.ErrDeviceNotFound):
		return newProtocolError("DEVICE_NOT_FOUND", "device not found") // 设备未找到
	case errors.Is(err, device.ErrDeviceLimit):
		return newProtocolError("DEVICE_LIMIT", "device limit reached")
	case errors.Is(err, device.ErrDeviceOnline):
		return newProtocolError("DEVICE_ALREADY_ONLINE", "device is already online")
	case errors.Is(err, device.ErrBadPassword):
		return newProtocolError("BAD_TEMPORARY_PASSWORD", "temporary password is incorrect")
	case errors.Is(err, device.ErrPasswordExpiry):
		return newProtocolError("TEMPORARY_PASSWORD_EXPIRED", "temporary password has expired")
	case errors.Is(err, device.ErrPasswordRevoke):
		return newProtocolError("TEMPORARY_PASSWORD_REVOKED", "temporary password has been revoked")
	case errors.Is(err, device.ErrStaleVersion):
		return newProtocolError("STALE_PASSWORD_VERSION", "password version is stale")
	case errors.Is(err, device.ErrInvalidMode):
		return newProtocolError("INVALID_ARGUMENT", "invalid authorization mode")
	case errors.Is(err, device.ErrDeviceIDExhausted):
		// 9 位十进制空间耗尽：对客户端而言就是资源耗尽。
		return newProtocolError("DEVICE_LIMIT", "no free device id available")
	default:
		return newProtocolError("INVALID_ARGUMENT", "device request failed")
	}
}

func (s *Server) sessionProtocolError(err error) error {
	switch {
	case errors.Is(err, session.ErrNotFound):
		return newProtocolError("SESSION_NOT_FOUND", "session not found")
	case errors.Is(err, session.ErrSessionLimit):
		return newProtocolError("SESSION_LIMIT", "session limit reached")
	case errors.Is(err, session.ErrNotOwner):
		return newProtocolError("PERMISSION_DENIED", "connection does not own the session")
	case errors.Is(err, session.ErrNoPermission):
		return newProtocolError("PERMISSION_DENIED", "session does not grant this permission")
	case errors.Is(err, session.ErrExpired):
		return newProtocolError("SESSION_EXPIRED", "session has expired")
	case errors.Is(err, session.ErrInvalidState):
		return newProtocolError("INVALID_SESSION_STATE", "session is not in a state that permits this operation")
	case errors.Is(err, session.ErrInvalidPurpose):
		return newProtocolError("INVALID_ARGUMENT", "purpose is required")
	default:
		return newProtocolError("INVALID_ARGUMENT", "session request failed")
	}
}

func (s *Server) transferProtocolError(err error) error {
	switch {
	case errors.Is(err, transfer.ErrNotFound):
		return newProtocolError("TRANSFER_NOT_FOUND", "file transfer not found")
	case errors.Is(err, transfer.ErrTransferLimit):
		return newProtocolError("TRANSFER_LIMIT", "transfer limit reached for session")
	case errors.Is(err, transfer.ErrRequestConflict):
		return newProtocolError("REQUEST_ID_CONFLICT", "request id is already used by another session")
	case errors.Is(err, transfer.ErrSequence):
		return newProtocolError("INVALID_CHUNK_SEQUENCE", "file chunk sequence is not contiguous")
	case errors.Is(err, transfer.ErrChunkTooLarge):
		return newProtocolError("CHUNK_TOO_LARGE", "file chunk exceeds the maximum chunk size")
	case errors.Is(err, transfer.ErrEmptyChunk):
		return newProtocolError("INVALID_ARGUMENT", "file chunk is empty")
	case errors.Is(err, transfer.ErrChunkLimit):
		return newProtocolError("CHUNK_LIMIT", "file transfer exceeds the maximum chunk count")
	case errors.Is(err, transfer.ErrInvalidBase64):
		return newProtocolError("INVALID_ARGUMENT", "file chunk is not valid base64")
	case errors.Is(err, transfer.ErrSizeLimit):
		return newProtocolError("FILE_TOO_LARGE", "file download exceeds the configured size limit")
	case errors.Is(err, transfer.ErrTerminal):
		return newProtocolError("TRANSFER_ALREADY_FINISHED", "file transfer is already finished")
	case errors.Is(err, transfer.ErrInvalidStatus):
		return newProtocolError("INVALID_ARGUMENT", "invalid transfer status")
	default:
		return newProtocolError("INVALID_ARGUMENT", "file transfer request failed")
	}
}

func parseTerminalTransferStatus(status string) (transfer.Status, error) {
	switch transfer.Status(status) {
	case transfer.StatusSucceeded, transfer.StatusFailed, transfer.StatusCancelled, transfer.StatusResultUnknown:
		return transfer.Status(status), nil
	default:
		return "", errors.New("status must be a terminal transfer status")
	}
}

func (s *Server) taskProtocolError(err error) error {
	switch {
	case errors.Is(err, task.ErrNotFound):
		return newProtocolError("TASK_NOT_FOUND", "task not found")
	case errors.Is(err, task.ErrTaskLimit):
		return newProtocolError("TASK_LIMIT", "task limit reached for session")
	case errors.Is(err, task.ErrRequestConflict):
		return newProtocolError("REQUEST_ID_CONFLICT", "request id is already used by another session")
	case errors.Is(err, task.ErrTerminal):
		return newProtocolError("TASK_ALREADY_FINISHED", "task is already in a terminal state")
	case errors.Is(err, task.ErrInvalidStatus):
		return newProtocolError("INVALID_ARGUMENT", "invalid task status")
	default:
		return newProtocolError("INVALID_ARGUMENT", "task request failed")
	}
}

func parseTerminalTaskStatus(status string) (task.Status, error) {
	switch task.Status(status) {
	case task.StatusSucceeded, task.StatusFailed, task.StatusTimedOut, task.StatusCancelled, task.StatusResultUnknown:
		return task.Status(status), nil
	default:
		return "", errors.New("status must be a terminal task status")
	}
}

// auditErrorCode maps internal errors to the stable protocol error code used in
// audit logs. Logs must never contain free-form internal error text.
func auditErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, auth.ErrRateLimited):
		return "RATE_LIMITED"
	case errors.Is(err, auth.ErrBadPassword):
		return "BAD_PASSWORD"
	case errors.Is(err, device.ErrDeviceNotFound):
		return "DEVICE_NOT_FOUND"
	case errors.Is(err, device.ErrBadPassword):
		return "BAD_TEMPORARY_PASSWORD"
	case errors.Is(err, device.ErrPasswordExpiry):
		return "TEMPORARY_PASSWORD_EXPIRED"
	case errors.Is(err, device.ErrPasswordRevoke):
		return "TEMPORARY_PASSWORD_REVOKED"
	case errors.Is(err, device.ErrDeviceIDExhausted):
		return "DEVICE_LIMIT"
	default:
		return "INTERNAL"
	}
}

// protocolError is a client-visible protocol error. Fatal errors close the
// connection after the error frame is written.
type protocolError struct {
	code    string
	message string
	fatal   bool
}

func (e *protocolError) Error() string { return e.code + ": " + e.message }

func newProtocolError(code, message string) error {
	return &protocolError{code: code, message: message}
}

func newFatalProtocolError(code, message string) error {
	return &protocolError{code: code, message: message, fatal: true}
}
