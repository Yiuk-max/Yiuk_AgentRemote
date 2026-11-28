// Package agenttools 定义 Agent 可调用的工具目录、输入校验和统一后端接口。
package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// 以下常量是工具目录中的稳定名称，也用于分发后端调用。
	AskUser           = "ask_user"
	GetUserReply      = "get_user_reply"
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
	TakeScreenshot    = "take_screenshot"
	PressKeys         = "press_keys"
	MoveMouse         = "move_mouse"
	Wait              = "wait"
	WaitFor           = "wait_for"
	ListDir           = "list_dir"
	StatPath          = "stat_path"
	Click             = "click"
	TypeText          = "type_text"
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
	// Hint 是可执行的下一步建议（例如“先调用 list_devices”），
	// 让 agent 不用猜错误含义。
	Hint   string  `json:"hint,omitempty"`
	Data   any     `json:"data,omitempty"`
	Images []Image `json:"-"`
}

// Backend 是稳定且独立于 Agent 的接口，由本地客户端或远程平台连接实现
type Backend interface {
	Invoke(context.Context, Request) (any, error) // Invoke 调用工具，返回结构化结果或业务错误
}

type Core struct {
	backend Backend
	desktop desktopState
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
	data, images, err := c.invoke(ctx, spec.Name, input)
	if err != nil {
		var toolErr *Error
		if errors.As(err, &toolErr) {
			return Response{Code: toolErr.Code, Message: toolErr.Message, Hint: toolErr.Hint, Data: toolErr.Data}
		}
		return Response{Code: "INTERNAL", Message: "tool request failed"}
	}
	return Response{OK: true, Code: "OK", Data: data, Images: images}
}

// invoke 依次处理本地工具（wait）、组合工具（截屏/点击/按键……）和后端调用。
// wait 必须在 backend 判空之前处理：它不需要任何后端。
func (c *Core) invoke(ctx context.Context, name string, input json.RawMessage) (any, []Image, error) {
	if name == Wait {
		data, err := c.wait(ctx, input)
		return data, nil, err
	}
	if c.backend == nil {
		return nil, nil, &Error{Code: "NOT_CONFIGURED", Message: "remote platform backend is not configured"}
	}
	if data, images, handled, err := c.callDesktopTool(ctx, name, input); handled {
		return data, images, err
	}
	data, err := c.backend.Invoke(ctx, Request{Name: name, Input: append(json.RawMessage(nil), input...)})
	return data, nil, err
}

