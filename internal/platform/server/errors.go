package server

import (
	"errors"
	"strings"

	"yiuk-switchboard/internal/platform/auth"
	"yiuk-switchboard/internal/platform/device"
	"yiuk-switchboard/internal/platform/session"
	"yiuk-switchboard/internal/platform/task"
	"yiuk-switchboard/internal/platform/transfer"
)

func (s *Server) validID(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return errors.New(field + " is required")
	}
	if len(value) > s.cfg.IdentifierMaxLen {
		return errors.New(field + " is too long")
	}
	return nil
}

func (s *Server) deviceError(err error) error {
	switch {
	case errors.Is(err, device.ErrDeviceNotFound):
		return perr("DEVICE_NOT_FOUND", "device not found") // 设备未找到
	case errors.Is(err, device.ErrDeviceLimit):
		return perr("DEVICE_LIMIT", "device limit reached")
	case errors.Is(err, device.ErrDeviceOnline):
		return perr("DEVICE_ALREADY_ONLINE", "device is already online")
	case errors.Is(err, device.ErrBadPassword):
		return perr("BAD_TEMPORARY_PASSWORD", "temporary password is incorrect")
	case errors.Is(err, device.ErrPasswordExpiry):
		return perr("TEMPORARY_PASSWORD_EXPIRED", "temporary password has expired")
	case errors.Is(err, device.ErrPasswordRevoke):
		return perr("TEMPORARY_PASSWORD_REVOKED", "temporary password has been revoked")
	case errors.Is(err, device.ErrStaleVersion):
		return perr("STALE_PASSWORD_VERSION", "password version is stale")
	case errors.Is(err, device.ErrInvalidMode):
		return perr("INVALID_ARGUMENT", "invalid authorization mode")
	default:
		return perr("INVALID_ARGUMENT", "device request failed")
	}
}

func (s *Server) sessionError(err error) error {
	switch {
	case errors.Is(err, session.ErrNotFound):
		return perr("SESSION_NOT_FOUND", "session not found")
	case errors.Is(err, session.ErrSessionLimit):
		return perr("SESSION_LIMIT", "session limit reached")
	case errors.Is(err, session.ErrNotOwner):
		return perr("PERMISSION_DENIED", "connection does not own the session")
	case errors.Is(err, session.ErrNoPermission):
		return perr("PERMISSION_DENIED", "session does not grant this permission")
	case errors.Is(err, session.ErrExpired):
		return perr("SESSION_EXPIRED", "session has expired")
	case errors.Is(err, session.ErrInvalidState):
		return perr("INVALID_SESSION_STATE", "session is not in a state that permits this operation")
	case errors.Is(err, session.ErrInvalidPurpose):
		return perr("INVALID_ARGUMENT", "purpose is required")
	default:
		return perr("INVALID_ARGUMENT", "session request failed")
	}
}

func (s *Server) transferError(err error) error {
	switch {
	case errors.Is(err, transfer.ErrNotFound):
		return perr("TRANSFER_NOT_FOUND", "file transfer not found")
	case errors.Is(err, transfer.ErrTransferLimit):
		return perr("TRANSFER_LIMIT", "transfer limit reached for session")
	case errors.Is(err, transfer.ErrRequestConflict):
		return perr("REQUEST_ID_CONFLICT", "request id is already used by another session")
	case errors.Is(err, transfer.ErrSequence):
		return perr("INVALID_CHUNK_SEQUENCE", "file chunk sequence is not contiguous")
	case errors.Is(err, transfer.ErrChunkTooLarge):
		return perr("CHUNK_TOO_LARGE", "file chunk exceeds the maximum chunk size")
	case errors.Is(err, transfer.ErrEmptyChunk):
		return perr("INVALID_ARGUMENT", "file chunk is empty")
	case errors.Is(err, transfer.ErrChunkLimit):
		return perr("CHUNK_LIMIT", "file transfer exceeds the maximum chunk count")
	case errors.Is(err, transfer.ErrInvalidBase64):
		return perr("INVALID_ARGUMENT", "file chunk is not valid base64")
	case errors.Is(err, transfer.ErrSizeLimit):
		return perr("FILE_TOO_LARGE", "file download exceeds the configured size limit")
	case errors.Is(err, transfer.ErrTerminal):
		return perr("TRANSFER_ALREADY_FINISHED", "file transfer is already finished")
	case errors.Is(err, transfer.ErrInvalidStatus):
		return perr("INVALID_ARGUMENT", "invalid transfer status")
	default:
		return perr("INVALID_ARGUMENT", "file transfer request failed")
	}
}

func terminalTransferStatus(status string) (transfer.Status, error) {
	switch transfer.Status(status) {
	case transfer.StatusSucceeded, transfer.StatusFailed, transfer.StatusCancelled, transfer.StatusResultUnknown:
		return transfer.Status(status), nil
	default:
		return "", errors.New("status must be a terminal transfer status")
	}
}

func (s *Server) taskError(err error) error {
	switch {
	case errors.Is(err, task.ErrNotFound):
		return perr("TASK_NOT_FOUND", "task not found")
	case errors.Is(err, task.ErrTaskLimit):
		return perr("TASK_LIMIT", "task limit reached for session")
	case errors.Is(err, task.ErrRequestConflict):
		return perr("REQUEST_ID_CONFLICT", "request id is already used by another session")
	case errors.Is(err, task.ErrTerminal):
		return perr("TASK_ALREADY_FINISHED", "task is already in a terminal state")
	case errors.Is(err, task.ErrInvalidStatus):
		return perr("INVALID_ARGUMENT", "invalid task status")
	default:
		return perr("INVALID_ARGUMENT", "task request failed")
	}
}

func terminalTaskStatus(status string) (task.Status, error) {
	switch task.Status(status) {
	case task.StatusSucceeded, task.StatusFailed, task.StatusTimedOut, task.StatusCancelled, task.StatusResultUnknown:
		return task.Status(status), nil
	default:
		return "", errors.New("status must be a terminal task status")
	}
}

// errorCode maps internal errors to the stable protocol error code used in
// audit logs. Logs must never contain free-form internal error text.
func errorCode(err error) string {
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
	default:
		return "INTERNAL"
	}
}

// protoError is a client-visible protocol error. Fatal errors close the
// connection after the error frame is written.
type protoError struct {
	code    string
	message string
	fatal   bool
}

func (e *protoError) Error() string { return e.code + ": " + e.message }

func perr(code, message string) error {
	return &protoError{code: code, message: message}
}

func ferr(code, message string) error {
	return &protoError{code: code, message: message, fatal: true}
}
