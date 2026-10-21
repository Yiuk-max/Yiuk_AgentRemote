package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ListDevices       = "list_devices"
	GetDeviceStatus   = "get_device_status"
	RequestConnection = "request_connection"
	GetSessionStatus  = "get_session_status"
	ExecuteCommand    = "execute_command"
	GetCommandResult  = "get_command_result"
	CancelCommand     = "cancel_command"
	CloseSession      = "close_session"
)

type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type Request struct {
	Name  string
	Input json.RawMessage
}

type Response struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// Backend is the stable, Agent-independent boundary implemented by the
// future local client/remote platform connection.
type Backend interface {
	Invoke(context.Context, Request) (any, error)
}

type Core struct {
	backend Backend
}

func NewCore(backend Backend) *Core { return &Core{backend: backend} }

func (c *Core) Tools() []ToolSpec { return append([]ToolSpec(nil), toolSpecs...) }

func (c *Core) Call(ctx context.Context, name string, input json.RawMessage) Response {
	spec, ok := specByName[name]
	if !ok {
		return Response{Code: "UNKNOWN_TOOL", Message: "tool is not supported"}
	}
	if err := validateInput(input, name); err != nil {
		return Response{Code: "INVALID_ARGUMENT", Message: err.Error()}
	}
	if c.backend == nil {
		return Response{Code: "NOT_CONFIGURED", Message: "remote platform backend is not configured"}
	}
	data, err := c.backend.Invoke(ctx, Request{Name: spec.Name, Input: append(json.RawMessage(nil), input...)})
	if err != nil {
		var toolErr *Error
		if errors.As(err, &toolErr) {
			return Response{Code: toolErr.Code, Message: toolErr.Message}
		}
		return Response{Code: "INTERNAL", Message: "tool request failed"}
	}
	return Response{OK: true, Code: "OK", Data: data}
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var toolSpecs = []ToolSpec{
	{ListDevices, "List devices available to the current operator.", json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)},
	{GetDeviceStatus, "Get online and session status for a device.", json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128}},"required":["device_id"],"additionalProperties":false}`)},
	{RequestConnection, "Request an owner-approved remote session. A pending response is not authorization to execute commands. A platform backend requires the device temporary_password shared by the device owner.", json.RawMessage(`{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128},"purpose":{"type":"string","minLength":1,"maxLength":512},"permissions":{"type":"array","items":{"type":"string","enum":["execute_command"]},"minItems":1,"uniqueItems":true},"temporary_password":{"type":"string","minLength":32,"maxLength":256}},"required":["device_id","purpose","permissions"],"additionalProperties":false}`)},
	{GetSessionStatus, "Get the current status of a connection request or session.", json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id"],"additionalProperties":false}`)},
	{ExecuteCommand, "Execute one PowerShell command in an approved session.", json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"command":{"type":"string","minLength":1,"maxLength":32768},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600}},"required":["session_id","command"],"additionalProperties":false}`)},
	{GetCommandResult, "Read output and final status for a command task.", json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128},"cursor":{"type":"integer","minimum":0}},"required":["session_id","request_id"],"additionalProperties":false}`)},
	{CancelCommand, "Cancel a running command in an approved session.", json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id","request_id"],"additionalProperties":false}`)},
	{CloseSession, "Close a remote session and revoke its authorization.", json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id"],"additionalProperties":false}`)},
}

var specByName = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, len(toolSpecs))
	for _, spec := range toolSpecs {
		m[spec.Name] = spec
	}
	return m
}()

func validateInput(raw json.RawMessage, name string) error {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return fmt.Errorf("arguments must be a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("arguments must be a JSON object")
	}
	var required []string
	switch name {
	case ListDevices:
	case GetDeviceStatus:
		required = []string{"device_id"}
	case RequestConnection:
		required = []string{"device_id", "purpose", "permissions"}
	case GetSessionStatus, CloseSession:
		required = []string{"session_id"}
	case ExecuteCommand:
		required = []string{"session_id", "command"}
	case GetCommandResult, CancelCommand:
		required = []string{"session_id", "request_id"}
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return fmt.Errorf("missing required field %q", key)
		}
	}
	var input struct {
		DeviceID         string   `json:"device_id"`
		Purpose          string   `json:"purpose"`
		Permissions      []string `json:"permissions"`
		TemporaryPass    string   `json:"temporary_password"`
		SessionID        string   `json:"session_id"`
		RequestID        string   `json:"request_id"`
		Command          string   `json:"command"`
		WorkingDirectory string   `json:"working_directory"`
		TimeoutSeconds   *int     `json:"timeout_seconds"`
		Cursor           *int     `json:"cursor"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	for _, key := range []string{"device_id", "purpose", "session_id", "request_id", "command", "working_directory", "temporary_password"} {
		if value, ok := fields[key]; ok {
			var s string
			if err := json.Unmarshal(value, &s); err != nil {
				return fmt.Errorf("field %q must be a string", key)
			}
			if key != "working_directory" && strings.TrimSpace(s) == "" {
				return fmt.Errorf("field %q must not be empty", key)
			}
		}
	}
	for key := range fields {
		if _, ok := schemaFields[name][key]; !ok {
			return fmt.Errorf("unknown field %q", key)
		}
	}
	if len(input.DeviceID) > 128 || len(input.SessionID) > 128 || len(input.RequestID) > 128 {
		return fmt.Errorf("identifier exceeds 128 characters")
	}
	if len(input.Purpose) > 512 {
		return fmt.Errorf("purpose exceeds 512 characters")
	}
	if len(input.Command) > 32768 {
		return fmt.Errorf("command exceeds 32768 characters")
	}
	if len(input.WorkingDirectory) > 4096 {
		return fmt.Errorf("working_directory exceeds 4096 characters")
	}
	if input.TemporaryPass != "" && (len(input.TemporaryPass) < 32 || len(input.TemporaryPass) > 256) {
		return fmt.Errorf("temporary_password must be between 32 and 256 characters")
	}
	if name == RequestConnection {
		if len(input.Permissions) == 0 {
			return fmt.Errorf("permissions must contain at least one permission")
		}
		seen := make(map[string]bool)
		for _, permission := range input.Permissions {
			if permission != "execute_command" || seen[permission] {
				return fmt.Errorf("unsupported or duplicate permission %q", permission)
			}
			seen[permission] = true
		}
	}
	if input.TimeoutSeconds != nil && (*input.TimeoutSeconds < 1 || *input.TimeoutSeconds > 3600) {
		return fmt.Errorf("timeout_seconds must be between 1 and 3600")
	}
	if input.Cursor != nil && *input.Cursor < 0 {
		return fmt.Errorf("cursor must not be negative")
	}
	return nil
}

var schemaFields = map[string]map[string]struct{}{
	ListDevices:       fieldSet(),
	GetDeviceStatus:   fieldSet("device_id"),
	RequestConnection: fieldSet("device_id", "purpose", "permissions", "temporary_password"),
	GetSessionStatus:  fieldSet("session_id"),
	ExecuteCommand:    fieldSet("session_id", "command", "working_directory", "timeout_seconds"),
	GetCommandResult:  fieldSet("session_id", "request_id", "cursor"),
	CancelCommand:     fieldSet("session_id", "request_id"),
	CloseSession:      fieldSet("session_id"),
}

func fieldSet(keys ...string) map[string]struct{} {
	fields := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		fields[key] = struct{}{}
	}
	return fields
}
