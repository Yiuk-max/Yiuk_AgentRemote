//通过 WebSocket 传输的版本化 JSON 消息

package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// 协议版本
const Version = 1

// 业务类型是明确的，因此未来的业务永远不能与代理辅助流量共享会话、权限集或频道。
const (
	BusinessAgentAssist = "agent_assist"
)

// 信息类型
const (
	TypeAuthenticatePlayer = "authenticate_player" //连接平台
	TypeAuthenticated      = "authenticated"

	TypeRegisterDevice           = "register_device"
	TypeDeviceRegistered         = "device_registered"
	TypeDeviceHeartbeat          = "device_heartbeat"
	TypeHeartbeatAck             = "heartbeat_ack"
	TypeUpdateTemporaryPassword  = "update_temporary_password"
	TypeTemporaryPasswordUpdated = "temporary_password_updated"

	TypeRequestSession     = "request_session"
	TypeSessionRequest     = "session_request"
	TypeSessionCreated     = "session_created"
	TypeSessionStatus      = "session_status"
	TypeApproveSession     = "approve_session"
	TypeRejectSession      = "reject_session"
	TypeOpenSessionChannel = "open_session_channel"
	TypeCloseSession       = "close_session"

	TypeExecuteCommand   = "execute_command"
	TypeCommandAccepted  = "command_accepted"
	TypeCommandOutput    = "command_output"
	TypeCommandResult    = "command_result"
	TypeGetCommandResult = "get_command_result"
	TypeCancelCommand    = "cancel_command"
	TypeCommandCanceled  = "command_cancelled"

	TypeDownloadFile       = "download_file"
	TypeCancelFileDownload = "cancel_file_download"
	TypeFileChunk          = "file_chunk"
	TypeFileDownloadResult = "file_download_result"

	TypeUploadFile       = "upload_file"
	TypeCancelFileUpload = "cancel_file_upload"
	TypeFileUploadChunk  = "file_upload_chunk"
	TypeFileUploadResult = "file_upload_result"

	TypeListDevices = "list_devices"
	TypeDeviceList  = "device_list"

	TypePing  = "ping"
	TypePong  = "pong"
	TypeError = "error"
)

// 连接状态值，它们在 internal/platform/session 中被镜像。
const (
	StatusRequested       = "requested"
	StatusPendingApproval = "pending_approval"
	StatusApproved        = "approved"
	StatusActive          = "active"
	StatusClosed          = "closed"
	StatusRejected        = "rejected"
	StatusExpired         = "expired"
	StatusInterrupted     = "interrupted"
)

// 任务状态值，它们在 internal/platform/task 中被镜像。
const (
	TaskQueued        = "queued"
	TaskRunning       = "running"
	TaskSucceeded     = "succeeded"
	TaskFailed        = "failed"
	TaskTimedOut      = "timed_out"
	TaskCancelled     = "cancelled"
	TaskResultUnknown = "result_unknown"
)

// 临时密码授权模式
const (
	ModeDirect   = "direct"
	ModeApproval = "approval"
)

// 默认最大消息体字节数
const DefaultMaxMessageBytes = 256 << 10

var ErrMessageTooLarge = errors.New("protocol message exceeds configured maximum")

// Message 是每个平台消息的通用信封。字段仅在需要它们的消息类型中填充。密码字段仅在身份验证/设备注册消息中有效，且从不记录。
type Message struct {
	Version      int    `json:"version"`
	BusinessType string `json:"business_type,omitempty"`
	Type         string `json:"type"`
	RequestID    string `json:"request_id,omitempty"`
	// CorrelationID echoes a caller-generated value on the direct response to
	// a request. Asynchronous events leave it empty.
	CorrelationID string `json:"correlation_id,omitempty"`
	ConnectionID  string `json:"connection_id,omitempty"`
	DeviceID      string `json:"device_id,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	Sequence      uint64 `json:"sequence,omitempty"`

	IdentityType   string `json:"identity_type,omitempty"`
	PlayerPassword string `json:"player_password,omitempty"`

	DeviceName        string `json:"device_name,omitempty"`
	Platform          string `json:"platform,omitempty"`
	TemporaryPassword string `json:"temporary_password,omitempty"`
	PasswordVersion   int    `json:"password_version,omitempty"`
	Mode              string `json:"mode,omitempty"`
	ExpiresAt         int64  `json:"expires_at,omitempty"`

	Purpose     string   `json:"purpose,omitempty"`
	Permissions []string `json:"permissions,omitempty"`

	Command          string `json:"command,omitempty"`
	WorkingDirectory string `json:"working_directory,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
	Cursor           int    `json:"cursor,omitempty"`

	RemotePath string `json:"remote_path,omitempty"`
	FileName   string `json:"file_name,omitempty"`
	FileSize   int64  `json:"file_size,omitempty"`
	FileSHA256 string `json:"file_sha256,omitempty"`
	// 允许上传文件覆盖现有的远程文件。设备不直接替换文件，需要告知明确要覆盖
	Overwrite bool `json:"overwrite,omitempty"`

	Stream      string `json:"stream,omitempty"`
	Data        string `json:"data,omitempty"`
	ExitCode    *int   `json:"exit_code,omitempty"`
	Truncated   bool   `json:"truncated,omitempty"`
	ResultKnown bool   `json:"result_known,omitempty"`
	Status      string `json:"status,omitempty"`
	EOF         bool   `json:"eof,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`

	ServerTime int64 `json:"server_time,omitempty"`

	Device   *DeviceInfo   `json:"device,omitempty"`
	Devices  []DeviceInfo  `json:"devices,omitempty"`
	Session  *SessionInfo  `json:"session,omitempty"`
	Sessions []SessionInfo `json:"sessions,omitempty"`

	Chunks      []OutputChunk `json:"chunks,omitempty"`
	NextCursor  int           `json:"next_cursor,omitempty"`
	OutputBytes int           `json:"output_bytes,omitempty"`

	ActiveSessions int `json:"active_sessions,omitempty"`
}

