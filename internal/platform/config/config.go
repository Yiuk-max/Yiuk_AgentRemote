// config 包定义了内存中平台服务器的配置
// 平台服务器使用独立配置节，便于按部署隔离玩家密码、端口和限制。
// YAML → 结构体 + ApplyDefaults/Validate + 派生 TTL 方法
package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const examplePassword = "REPLACE_WITH_A_LONG_RANDOM_PLAYER_SECRET"

// MinPlayerPasswordLen 是 player_password 的最小长度
// validation.允许短于推荐长度的密码，但在启动时记录为警告
const (
	MinPlayerPasswordLen         = 8
	RecommendedPlayerPasswordLen = 16
)

// 服务器配置.所有字段都映射到 YAML 文件的 `platform:` 部分
type Config struct {
	Listen string `yaml:"listen"` // WebSocket 监听地址与端口

	TLSCert string `yaml:"tls_cert"` //证书文件
	TLSKey  string `yaml:"tls_key"`  //私钥文件
	// 是否允许明文传输TCP.测试可以明文方便调试。生产环境应使用TLS。
	AllowInsecure bool `yaml:"allow_insecure"`

	PlayerPassword string `yaml:"player_password"`

	MaxConnections       int `yaml:"max_connections"`         // 服务器最大连接数
	MaxDevices           int `yaml:"max_devices"`             // 服务器最大设备数
	MaxSessions          int `yaml:"max_sessions"`            // 服务器最大会话数
	MaxSessionsPerDevice int `yaml:"max_sessions_per_device"` // 每个设备最大会话数
	MaxTasksPerSession   int `yaml:"max_tasks_per_session"`   // 每个会话最大任务数

	SessionTimeoutSec     int    `yaml:"session_timeout_sec"`        // 会话超时秒数
	ApprovalTimeoutSec    int    `yaml:"approval_timeout_sec"`       // 会话审批超时秒数
	TempPasswordTTLSec    int    `yaml:"temp_password_ttl_sec"`      // 临时密码生存时间（秒）
	HeartbeatTimeoutSec   int    `yaml:"heartbeat_timeout_sec"`      // 心跳超时秒数
	HandshakeTimeoutSec   int    `yaml:"handshake_timeout_sec"`      // 握手超时秒数
	MaxOutputBytes        int    `yaml:"max_output_bytes"`           // 最大输出字节数
	MaxDownloadBytes      int    `yaml:"max_download_bytes"`         // 最大下载字节数
	MaxUploadBytes        int    `yaml:"max_upload_bytes"`           // 最大上传字节数
	MaxFrameBytes         int    `yaml:"max_frame_bytes"`            // 单条 WebSocket JSON 消息最大字节数
	AuthRatePerMin        int    `yaml:"auth_rate_per_min"`          // 认证频率（每分钟），ip限流用
	TemporaryPasswordMin  int    `yaml:"temporary_password_min_sec"` // 临时密码最小生存时间（秒）
	TemporaryPasswordMax  int    `yaml:"temporary_password_max_sec"` // 临时密码最大生存时间（秒）
	OfflineDeviceKeepSec  int    `yaml:"offline_device_keep_sec"`    // 离线设备保留时间（秒）
	CloseReasonMaxLen     int    `yaml:"close_reason_max_len"`       // 关闭原因最大长度
	PurposeMaxLen         int    `yaml:"purpose_max_len"`            // 目的最大长度，客户端信息面板的标语
	CommandMaxLen         int    `yaml:"command_max_len"`            // 命令最大长度
	WorkingDirMaxLen      int    `yaml:"working_directory_max_len"`  // 工作目录最大长度
	IdentifierMaxLen      int    `yaml:"identifier_max_len"`         // 标识符最大长度，用于唯一标识设备或用户
	ObserverSlowClientBuf int    `yaml:"slow_client_buffer"`         // 慢客户端缓冲区大小
	LogFile               string `yaml:"log_file"`                   // 日志文件路径
}

// 硬编码的配置默认值
func Default() Config {
	return Config{
		Listen:                ":25664",
		MaxConnections:        1024,
		MaxDevices:            1024,
		MaxSessions:           2048,
		MaxSessionsPerDevice:  8,
		MaxTasksPerSession:    2,
		SessionTimeoutSec:     600,
		ApprovalTimeoutSec:    120,
		TempPasswordTTLSec:    600,
		HeartbeatTimeoutSec:   45,
		HandshakeTimeoutSec:   10,
		MaxOutputBytes:        2 << 20,  //2 MiB
		MaxDownloadBytes:      32 << 20, //32 MiB
		MaxUploadBytes:        32 << 20,
		MaxFrameBytes:         256 << 10, //256 KiB
		AuthRatePerMin:        30,
		TemporaryPasswordMin:  60,
		TemporaryPasswordMax:  24 * 3600, //24h
		OfflineDeviceKeepSec:  24 * 3600,
		CloseReasonMaxLen:     256,
		PurposeMaxLen:         512,
		CommandMaxLen:         32768, //32 KiB
		WorkingDirMaxLen:      4096,  //4 KiB
		IdentifierMaxLen:      128,
		ObserverSlowClientBuf: 64,
		LogFile:               "platform.log",
	}
}

