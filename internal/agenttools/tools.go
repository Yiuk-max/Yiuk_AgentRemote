// Package agenttools 定义 Agent 可调用的工具目录、输入校验和统一后端接口。
package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	// 以下常量是工具目录中的稳定名称，也用于分发后端调用。
	ListDevices       = "list_devices"
	GetDeviceStatus   = "get_device_status"
	RequestConnection = "request_connection"
	RunCommand        = "run_command" //一站式执行
	GetSessionStatus  = "get_session_status"
	ExecuteCommand    = "execute_command" //在已有会话里提交命令
	GetCommandResult  = "get_command_result"
	DownloadFile      = "download_file"
	UploadFile        = "upload_file"
	CancelCommand     = "cancel_command"
	CloseSession      = "close_session"
)

// ToolSpec 描述一个可供 Agent 调用的工具及其输入、输出结构。
type ToolSpec struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
	Annotations  ToolAnnotations `json:"annotations"`
}

// Request 是后端收到的工具名称和原始 JSON 参数。
type Request struct {
	Name  string
	Input json.RawMessage
}

// Response 是工具调用的统一结果格式；业务错误通过 Code 和 Message 返回。
type Response struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

// Backend 是稳定且独立于 Agent 的接口，由本地客户端或远程平台连接实现
type Backend interface {
	Invoke(context.Context, Request) (any, error) // Invoke 调用工具，返回结构化结果或业务错误
}

type Core struct {
	backend Backend
}

// NewCore 创建工具分发核心
func NewCore(backend Backend) *Core { return &Core{backend: backend} }

// 告诉agent 有哪些工具可用
func (c *Core) Tools() []ToolSpec { return append([]ToolSpec(nil), toolSpecs...) }

// Call 校验工具名称和参数，再调用后端并将错误归一化为 Response。
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

// Error 表示可安全传递给工具调用方的结构化业务错误。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var responseSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"},"code":{"type":"string"},"message":{"type":"string"},"data":{}},"required":["ok","code"],"additionalProperties":false}`)

// ToolAnnotations 是 MCP 提示，供 pi 等宿主展示工具和应用确认策略时使用
type ToolAnnotations struct {
	ReadOnly    bool
	Destructive *bool //可能破坏性
	Idempotent  bool  //幂等
	OpenWorld   *bool //可能与本系统之外的外部实体或环境交互
}

// 表达可选的布尔提示。
func boolPtr(value bool) *bool { return &value }

// tool 构造工具定义，并为所有工具附加相同的响应结构。
func tool(name, description, input string, annotations ToolAnnotations) ToolSpec {
	return ToolSpec{Name: name, Description: description, InputSchema: json.RawMessage(input), OutputSchema: append(json.RawMessage(nil), responseSchema...), Annotations: annotations}
}

// toolSpecs 是 MCP 暴露给 Agent 的唯一工具目录，输入 schema 与运行时校验需保持一致。
var toolSpecs = []ToolSpec{
	tool(ListDevices, "List devices available to the current operator.", `{"type":"object","properties":{},"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(GetDeviceStatus, "Get online and session status for a device.", `{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128}},"required":["device_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(RequestConnection, "Request a remote session to one target device. Without temporary_password the device owner must approve; with the owner's current temporary password, the session connects directly without changing the target's mode setting. Pass wait_for_approval_seconds to wait for an owner decision when requesting approval. A pending session is not authorization to execute commands. Request only the permissions needed: execute_command runs PowerShell; read_file downloads one remote file; write_file uploads one local file to the device.", `{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128},"purpose":{"type":"string","minLength":1,"maxLength":512},"permissions":{"type":"array","items":{"type":"string","enum":["execute_command","read_file","write_file"]},"minItems":1,"uniqueItems":true},"temporary_password":{"type":"string","minLength":32,"maxLength":256},"wait_for_approval_seconds":{"type":"integer","minimum":0,"maximum":300}},"required":["device_id","purpose","permissions"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(RunCommand, "One-shot remote task for a server-hosted agent: ask the human for the target device code (device_id) and its 32-character temporary password, request a session, optionally wait for owner approval, execute one PowerShell command, wait for the result, then close the session unless keep_session_open is true. Returns aggregated stdout, stderr, exit code, status and truncation flag.", `{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128},"temporary_password":{"type":"string","minLength":32,"maxLength":256},"command":{"type":"string","minLength":1,"maxLength":32768},"purpose":{"type":"string","maxLength":512},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600},"wait_for_approval_seconds":{"type":"integer","minimum":0,"maximum":300},"keep_session_open":{"type":"boolean"}},"required":["device_id","temporary_password","command"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(GetSessionStatus, "Get the current status of a connection request or session.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(ExecuteCommand, "Execute one PowerShell command in an approved session with execute_command permission.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"command":{"type":"string","minLength":1,"maxLength":32768},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600}},"required":["session_id","command"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(GetCommandResult, "Read output and final status for a command task.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128},"cursor":{"type":"integer","minimum":0}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(DownloadFile, "Download one remote file from an approved session with read_file permission. The device owner must have approved this permission. The file is written locally and the result returns its local_path; files larger than the configured limit are rejected.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"remote_path":{"type":"string","minLength":1,"maxLength":4096},"destination_path":{"type":"string","maxLength":4096}},"required":["session_id","remote_path"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(UploadFile, "Upload one local file to the target device in an approved session with write_file permission. The device owner must have approved this permission. Files are limited to 32 MiB; the device refuses to replace an existing file unless overwrite is true. remote_path may be a full path, or empty to let the device store the file in its uploads directory; the result returns the final remote_path, size and SHA-256.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"local_path":{"type":"string","minLength":1,"maxLength":4096},"remote_path":{"type":"string","maxLength":4096},"overwrite":{"type":"boolean"}},"required":["session_id","local_path"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(CancelCommand, "Cancel a running command in an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(CloseSession, "Close a remote session and revoke its authorization.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), Idempotent: true, OpenWorld: boolPtr(false)}),
}