// DeviceInfo 不用加密保密
type DeviceInfo struct {
	DeviceID          string `json:"device_id"`
	Name              string `json:"name,omitempty"`
	Platform          string `json:"platform,omitempty"`
	Mode              string `json:"mode,omitempty"`
	Online            bool   `json:"online"`
	PasswordVersion   int    `json:"password_version,omitempty"`
	PasswordExpiresAt int64  `json:"password_expires_at,omitempty"`
	LastHeartbeat     int64  `json:"last_heartbeat,omitempty"`
	ActiveSessions    int    `json:"active_sessions,omitempty"`
}

// SessionInfo 不用加密保密
type SessionInfo struct {
	SessionID      string   `json:"session_id"`
	DeviceID       string   `json:"device_id"`
	Operator       string   `json:"operator,omitempty"`
	OperatorDevice string   `json:"operator_device,omitempty"`
	Purpose        string   `json:"purpose,omitempty"`
	Permissions    []string `json:"permissions,omitempty"`
	Mode           string   `json:"mode,omitempty"`
	Status         string   `json:"status"`
	CreatedAt      int64    `json:"created_at,omitempty"`
	ExpiresAt      int64    `json:"expires_at,omitempty"`
	ApprovedAt     int64    `json:"approved_at,omitempty"`
	ClosedAt       int64    `json:"closed_at,omitempty"`
	CloseReason    string   `json:"close_reason,omitempty"`
}

// OutputChunk 是一个有限的 stdout/stderr 块
type OutputChunk struct {
	Stream   string `json:"stream"`
	Sequence uint64 `json:"sequence"`
	Data     string `json:"data"`
}

// Codec 验证 JSON WebSocket 消息的大小和封装字段
type Codec struct{ MaxMessageBytes int }

func NewCodec(max int) Codec {
	if max <= 0 {
		max = DefaultMaxMessageBytes
	}
	return Codec{MaxMessageBytes: max}
}

func (c Codec) Encode(m Message) ([]byte, error) {
	if m.Version == 0 {
		m.Version = Version
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode protocol message: %w", err)
	}
	if len(data) > c.maxMessageBytes() {
		return nil, fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, len(data), c.maxMessageBytes())
	}
	return data, nil
}

func (c Codec) Decode(data []byte) (Message, error) {
	if len(data) == 0 {
		return Message{}, errors.New("empty protocol message")
	}
	if len(data) > c.maxMessageBytes() {
		return Message{}, fmt.Errorf("%w: %d > %d", ErrMessageTooLarge, len(data), c.maxMessageBytes())
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Message{}, fmt.Errorf("decode protocol message: %w", err)
	}
	if m.Version != Version {
		return Message{}, fmt.Errorf("unsupported protocol version %d", m.Version)
	}
	if m.Type == "" {
		return Message{}, errors.New("missing message type")
	}
	return m, nil
}

func (c Codec) maxMessageBytes() int {
	if c.MaxMessageBytes <= 0 {
		return DefaultMaxMessageBytes
	}
	return c.MaxMessageBytes
}

// ErrorMessage 构建一个协议错误响应
func ErrorMessage(code, message string) Message {
	return Message{Version: Version, Type: TypeError, Code: code, Message: message}
}