// 用默认值填充0值字段。必须在 Validate 之前调用。
func (c *Config) ApplyDefaults() {
	d := Default()
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.MaxConnections == 0 {
		c.MaxConnections = d.MaxConnections
	}
	if c.MaxDevices == 0 {
		c.MaxDevices = d.MaxDevices
	}
	if c.MaxSessions == 0 {
		c.MaxSessions = d.MaxSessions
	}
	if c.MaxSessionsPerDevice == 0 {
		c.MaxSessionsPerDevice = d.MaxSessionsPerDevice
	}
	if c.MaxTasksPerSession == 0 {
		c.MaxTasksPerSession = d.MaxTasksPerSession
	}
	if c.SessionTimeoutSec == 0 {
		c.SessionTimeoutSec = d.SessionTimeoutSec
	}
	if c.ApprovalTimeoutSec == 0 {
		c.ApprovalTimeoutSec = d.ApprovalTimeoutSec
	}
	if c.TempPasswordTTLSec == 0 {
		c.TempPasswordTTLSec = d.TempPasswordTTLSec
	}
	if c.HeartbeatTimeoutSec == 0 {
		c.HeartbeatTimeoutSec = d.HeartbeatTimeoutSec
	}
	if c.HandshakeTimeoutSec == 0 {
		c.HandshakeTimeoutSec = d.HandshakeTimeoutSec
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = d.MaxOutputBytes
	}
	if c.MaxDownloadBytes == 0 {
		c.MaxDownloadBytes = d.MaxDownloadBytes
	}
	if c.MaxUploadBytes == 0 {
		c.MaxUploadBytes = d.MaxUploadBytes
	}
	if c.MaxFrameBytes == 0 {
		c.MaxFrameBytes = d.MaxFrameBytes
	}
	if c.AuthRatePerMin == 0 {
		c.AuthRatePerMin = d.AuthRatePerMin
	}
	if c.TemporaryPasswordMin == 0 {
		c.TemporaryPasswordMin = d.TemporaryPasswordMin
	}
	if c.TemporaryPasswordMax == 0 {
		c.TemporaryPasswordMax = d.TemporaryPasswordMax
	}
	if c.OfflineDeviceKeepSec == 0 {
		c.OfflineDeviceKeepSec = d.OfflineDeviceKeepSec
	}
	if c.CloseReasonMaxLen == 0 {
		c.CloseReasonMaxLen = d.CloseReasonMaxLen
	}
	if c.PurposeMaxLen == 0 {
		c.PurposeMaxLen = d.PurposeMaxLen
	}
	if c.CommandMaxLen == 0 {
		c.CommandMaxLen = d.CommandMaxLen
	}
	if c.WorkingDirMaxLen == 0 {
		c.WorkingDirMaxLen = d.WorkingDirMaxLen
	}
	if c.IdentifierMaxLen == 0 {
		c.IdentifierMaxLen = d.IdentifierMaxLen
	}
	if c.ObserverSlowClientBuf == 0 {
		c.ObserverSlowClientBuf = d.ObserverSlowClientBuf
	}
	if c.LogFile == "" {
		c.LogFile = d.LogFile
	}
}

// Load 读取并验证包含 `platform:` 部分的 YAML 文件。
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var file struct {
		Platform *Config `yaml:"platform"`
	}
	if err := yaml.Unmarshal(b, &file); err != nil { // 解析 YAML
		return Config{}, fmt.Errorf("parse platform config: %w", err)
	}
	if file.Platform == nil { // 检查是否存在 platform 部分
		return Config{}, errors.New("platform config is missing the platform section")
	}
	c := *file.Platform
	c.ApplyDefaults()                    // 填充默认值
	if err := c.Validate(); err != nil { // 验证配置
		return Config{}, err
	}
	return c, nil
}