// 名称->工具定义 映射
var specByName = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, len(toolSpecs))
	for _, spec := range toolSpecs {
		m[spec.Name] = spec
	}
	return m
}()

// validateInput 在调用后端前执行严格校验，拒绝未知字段及不符合约束的参数。
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
		required = []string{"device_id", "purpose", "permissions", "temporary_password"}
	case RunCommand:
		required = []string{"device_id", "temporary_password", "command"}
	case GetSessionStatus, CloseSession:
		required = []string{"session_id"}
	case ExecuteCommand:
		required = []string{"session_id", "command"}
	case GetCommandResult, CancelCommand:
		required = []string{"session_id", "request_id"}
	case DownloadFile:
		required = []string{"session_id", "remote_path"}
	case UploadFile:
		required = []string{"session_id", "local_path"}
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
		WaitForApproval  *int     `json:"wait_for_approval_seconds"`
		KeepSessionOpen  *bool    `json:"keep_session_open"`
		RemotePath       string   `json:"remote_path"`
		DestinationPath  string   `json:"destination_path"`
		LocalPath        string   `json:"local_path"`
		Overwrite        *bool    `json:"overwrite"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	for _, key := range []string{"device_id", "purpose", "session_id", "request_id", "command", "working_directory", "temporary_password", "remote_path", "destination_path", "local_path"} {
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
	if len(input.RemotePath) > 4096 || len(input.DestinationPath) > 4096 || len(input.LocalPath) > 4096 {
		return fmt.Errorf("file path exceeds 4096 characters")
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
			if (permission != "execute_command" && permission != "read_file" && permission != "write_file") || seen[permission] {
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
	if input.WaitForApproval != nil && (*input.WaitForApproval < 0 || *input.WaitForApproval > 300) {
		return fmt.Errorf("wait_for_approval_seconds must be between 0 and 300")
	}
	return nil
}

// schemaFields 按工具记录允许的字段，用于补充 JSON Schema 的未知字段检查。
var schemaFields = map[string]map[string]struct{}{
	ListDevices:       fieldSet(),
	GetDeviceStatus:   fieldSet("device_id"),
	RequestConnection: fieldSet("device_id", "purpose", "permissions", "temporary_password", "wait_for_approval_seconds"),
	RunCommand:        fieldSet("device_id", "temporary_password", "command", "purpose", "working_directory", "timeout_seconds", "wait_for_approval_seconds", "keep_session_open"),
	GetSessionStatus:  fieldSet("session_id"),
	ExecuteCommand:    fieldSet("session_id", "command", "working_directory", "timeout_seconds"),
	GetCommandResult:  fieldSet("session_id", "request_id", "cursor"),
	DownloadFile:      fieldSet("session_id", "remote_path", "destination_path"),
	UploadFile:        fieldSet("session_id", "local_path", "remote_path", "overwrite"),
	CancelCommand:     fieldSet("session_id", "request_id"),
	CloseSession:      fieldSet("session_id"),
}

// fieldSet 将字段名切片转换为便于查询的集合。
func fieldSet(keys ...string) map[string]struct{} {
	fields := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		fields[key] = struct{}{}
	}
	return fields
}
