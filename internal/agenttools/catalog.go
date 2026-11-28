// Package agenttools 定义 Agent 可调用的工具目录、输入校验和统一后端接口。
//
// 目录本身（catalog.go）是唯一事实来源：工具名、JSON Schema、字段白名单都在这里；
// 组合工具（desktop*.go、file_ops.go、wait.go）用 execute_command 在设备上跑
// scripts.go 里的 PowerShell，不新增协议帧。
package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
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
	FindOnScreen      = "find_on_screen" // UIA 文本定位（P3）
	ClickTarget       = "click_target"   // 定位 + 点击一笔事务（P4）
	Drag              = "drag"           // 按住左键拖动（P4）
)

// Image 是工具返回给 Agent 的图片内容，不进入 JSON 结果。
type Image struct {
	MIMEType string
	Data     []byte
}

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

// invoke 按三层分发：本地工具（只睡，不碰设备和后端）、组合工具（在设备上跑脚本）、
// 以及必须由后端实现的平台操作。
//
// 顺序有意义：本地工具在 backend 判空之前，因为它根本不需要后端。
func (c *Core) invoke(ctx context.Context, name string, input json.RawMessage) (any, []Image, error) {
	if run, ok := localTools[name]; ok {
		data, err := run(ctx, c, input)
		return data, nil, err
	}
	if c.backend == nil {
		return nil, nil, &Error{Code: "NOT_CONFIGURED", Message: "remote platform backend is not configured"}
	}
	if run, ok := composedTools[name]; ok {
		return run(ctx, c, input)
	}
	data, err := c.backend.Invoke(ctx, Request{Name: name, Input: append(json.RawMessage(nil), input...)})
	return data, nil, err
}

// localTools 是不接触设备也不需要后端的工具。目前只有 wait：宿主的沙箱里没有 sleep。
var localTools = map[string]func(ctx context.Context, core *Core, raw json.RawMessage) (any, error){
	Wait: func(ctx context.Context, core *Core, raw json.RawMessage) (any, error) {
		return core.wait(ctx, raw)
	},
}

// backendTools 是必须由 Backend 实现的工具集合（组合工具与本地工具都不在内）。
// 导出它是为了让 platformclient 用同一条清单自检「有没有漏实现」。
func backendTools() []string {
	names := make([]string, 0, len(specByName))
	for name := range specByName {
		if _, ok := localTools[name]; ok {
			continue
		}
		if _, ok := composedTools[name]; ok {
			continue
		}
		names = append(names, name)
	}
	return names
}