// 拒绝不安全和不一致的字段
func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" {
		return errors.New("listen address must be set")
	}
	secureTLS := c.TLSCert != "" && c.TLSKey != ""
	if !secureTLS && !c.AllowInsecure {
		return errors.New("tls_cert and tls_key are required unless allow_insecure is true")
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls_cert and tls_key must be set together")
	}
	if invalidPassword(c.PlayerPassword) {
		return errors.New("player_password must be a non-example secret")
	}
	if len(c.PlayerPassword) < MinPlayerPasswordLen {
		return fmt.Errorf("player_password must be at least %d characters", MinPlayerPasswordLen)
	}
	if c.MaxConnections < 1 || c.MaxConnections > 1_000_000 {
		return errors.New("max_connections must be between 1 and 1000000")
	}
	if c.MaxDevices < 1 || c.MaxSessions < 1 || c.MaxSessionsPerDevice < 1 || c.MaxTasksPerSession < 1 {
		return errors.New("device, session and task limits must be positive")
	}
	if c.MaxSessionsPerDevice > c.MaxSessions {
		return errors.New("max_sessions_per_device must not exceed max_sessions")
	}
	if c.SessionTimeoutSec < 10 || c.SessionTimeoutSec > 24*3600 {
		return errors.New("session_timeout_sec must be between 10 and 86400")
	}
	if c.ApprovalTimeoutSec < 5 || c.ApprovalTimeoutSec > c.SessionTimeoutSec {
		return errors.New("approval_timeout_sec must be between 5 and session_timeout_sec")
	}
	if c.TempPasswordTTLSec <= 0 {
		return errors.New("temp_password_ttl_sec must be positive")
	}
	if c.TemporaryPasswordMin < 1 || c.TemporaryPasswordMax < c.TemporaryPasswordMin {
		return errors.New("temporary password TTL bounds are invalid")
	}
	if c.TempPasswordTTLSec < c.TemporaryPasswordMin || c.TempPasswordTTLSec > c.TemporaryPasswordMax {
		return errors.New("temp_password_ttl_sec must be within temporary password TTL bounds")
	}
	if c.HeartbeatTimeoutSec < 5 {
		return errors.New("heartbeat_timeout_sec must be at least 5")
	}
	if c.HandshakeTimeoutSec < 1 {
		return errors.New("handshake_timeout_sec must be at least 1")
	}
	if c.MaxOutputBytes < 1024 {
		return errors.New("max_output_bytes must be at least 1024")
	}
	if c.MaxDownloadBytes < 1024 || c.MaxDownloadBytes > 256<<20 {
		return errors.New("max_download_bytes must be between 1 KiB and 256 MiB")
	}
	if c.MaxUploadBytes < 1024 || c.MaxUploadBytes > 256<<20 {
		return errors.New("max_upload_bytes must be between 1 KiB and 256 MiB")
	}
	if c.MaxFrameBytes < 4096 || c.MaxFrameBytes > 16<<20 {
		return errors.New("max_frame_bytes must be between 4096 and 16 MiB")
	}
	if c.AuthRatePerMin < 1 {
		return errors.New("auth_rate_per_min must be positive")
	}
	if c.OfflineDeviceKeepSec < 60 {
		return errors.New("offline_device_keep_sec must be at least 60")
	}
	if c.CloseReasonMaxLen < 1 || c.PurposeMaxLen < 1 || c.CommandMaxLen < 1 || c.WorkingDirMaxLen < 1 || c.IdentifierMaxLen < 1 {
		return errors.New("length limits must be positive")
	}
	if c.ObserverSlowClientBuf < 1 {
		return errors.New("slow_client_buffer must be positive")
	}
	return nil
}

// 获取配置中各种数值
// SessionTimeout returns the approved session lifetime.
func (c Config) SessionTimeout() time.Duration {
	return time.Duration(c.SessionTimeoutSec) * time.Second
}

// ApprovalTimeout returns the pending-approval lifetime.
func (c Config) ApprovalTimeout() time.Duration {
	return time.Duration(c.ApprovalTimeoutSec) * time.Second
}

// TempPasswordTTL returns the default temporary password lifetime.
func (c Config) TempPasswordTTL() time.Duration {
	return time.Duration(c.TempPasswordTTLSec) * time.Second
}

// HeartbeatTimeout returns the read deadline for established connections.
func (c Config) HeartbeatTimeout() time.Duration {
	return time.Duration(c.HeartbeatTimeoutSec) * time.Second
}

// HandshakeTimeout returns the deadline for the first frame.
func (c Config) HandshakeTimeout() time.Duration {
	return time.Duration(c.HandshakeTimeoutSec) * time.Second
}

// MinTempPasswordTTL returns the lower clamp for client requested TTLs.
func (c Config) MinTempPasswordTTL() time.Duration {
	return time.Duration(c.TemporaryPasswordMin) * time.Second
}

// MaxTempPasswordTTL returns the upper clamp for client requested TTLs.
func (c Config) MaxTempPasswordTTL() time.Duration {
	return time.Duration(c.TemporaryPasswordMax) * time.Second
}

func invalidPassword(password string) bool {
	trimmed := strings.TrimSpace(password)
	return trimmed == "" || password == examplePassword || strings.HasPrefix(trimmed, "REPLACE_WITH_")
}