// Error 表示可安全传递给工具调用方的结构化业务错误。
type Error struct {
	Code    string
	Message string
	Hint    string
	// Data 用于错误也需要现场信息的场景（例如 DEVICE_NOT_FOUND 附带在线设备）。
	Data any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

var responseSchema = json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"},"code":{"type":"string"},"message":{"type":"string"},"hint":{"type":"string"},"data":{}},"required":["ok","code"],"additionalProperties":false}`)

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
	tool(AskUser, "Ask the device owner one question in an approved session. Shows up to six choices and always allows a custom text answer. Returns a request_id immediately; use get_user_reply later, without busy-polling. Do not repeat a pending question or perform actions that depend on its answer. A reply does not grant extra permissions. Never request passwords, verification codes or private keys.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"question":{"type":"string","minLength":1,"maxLength":1000},"options":{"type":"array","maxItems":6,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":100}},"timeout_seconds":{"type":"integer","minimum":15,"maximum":300}},"required":["session_id","question"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(GetUserReply, "Read the status and reply to ask_user. Returns pending, answered, cancelled, timed_out or result_unknown; only answered is an explicit user response and none of the other states implies consent. Set wait_seconds (0-25) to poll internally; a timeout returns ok with timed_out=true. Do not automatically reissue questions after a disconnect.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128},"wait_seconds":{"type":"integer","minimum":0,"maximum":25}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(ListDevices, "List devices available to the current operator.", `{"type":"object","properties":{},"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(GetDeviceStatus, "Get online and session status for a device.", `{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128}},"required":["device_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(RequestConnection, "Request a remote session to one target device. Without temporary_password the device owner must approve; with the owner's current temporary password, the session connects directly without changing the target's mode setting. It returns immediately with status pending_approval and next=get_session_status; do not block here waiting for the owner, poll get_session_status with wait_seconds instead (wait_for_approval_seconds is capped at 25s). Approval grants the full set of session actions: run PowerShell, download remote files and upload local files. A pending session is not authorization to execute commands. When the owner approves, their current temporary password is returned as temporary_password: reuse it to create a new session after a disconnect without asking for approval again, until it expires or is rotated.", `{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128},"purpose":{"type":"string","minLength":1,"maxLength":512},"temporary_password":{"type":"string","minLength":32,"maxLength":256},"wait_for_approval_seconds":{"type":"integer","minimum":0,"maximum":25}},"required":["device_id","purpose"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(RunCommand, "One-shot remote task for a server-hosted agent: identify the device by device_id or device_name, optionally with its 32-character temporary password (without it the owner must approve), request a session, execute one PowerShell command, wait for the result, then close the session unless keep_session_open is true. Good for short commands; for long jobs use execute_command + get_command_result. Returns aggregated stdout, stderr, exit code, status and truncation flag.", `{"type":"object","properties":{"device_id":{"type":"string","minLength":1,"maxLength":128},"temporary_password":{"type":"string","minLength":32,"maxLength":256},"command":{"type":"string","minLength":1,"maxLength":32768},"purpose":{"type":"string","maxLength":512},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600},"wait_for_approval_seconds":{"type":"integer","minimum":0,"maximum":25},"keep_session_open":{"type":"boolean"}},"required":["device_id","temporary_password","command"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(GetSessionStatus, "Get the current status of a connection request or session. Set wait_seconds (0-25) to poll internally instead of calling again in a loop; a timeout returns ok with timed_out=true.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"wait_seconds":{"type":"integer","minimum":0,"maximum":25}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(ExecuteCommand, "Execute one PowerShell command in an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"command":{"type":"string","minLength":1,"maxLength":32768},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600}},"required":["session_id","command"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(GetCommandResult, "Read output and final status for a command task.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128},"cursor":{"type":"integer","minimum":0}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(DownloadFile, "Download one remote file from an approved session. The file is written locally and the result returns its local_path; files larger than the configured limit are rejected.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"remote_path":{"type":"string","minLength":1,"maxLength":4096},"destination_path":{"type":"string","maxLength":4096}},"required":["session_id","remote_path"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(UploadFile, "Upload one local file to the target device in an approved session. Files are limited to 32 MiB; the device refuses to replace an existing file unless overwrite is true. remote_path may be a full path, or empty to let the device store the file in its uploads directory; the result returns the final remote_path, size and SHA-256.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"local_path":{"type":"string","minLength":1,"maxLength":4096},"remote_path":{"type":"string","maxLength":4096},"overwrite":{"type":"boolean"}},"required":["session_id","local_path"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(CancelCommand, "Cancel a running command in an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"request_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(CloseSession, "Close a remote session and revoke its authorization.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(TakeScreenshot, "Capture the whole desktop (all monitors) of the device in an approved session and return it as an image. The image is scaled down to max_width pixels wide (default 1568). Coordinates for click are pixels in the most recent screenshot of the session, so take a new screenshot whenever the screen may have changed. Fails while the device is locked or showing a UAC prompt.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"max_width":{"type":"integer","minimum":320,"maximum":7680},"format":{"type":"string","enum":["png","jpeg"]}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, OpenWorld: boolPtr(false)}),
	tool(Click, "Move the mouse and click on the device in an approved session. x and y are pixel coordinates in the most recent take_screenshot image of this session (call take_screenshot first). button defaults to left; set double for a double-click. Clicks into elevated (administrator) windows are silently blocked by Windows.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"button":{"type":"string","enum":["left","right","middle"]},"double":{"type":"boolean"},"screenshot_id":{"type":"string","minLength":1,"maxLength":64},"detect_change":{"type":"boolean"},"settle_ms":{"type":"integer","minimum":0,"maximum":2000}},"required":["session_id","x","y"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(TypeText, "Type text into the focused control on the device in an approved session, e.g. a chat box: click the input first, then call this. Any language including Chinese and emoji is typed directly, independent of the input method. method \"type\" (default) sends characters one by one and types line breaks as Shift+Enter so chat apps do not send early; method \"paste\" pastes through the clipboard (better for long text) and restores the previous clipboard text afterwards. Set press_enter to press Enter once after the text, which sends the message in most chat apps. Input into elevated (administrator) windows is silently blocked by Windows.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"text":{"type":"string","minLength":1,"maxLength":4000},"method":{"type":"string","enum":["type","paste"]},"press_enter":{"type":"boolean"}},"required":["session_id","text"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}), tool(PressKeys, "Send one key chord such as ctrl+l, ctrl+shift+o, win+r or alt+f4 to the device in an approved session. Letters, digits, F1-F24 and common named keys are supported: up to three modifiers plus exactly one main key. Keys go to the foreground window: confirm the target with wait_for(window_title=...) first, and check foreground_title in the result. Unavailable while the device is locked or showing a UAC prompt.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"keys":{"type":"string","minLength":1,"maxLength":64},"repeat":{"type":"integer","minimum":1,"maximum":20},"interval_ms":{"type":"integer","minimum":0,"maximum":1000}},"required":["session_id","keys"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(MoveMouse, "Move the pointer without clicking, to reveal tooltips, hover menus or auto-hiding docks. x and y are pixels in the latest take_screenshot image of this session; call take_screenshot first. settle_ms waits on the device before returning so a tooltip has time to appear; foreground_title reports which window was frontmost.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"settle_ms":{"type":"integer","minimum":0,"maximum":3000},"screenshot_id":{"type":"string","minLength":1,"maxLength":64}},"required":["session_id","x","y"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(Wait, "Wait without touching the device, for hosts whose sandbox has no sleep. Maximum 25000 ms; returns early if the call is cancelled.", `{"type":"object","properties":{"ms":{"type":"integer","minimum":0,"maximum":25000}},"required":["ms"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(WaitFor, "Wait on the device until a condition becomes true: the frontmost window title contains value, a process is running, or a remote path exists. Returns matched, timed_out, elapsed_ms and evidence; a timeout is a normal result, not an error. Needs an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"condition":{"type":"string","enum":["window_title","process","path_exists"]},"value":{"type":"string","minLength":1,"maxLength":1024},"timeout_seconds":{"type":"integer","minimum":1,"maximum":25}},"required":["session_id","condition","value"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}), tool(ListDir, "List one directory on the device without running PowerShell yourself. Returns name, type, size and modified for each entry; links are reported as type symlink with target and are never followed. If path itself is a symlink the call fails with hint.target so you can switch to the real path.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"path":{"type":"string","minLength":1,"maxLength":4096},"limit":{"type":"integer","minimum":1,"maximum":1000}},"required":["session_id","path"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(StatPath, "Return type, size, mtime and link target for one remote path. Missing paths are FILE_NOT_FOUND instead of an exception.", `{"type":"object","properties":{"session_id":{"type":"string","minLength":1,"maxLength":128},"path":{"type":"string","minLength":1,"maxLength":4096}},"required":["session_id","path"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
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
		required = nil
	case RequestConnection:
		// 不提供 temporary_password 时走审批流：由设备所有者在客户端批准，因此它不是必填项。
		// 也不再要求 permissions：批准即授予全部会话能力。
		required = []string{"purpose"}
	case RunCommand:
		required = []string{"command"}
	case GetSessionStatus, CloseSession:
		required = []string{"session_id"}
	case ExecuteCommand:
		required = []string{"session_id", "command"}
	case AskUser:
		required = []string{"session_id", "question"}
	case GetCommandResult, CancelCommand, GetUserReply:
		required = []string{"session_id", "request_id"}
	case DownloadFile:
		required = []string{"session_id", "remote_path"}
	case UploadFile:
		required = []string{"session_id", "local_path"}
	case TakeScreenshot:
		required = []string{"session_id"}
	case Click:
		required = []string{"session_id", "x", "y"}
	case TypeText:
		required = []string{"session_id", "text"}
	case PressKeys:
		required = []string{"session_id", "keys"}
	case MoveMouse:
		required = []string{"session_id", "x", "y"}
	case Wait:
		required = []string{"ms"}
	case WaitFor:
		required = []string{"session_id", "condition", "value"}
	case ListDir:
		required = []string{"session_id", "path"}
	case StatPath:
		required = []string{"session_id", "path"}
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return fmt.Errorf("missing required field %q", key)
		}
	}
	if name == AskUser {
		if err := validateQuestionInput(fields); err != nil {
			return err
		}
	}
	var input struct {
		DeviceID         string `json:"device_id"`
		Purpose          string `json:"purpose"`
		TemporaryPass    string `json:"temporary_password"`
		SessionID        string `json:"session_id"`
		RequestID        string `json:"request_id"`
		Command          string `json:"command"`
		WorkingDirectory string `json:"working_directory"`
		TimeoutSeconds   *int   `json:"timeout_seconds"`
		Cursor           *int   `json:"cursor"`
		WaitForApproval  *int   `json:"wait_for_approval_seconds"`
		KeepSessionOpen  *bool  `json:"keep_session_open"`
		RemotePath       string `json:"remote_path"`
		DestinationPath  string `json:"destination_path"`
		LocalPath        string `json:"local_path"`
		Overwrite        *bool  `json:"overwrite"`
		MaxWidth         *int   `json:"max_width"`
		Format           string `json:"format"`
		X                *int   `json:"x"`
		Y                *int   `json:"y"`
		Button           string `json:"button"`
		Double           *bool  `json:"double"`
		Text             string `json:"text"`
		Method           string `json:"method"`
		PressEnter       *bool  `json:"press_enter"`
		Keys             string `json:"keys"`
		Repeat           *int   `json:"repeat"`
		IntervalMS       *int   `json:"interval_ms"`
		SettleMS         *int   `json:"settle_ms"`
		MS               *int   `json:"ms"`
		Condition        string `json:"condition"`
		Value            string `json:"value"`
		ScreenshotID     string `json:"screenshot_id"`
		DetectChange     *bool  `json:"detect_change"`
		WaitSeconds      *int   `json:"wait_seconds"`
		DeviceName       string `json:"device_name"`
		Path             string `json:"path"`
		Limit            *int   `json:"limit"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	for _, key := range []string{"device_id", "purpose", "session_id", "request_id", "command", "working_directory", "temporary_password", "remote_path", "destination_path", "local_path", "format", "button", "text", "method", "question", "keys", "condition", "value", "screenshot_id", "device_name", "path"} {
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
	if input.TimeoutSeconds != nil && (*input.TimeoutSeconds < 1 || *input.TimeoutSeconds > 3600) {
		return fmt.Errorf("timeout_seconds must be between 1 and 3600")
	}
	if input.Cursor != nil && *input.Cursor < 0 {
		return fmt.Errorf("cursor must not be negative")
	}
	if input.WaitForApproval != nil && (*input.WaitForApproval < 0 || *input.WaitForApproval > maxWaitForSeconds) {
		return fmt.Errorf("wait_for_approval_seconds must be between 0 and %d; poll get_session_status with wait_seconds for longer waits", maxWaitForSeconds)
	}
	if input.WaitSeconds != nil && (*input.WaitSeconds < 0 || *input.WaitSeconds > maxWaitForSeconds) {
		return fmt.Errorf("wait_seconds must be between 0 and %d", maxWaitForSeconds)
	}
	if len(input.ScreenshotID) > 64 {
		return fmt.Errorf("screenshot_id exceeds 64 characters")
	}
	if input.SettleMS != nil && name == Click && (*input.SettleMS > 2000) {
		return fmt.Errorf("settle_ms must be between 0 and 2000 for click")
	}
	if input.MaxWidth != nil && (*input.MaxWidth < 320 || *input.MaxWidth > 7680) {
		return fmt.Errorf("max_width must be between 320 and 7680")
	}
	if input.Format != "" && input.Format != "png" && input.Format != "jpeg" {
		return fmt.Errorf("format must be png or jpeg")
	}
	if (input.X != nil && *input.X < 0) || (input.Y != nil && *input.Y < 0) {
		return fmt.Errorf("x and y must not be negative")
	}
	if _, ok := mouseButtonFlags[input.Button]; input.Button != "" && !ok {
		return fmt.Errorf("button must be left, right or middle")
	}
	if utf8.RuneCountInString(input.Text) > maxTypeTextRunes {
		return fmt.Errorf("text exceeds %d characters", maxTypeTextRunes)
	}
	if input.Method != "" && input.Method != "type" && input.Method != "paste" {
		return fmt.Errorf("method must be type or paste")
	}
	if input.Repeat != nil && (*input.Repeat < 1 || *input.Repeat > 20) {
		return fmt.Errorf("repeat must be between 1 and 20")
	}
	if input.IntervalMS != nil && (*input.IntervalMS < 0 || *input.IntervalMS > 1000) {
		return fmt.Errorf("interval_ms must be between 0 and 1000")
	}
	if input.SettleMS != nil && (*input.SettleMS < 0 || *input.SettleMS > 3000) {
		return fmt.Errorf("settle_ms must be between 0 and 3000")
	}
	if input.MS != nil && (*input.MS < 0 || *input.MS > maxWaitMilliseconds) {
		return fmt.Errorf("ms must be between 0 and %d", maxWaitMilliseconds)
	}
	if len(input.Keys) > 64 {
		return fmt.Errorf("keys exceeds 64 characters")
	}
	if len(input.Value) > 1024 {
		return fmt.Errorf("value exceeds 1024 characters")
	}
	switch name {
	case GetDeviceStatus, RequestConnection, RunCommand:
		hasID := strings.TrimSpace(input.DeviceID) != ""
		hasName := strings.TrimSpace(input.DeviceName) != ""
		if hasID == hasName {
			return fmt.Errorf("provide exactly one of device_id or device_name")
		}
	}
	if input.Limit != nil && (*input.Limit < 1 || *input.Limit > 1000) {
		return fmt.Errorf("limit must be between 1 and 1000")
	}
	if len(input.Path) > 4096 {
		return fmt.Errorf("path exceeds 4096 characters")
	}
	if name == WaitFor {
		switch input.Condition {
		case "window_title", "process", "path_exists":
		default:
			return fmt.Errorf("condition must be window_title, process or path_exists")
		}
		if input.TimeoutSeconds != nil && (*input.TimeoutSeconds < 1 || *input.TimeoutSeconds > maxWaitForSeconds) {
			return fmt.Errorf("timeout_seconds must be between 1 and %d", maxWaitForSeconds)
		}
	}
	return nil
}

// schemaFields 按工具记录允许的字段，用于补充 JSON Schema 的未知字段检查。
var schemaFields = map[string]map[string]struct{}{
	AskUser:           fieldSet("session_id", "question", "options", "timeout_seconds"),
	GetUserReply:      fieldSet("session_id", "request_id", "wait_seconds"),
	ListDevices:       fieldSet(),
	GetDeviceStatus:   fieldSet("device_id", "device_name"),
	RequestConnection: fieldSet("device_id", "device_name", "purpose", "temporary_password", "wait_for_approval_seconds"),
	RunCommand:        fieldSet("device_id", "device_name", "temporary_password", "command", "purpose", "working_directory", "timeout_seconds", "wait_for_approval_seconds", "keep_session_open"),
	GetSessionStatus:  fieldSet("session_id", "wait_seconds"),
	ExecuteCommand:    fieldSet("session_id", "command", "working_directory", "timeout_seconds"),
	GetCommandResult:  fieldSet("session_id", "request_id", "cursor"),
	DownloadFile:      fieldSet("session_id", "remote_path", "destination_path"),
	UploadFile:        fieldSet("session_id", "local_path", "remote_path", "overwrite"),
	CancelCommand:     fieldSet("session_id", "request_id"),
	CloseSession:      fieldSet("session_id"),
	TakeScreenshot:    fieldSet("session_id", "max_width", "format"),
	Click:             fieldSet("session_id", "x", "y", "button", "double", "screenshot_id", "detect_change", "settle_ms"),
	TypeText:          fieldSet("session_id", "text", "method", "press_enter"),
	PressKeys:         fieldSet("session_id", "keys", "repeat", "interval_ms"),
	MoveMouse:         fieldSet("session_id", "x", "y", "settle_ms", "screenshot_id"),
	Wait:              fieldSet("ms"),
	WaitFor:           fieldSet("session_id", "condition", "value", "timeout_seconds"),
	ListDir:           fieldSet("session_id", "path", "limit"),
	StatPath:          fieldSet("session_id", "path"),
}

// fieldSet 将字段名切片转换为便于查询的集合。
func fieldSet(keys ...string) map[string]struct{} {
	fields := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		fields[key] = struct{}{}
	}
	return fields
}
