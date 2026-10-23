// Package protocol implements the versioned, length-delimited platform
// protocol. Control messages are JSON objects framed by a 4-byte big-endian
// length. JSON was chosen for the first platform version so the Windows Qt
// client and Go backend can share a small, explicit schema without generated
// code; the envelope is versioned so a binary codec can be added later.
package protocol

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version is the only protocol version accepted by this build.
const Version = 1

// Business types are explicit so a future business can never share a session,
// permission set or channel with agent-assist traffic.
const (
	BusinessAgentAssist = "agent_assist"
)

// 信息类型
const (
	TypeAuthenticatePlayer = "authenticate_player"
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

// Session status values. They are mirrored in internal/platform/session.
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

// Task status values. They are mirrored in internal/platform/task.
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

// Modes for temporary-password authorization.
// 临时密码授权模式
const (
	ModeDirect   = "direct"
	ModeApproval = "approval"
)

// DefaultMaxFrameBytes bounds every control frame. Command output is chunked
// well below this value by the device client.
// 默认最大帧字节数限制每个控制帧。设备客户端将命令输出分块，远低于此值。
const DefaultMaxFrameBytes = 256 << 10

// ErrFrameTooLarge is returned when a peer sends an oversized frame.
var ErrFrameTooLarge = errors.New("protocol frame exceeds configured maximum")

// Message is the common envelope for every platform message. Fields are only
// populated for the message types that need them. Password fields are only
// valid on authentication/device-registration messages and are never logged.
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
	// Overwrite lets an upload replace an existing remote file. The device
	// refuses to replace files unless this is explicitly set.
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

// DeviceInfo is the non-secret device view returned by the server.
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

// SessionInfo is the non-secret session view returned by the server.
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

// OutputChunk is one bounded stdout/stderr chunk.
type OutputChunk struct {
	Stream   string `json:"stream"`
	Sequence uint64 `json:"sequence"`
	Data     string `json:"data"`
}

// Codec reads and writes framed messages with a configurable maximum size.
type Codec struct {
	MaxFrameBytes int
}

// NewCodec returns a codec with the given frame limit, falling back to the
// package default when the limit is not positive.
// NewCodec 返回具有给定帧限制的编解码器，当限制不是正数时，回退到包默认值。
func NewCodec(max int) Codec {
	if max <= 0 {
		max = DefaultMaxFrameBytes
	}
	return Codec{MaxFrameBytes: max}
}

// Read consumes one framed message.
func (c Codec) Read(r *bufio.Reader) (Message, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Message{}, err
	}
	length := binary.BigEndian.Uint32(header[:])
	max := c.MaxFrameBytes
	if max <= 0 {
		max = DefaultMaxFrameBytes
	}
	if length == 0 {
		return Message{}, errors.New("empty protocol frame")
	}
	if int(length) > max {
		return Message{}, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, length, max)
	}
	data := make([]byte, int(length))
	if _, err := io.ReadFull(r, data); err != nil {
		return Message{}, err
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Message{}, fmt.Errorf("decode protocol frame: %w", err)
	}
	if m.Version != Version {
		return Message{}, fmt.Errorf("unsupported protocol version %d", m.Version)
	}
	if m.Type == "" {
		return Message{}, errors.New("missing message type")
	}
	return m, nil
}

// Write emits one framed message.
func (c Codec) Write(w io.Writer, m Message) error {
	if m.Version == 0 {
		m.Version = Version
	}
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode protocol message: %w", err)
	}
	max := c.MaxFrameBytes
	if max <= 0 {
		max = DefaultMaxFrameBytes
	}
	if len(data) > max {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, len(data), max)
	}
	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)
	for len(frame) > 0 {
		n, err := w.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}

// ErrorMessage builds a protocol error response.
func ErrorMessage(code, message string) Message {
	return Message{Version: Version, Type: TypeError, Code: code, Message: message}
}