// BackendTools 返回必须由 Backend 实现的工具名，供 backend 实现方自检。
func BackendTools() []string { return backendTools() }

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
	tool(AskUser, "Ask the device owner one question in an approved session. Shows up to six choices and always allows a custom text answer. Returns a request_id immediately; use get_user_reply later, without busy-polling. Do not repeat a pending question or perform actions that depend on its answer. A reply does not grant extra permissions. Never request passwords, verification codes or private keys.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"question":{"type":"string","pattern":"\\S","minLength":1,"maxLength":1000},"options":{"type":"array","maxItems":6,"uniqueItems":true,"items":{"type":"string","minLength":1,"maxLength":100}},"timeout_seconds":{"type":"integer","minimum":15,"maximum":300}},"required":["session_id","question"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(GetUserReply, "Read the status and reply to ask_user. Returns pending, answered, cancelled, timed_out or result_unknown; only answered is an explicit user response and none of the other states implies consent. Set wait_seconds (0-25) to poll internally; a timeout returns ok with timed_out=true. Do not automatically reissue questions after a disconnect.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"request_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"wait_seconds":{"type":"integer","minimum":0,"maximum":25}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(ListDevices, "List devices available to the current operator.", `{"type":"object","properties":{},"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(GetDeviceStatus, "Get online and session status for a device.", `{"type":"object","properties":{"device_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"device_name":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128}},"oneOf":[{"required":["device_id"]},{"required":["device_name"]}],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(RequestConnection, "Request a remote session to one target device. Without temporary_password the device owner must approve; with the owner's current temporary password, the session is approved at once. It returns immediately with status pending_approval and next=get_session_status; do not block here waiting for the owner, poll get_session_status with wait_seconds instead (wait_for_approval_seconds is capped at 25s). Approval grants the full set of session actions: run PowerShell, download remote files and upload local files. A pending session is not authorization to execute commands. When the owner approves, their current temporary password is returned as temporary_password: reuse it to create a new session after a disconnect without asking for approval again, until it expires or is rotated.", `{"type":"object","properties":{"device_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"device_name":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"purpose":{"type":"string","pattern":"\\S","minLength":1,"maxLength":512},"temporary_password":{"type":"string","minLength":32,"maxLength":256},"wait_for_approval_seconds":{"type":"integer","minimum":0,"maximum":25}},"required":["purpose"],"oneOf":[{"required":["device_id"]},{"required":["device_name"]}],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(RunCommand, "One-shot remote task for a server-hosted agent: identify the device by device_id or device_name, optionally with its 32-character temporary password (without it the owner must approve), request a session, execute one PowerShell command, wait for the result, then close the session unless keep_session_open is true. Good for short commands; for long jobs use execute_command + get_command_result. Returns aggregated stdout, stderr, exit code, status and truncation flag.", `{"type":"object","properties":{"device_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"device_name":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"temporary_password":{"type":"string","minLength":32,"maxLength":256},"command":{"type":"string","pattern":"\\S","minLength":1,"maxLength":32768},"purpose":{"type":"string","pattern":"\\S","maxLength":512},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600},"wait_for_approval_seconds":{"type":"integer","minimum":0,"maximum":25},"keep_session_open":{"type":"boolean"}},"required":["command"],"oneOf":[{"required":["device_id"]},{"required":["device_name"]}],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(GetSessionStatus, "Get the current status of a connection request or session. Set wait_seconds (0-25) to poll internally instead of calling again in a loop; a timeout returns ok with timed_out=true.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"wait_seconds":{"type":"integer","minimum":0,"maximum":25}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(ExecuteCommand, "Execute one PowerShell command in an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"command":{"type":"string","pattern":"\\S","minLength":1,"maxLength":32768},"working_directory":{"type":"string","maxLength":4096},"timeout_seconds":{"type":"integer","minimum":1,"maximum":3600}},"required":["session_id","command"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(GetCommandResult, "Read output and final status for a command task.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"request_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"cursor":{"type":"integer","minimum":0}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(DownloadFile, "Download one remote file from an approved session. The file is written locally and the result returns its local_path; files larger than the configured limit are rejected.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"remote_path":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4096},"destination_path":{"type":"string","pattern":"\\S","maxLength":4096}},"required":["session_id","remote_path"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(UploadFile, "Upload one local file to the target device in an approved session. Files are limited to 32 MiB; the device refuses to replace an existing file unless overwrite is true. remote_path may be a full path, or empty to let the device store the file in its uploads directory; the result returns the final remote_path, size and SHA-256.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"local_path":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4096},"remote_path":{"type":"string","pattern":"\\S","maxLength":4096},"overwrite":{"type":"boolean"}},"required":["session_id","local_path"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(CancelCommand, "Cancel a running command in an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"request_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128}},"required":["session_id","request_id"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(CloseSession, "Close a remote session and revoke its authorization.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(TakeScreenshot, "Capture the whole desktop (all monitors) of the device in an approved session and return it as an image. The image is scaled down to max_width pixels wide (default 1568). Coordinates for click are pixels in the most recent screenshot of the session, so take a new screenshot whenever the screen may have changed. Fails while the device is locked or showing a UAC prompt.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"max_width":{"type":"integer","minimum":320,"maximum":7680},"format":{"type":"string","enum":["png","jpeg"]}},"required":["session_id"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, OpenWorld: boolPtr(false)}),
	tool(FindOnScreen, "Find a text label (UI Automation) or an image template (OpenCV gray matchTemplate) on the device screen and return matches in the coordinates of the latest screenshot of this session. Give exactly one of text, image_path or image_b64; image_path is a path on the device (upload the template first) and image_b64 is a PNG/JPEG no larger than 64 KiB decoded. Coordinates are screenshot pixels: pass the returned screenshot_id and a match center to click or drag. Narrow ambiguous results with region, control_type, match=exact or a higher threshold. source selects the locator: auto (default, UI Automation first and OCR when it finds nothing), uia or ocr; OCR matches report source=ocr and need an installed OCR language pack (otherwise auto just returns the empty UI Automation result with ocr_note, while source=ocr fails with OCR_UNAVAILABLE). To wait for something to appear, set wait_until_found=true and timeout_seconds (1-25, default 10): it re-searches every 500ms and a timeout returns the empty result with timed_out=true instead of an error.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"text":{"type":"string","pattern":"\\S","minLength":1,"maxLength":200},"image_path":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4096},"image_b64":{"type":"string","pattern":"\\S","minLength":1,"maxLength":200000},"threshold":{"type":"number","minimum":0.1,"maximum":0.999},"match":{"type":"string","enum":["contains","exact"]},"control_type":{"type":"string","pattern":"\\S","minLength":1,"maxLength":32},"region":{"type":"array","minItems":4,"maxItems":4,"items":{"type":"integer","minimum":0}},"limit":{"type":"integer","minimum":1,"maximum":20},"wait_until_found":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":25},"source":{"type":"string","enum":["auto","uia","ocr"]}},"anyOf":[{"required":["text"]},{"required":["image_path"]},{"required":["image_b64"]}],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(ClickTarget, "Locate a text label (UI Automation) or an image template (OpenCV) and click it in one device-side transaction: the frame used for locating is the coordinate base for the click and no other session can capture or move the mouse in between. Give exactly one of text, image_path or image_b64 (same semantics as find_on_screen: image_b64 is a PNG/JPEG no larger than 64 KiB decoded, image_path is a path on the device). pick defaults to unique: when two candidates sit at different positions with scores within 0.03 the device clicks nothing and returns AMBIGUOUS_TARGET with the candidates, so choose pick=best or pick=index:N or narrow the search with region/control_type. No match returns NOT_FOUND with the scores and threshold; neither case sends a mouse-down. detect_change defaults to true and settle_ms to 250. Clicks into elevated (administrator) windows are silently blocked by Windows. source works like find_on_screen: auto (UI Automation, then OCR on the same captured frame), uia, or ocr (OCR_UNAVAILABLE when no language pack is installed). Set wait_until_found=true with timeout_seconds (1-20) to wait for the target to appear: the device re-searches every 500ms, releasing the device lock between rounds, and only clicks once something is found (a timeout is NOT_FOUND with timed_out=true). If the target is a captcha or human-verification widget, stop and use ask_user instead.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"text":{"type":"string","pattern":"\\S","minLength":1,"maxLength":200},"image_path":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4096},"image_b64":{"type":"string","pattern":"\\S","minLength":1,"maxLength":200000},"threshold":{"type":"number","minimum":0.1,"maximum":0.999},"match":{"type":"string","enum":["contains","exact"]},"control_type":{"type":"string","pattern":"\\S","minLength":1,"maxLength":32},"region":{"type":"array","minItems":4,"maxItems":4,"items":{"type":"integer","minimum":0}},"limit":{"type":"integer","minimum":1,"maximum":20},"pick":{"type":"string","pattern":"^(unique|best|first|index:[1-9][0-9]*)$"},"button":{"type":"string","enum":["left","right","middle"]},"double":{"type":"boolean"},"detect_change":{"type":"boolean"},"settle_ms":{"type":"integer","minimum":0,"maximum":2000},"wait_until_found":{"type":"boolean"},"timeout_seconds":{"type":"integer","minimum":1,"maximum":20},"source":{"type":"string","enum":["auto","uia","ocr"]}},"anyOf":[{"required":["text"]},{"required":["image_path"]},{"required":["image_b64"]}],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(Click, "Move the mouse and click on the device in an approved session. x and y are pixel coordinates in the most recent take_screenshot image of this session (call take_screenshot first). button defaults to left; set double for a double-click. Clicks into elevated (administrator) windows are silently blocked by Windows.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"button":{"type":"string","enum":["left","right","middle"]},"double":{"type":"boolean"},"screenshot_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":64},"detect_change":{"type":"boolean"},"settle_ms":{"type":"integer","minimum":0,"maximum":2000}},"required":["session_id","x","y"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(TypeText, "Type text into the focused control on the device in an approved session, e.g. a chat box: click the input first, then call this. Any language including Chinese and emoji is typed directly, independent of the input method. method \"type\" (default) sends characters one by one and types line breaks as Shift+Enter so chat apps do not send early; method \"paste\" pastes through the clipboard (better for long text) and restores the previous clipboard text afterwards. Set press_enter to press Enter once after the text, which sends the message in most chat apps. Input into elevated (administrator) windows is silently blocked by Windows.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"text":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4000},"method":{"type":"string","enum":["type","paste"]},"press_enter":{"type":"boolean"}},"required":["session_id","text"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}), tool(PressKeys, "Send one key chord such as ctrl+l, ctrl+shift+o, win+r or alt+f4 to the device in an approved session. Letters, digits, F1-F24 and common named keys are supported: up to three modifiers plus exactly one main key. Keys go to the foreground window: confirm the target with wait_for(window_title=...) first, and check foreground_title in the result. Unavailable while the device is locked or showing a UAC prompt.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"keys":{"type":"string","pattern":"\\S","minLength":1,"maxLength":64},"repeat":{"type":"integer","minimum":1,"maximum":20},"interval_ms":{"type":"integer","minimum":0,"maximum":1000}},"required":["session_id","keys"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(MoveMouse, "Move the pointer without clicking, to reveal tooltips, hover menus or auto-hiding docks. x and y are pixels in the latest take_screenshot image of this session; call take_screenshot first. duration_ms > 0 moves along a deterministic eased path (needed by hover menus that require intermediate mousemove); duration_ms=0 jumps. settle_ms waits on the device before returning so a tooltip has time to appear; foreground_title reports which window was frontmost.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"x":{"type":"integer","minimum":0},"y":{"type":"integer","minimum":0},"settle_ms":{"type":"integer","minimum":0,"maximum":3000},"duration_ms":{"type":"integer","minimum":0,"maximum":2000},"screenshot_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":64}},"required":["session_id","x","y"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(false), OpenWorld: boolPtr(false)}),
	tool(Drag, "Press the left button, move along a deterministic eased path and release, all in one transaction: use it for window moving, sliders, progress bars and drag-and-drop reordering. from and to are [x,y] pixels in the latest take_screenshot image; the button is always released before returning (released=true). Prefer small hold_ms and a duration long enough for the target to follow the pointer. Input into elevated (administrator) windows is silently blocked by Windows, so elevated windows such as Task Manager cannot be dragged from a normal session. If the target is a captcha or human-verification widget, stop and use ask_user instead.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"from":{"type":"array","minItems":2,"maxItems":2,"items":{"type":"integer","minimum":0}},"to":{"type":"array","minItems":2,"maxItems":2,"items":{"type":"integer","minimum":0}},"duration_ms":{"type":"integer","minimum":50,"maximum":5000},"hold_ms":{"type":"integer","minimum":0,"maximum":1000},"screenshot_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":64}},"required":["session_id","from","to"],"additionalProperties":false}`, ToolAnnotations{Destructive: boolPtr(true), OpenWorld: boolPtr(false)}),
	tool(Wait, "Wait without touching the device, for hosts whose sandbox has no sleep. Maximum 25000 ms; returns early if the call is cancelled.", `{"type":"object","properties":{"ms":{"type":"integer","minimum":0,"maximum":25000}},"required":["ms"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(WaitFor, "Wait on the device until a condition becomes true: the frontmost window title contains value, a process is running, or a remote path exists. Returns matched, timed_out, elapsed_ms and evidence; a timeout is a normal result, not an error. Needs an approved session.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"condition":{"type":"string","enum":["window_title","process","path_exists"]},"value":{"type":"string","pattern":"\\S","minLength":1,"maxLength":1024},"timeout_seconds":{"type":"integer","minimum":1,"maximum":25}},"required":["session_id","condition","value"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}), tool(ListDir, "List one directory on the device without running PowerShell yourself. Returns name, type, size and modified for each entry; links are reported as type symlink with target and are never followed. If path itself is a symlink the call fails with hint.target so you can switch to the real path.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"path":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4096},"limit":{"type":"integer","minimum":1,"maximum":1000}},"required":["session_id","path"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
	tool(StatPath, "Return type, size, mtime and link target for one remote path. Missing paths are FILE_NOT_FOUND instead of an exception.", `{"type":"object","properties":{"session_id":{"type":"string","pattern":"\\S","minLength":1,"maxLength":128},"path":{"type":"string","pattern":"\\S","minLength":1,"maxLength":4096}},"required":["session_id","path"],"additionalProperties":false}`, ToolAnnotations{ReadOnly: true, Idempotent: true, OpenWorld: boolPtr(false)}),
}

// 名称->工具定义 映射
var specByName = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, len(toolSpecs))
	for _, spec := range toolSpecs {
		m[spec.Name] = spec
	}
	return m
}()

// compiledSchemas 是把 toolSpecs 里的 InputSchema 编译成的校验器。
//
// 编译失败意味着发布出去的契约本身有问题，所以直接 panic：这是一个写了就错的
// 常量（由 TestToolCatalog 在测试阶段先探到），不应该等到运行到某个工具才报错。
var compiledSchemas = func() map[string]*jsonschema.Schema {
	compiler := jsonschema.NewCompiler()
	compiled := make(map[string]*jsonschema.Schema, len(toolSpecs))
	for _, spec := range toolSpecs {
		document, err := jsonschema.UnmarshalJSON(strings.NewReader(string(spec.InputSchema)))
		if err != nil {
			panic(fmt.Sprintf("agenttools: %s input schema is not valid JSON: %v", spec.Name, err))
		}
		location := "tool://" + spec.Name + "/input.json"
		if err := compiler.AddResource(location, document); err != nil {
			panic(fmt.Sprintf("agenttools: %s input schema cannot be registered: %v", spec.Name, err))
		}
		schema, err := compiler.Compile(location)
		if err != nil {
			panic(fmt.Sprintf("agenttools: %s input schema cannot be compiled: %v", spec.Name, err))
		}
		compiled[spec.Name] = schema
	}
	return compiled
}()

// validateInput 在调用后端前执行校验。
//
// 约束只有一份：toolSpecs 里的 InputSchema —— 宿主看到的也是它。这里不再重复写
// "必填、长度、区间、枚举、未知字段"，因为两份拷贝必然会漂移（历史上就漂移过：
// schema 说 device_id 必填，校验器却允许 device_name）。
func validateInput(raw json.RawMessage, name string) error {
	schema, ok := compiledSchemas[name]
	if !ok {
		return fmt.Errorf("tool %q has no input schema", name)
	}
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return fmt.Errorf("arguments must be a JSON object")
	}
	document, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return fmt.Errorf("arguments must be a JSON object")
	}
	if _, isObject := document.(map[string]any); !isObject {
		return fmt.Errorf("arguments must be a JSON object")
	}
	if err := schema.Validate(document); err != nil {
		return schemaError(err)
	}
	// 只有这一条规则 schema 表达不了：选项去掉首尾空白后仍不得为空或重复。
	if name == AskUser {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return fmt.Errorf("arguments must be a JSON object")
		}
		return validateQuestionInput(fields)
	}
	return nil
}

// schemaError 把校验器给的多行报告压成一行，供 Agent 和日志直接读。
func schemaError(err error) error {
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return err
	}
	lines := strings.Split(err.Error(), "\n")
	details := make([]string, 0, len(lines))
	for _, line := range lines[1:] { // 首行是 schema 位置，对调用方没有意义
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if trimmed == "" {
			continue
		}
		details = append(details, strings.ReplaceAll(trimmed, "at ''", "arguments"))
	}
	if len(details) == 0 {
		return fmt.Errorf("arguments do not match the tool schema")
	}
	return fmt.Errorf("%s", strings.Join(details, "; "))
}
