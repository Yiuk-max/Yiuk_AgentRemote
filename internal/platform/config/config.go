// config 包定义了内存中平台服务器的配置
// 平台服务器使用独立配置节，便于按部署隔离玩家密码、端口和限制。
// YAML → 结构体（先铺默认值再覆盖）+ Validate + 派生 TTL 方法
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

// MinOperatorPasswordLen 是操作者密码（配置键 player_password）的最小长度
// validation.允许短于推荐长度的密码，但在启动时记录为警告
const (
	MinOperatorPasswordLen         = 8
	RecommendedOperatorPasswordLen = 16
)

// 服务器配置.所有字段都映射到 YAML 文件的 `platform:` 部分
type Config struct {
	Listen string `yaml:"listen"` // WebSocket 监听地址与端口

	TLSCert string `yaml:"tls_cert"` //证书文件
	TLSKey  string `yaml:"tls_key"`  //私钥文件
	// 是否允许明文传输TCP.测试可以明文方便调试。生产环境应使用TLS。
	AllowInsecure bool `yaml:"allow_insecure"`

	// 配置键沿用 player_password（已部署的 configs 不能因为改名而失效）。
	OperatorPassword string `yaml:"player_password"`

	MaxConnections       int `yaml:"max_connections"`         // 服务器最大连接数
	MaxDevices           int `yaml:"max_devices"`             // 服务器最大设备数
	MaxSessions          int `yaml:"max_sessions"`            // 服务器最大会话数
	MaxSessionsPerDevice int `yaml:"max_sessions_per_device"` // 每个设备最大会话数
	MaxTasksPerSession   int `yaml:"max_tasks_per_session"`   // 每个会话最大任务数

	SessionTimeoutSec      int    `yaml:"session_timeout_sec"`        // 会话超时秒数
	ApprovalTimeoutSec     int    `yaml:"approval_timeout_sec"`       // 会话审批超时秒数
	TempPasswordTTLSec     int    `yaml:"temp_password_ttl_sec"`      // 临时密码生存时间（秒）
	HeartbeatTimeoutSec    int    `yaml:"heartbeat_timeout_sec"`      // 心跳超时秒数
	HandshakeTimeoutSec    int    `yaml:"handshake_timeout_sec"`      // 握手超时秒数
	MaxOutputBytes         int    `yaml:"max_output_bytes"`           // 最大输出字节数
	MaxDownloadBytes       int    `yaml:"max_download_bytes"`         // 最大下载字节数
	MaxUploadBytes         int    `yaml:"max_upload_bytes"`           // 最大上传字节数
	MaxFrameBytes          int    `yaml:"max_frame_bytes"`            // 单条 WebSocket JSON 消息最大字节数
	AuthRatePerMin         int    `yaml:"auth_rate_per_min"`          // 认证频率（每分钟），ip限流用
	RegisterRatePerMin     int    `yaml:"register_rate_per_min"`      // 设备注册频率（每分钟），ip限流用
	UnusedDeviceReleaseSec int    `yaml:"unused_device_release_sec"`  // 从未建立过会话的设备离线多久后释放（秒），0 表示不启用
	TemporaryPasswordMin   int    `yaml:"temporary_password_min_sec"` // 临时密码最小生存时间（秒）
	TemporaryPasswordMax   int    `yaml:"temporary_password_max_sec"` // 临时密码最大生存时间（秒）
	OfflineDeviceKeepSec   int    `yaml:"offline_device_keep_sec"`    // 离线设备保留时间（秒）
	CloseReasonMaxLen      int    `yaml:"close_reason_max_len"`       // 关闭原因最大长度
	PurposeMaxLen          int    `yaml:"purpose_max_len"`            // 目的最大长度，客户端信息面板的标语
	CommandMaxLen          int    `yaml:"command_max_len"`            // 命令最大长度
	NativeArgumentMaxBytes int    `yaml:"native_argument_max_bytes"`  // 原生操作 arguments 的最大字节数
	WorkingDirMaxLen       int    `yaml:"working_directory_max_len"`  // 工作目录最大长度
	IdentifierMaxLen       int    `yaml:"identifier_max_len"`         // 标识符最大长度，用于唯一标识设备或用户
	ObserverSlowClientBuf  int    `yaml:"slow_client_buffer"`         // 慢客户端缓冲区大小
	LogFile                string `yaml:"log_file"`                   // 日志文件路径

	// 拆成两个进程后使用的内网 gRPC 地址（单进程模式不用）。
	// 两个进程读同一份配置，各自只取自己那一侧的两项。
	CoreGRPCListen    string `yaml:"core_grpc_listen"`    // core 进程：gRPC 监听，提供 Edge 服务
	CoreGRPCAddr      string `yaml:"core_grpc_addr"`      // gateway 进程：连 core 的地址
	GatewayGRPCListen string `yaml:"gateway_grpc_listen"` // gateway 进程：gRPC 监听，提供 Push 服务
	GatewayGRPCAddr   string `yaml:"gateway_grpc_addr"`   // core 进程：连 gateway 的地址

	// Redis 只记录"谁在哪"：网关存活、连接归属、设备路由。
	// 它不存 socket，也不存业务状态；键都带 TTL，重启 Redis 只会让路由失效，
	// 不会让授权失效（授权仍由 core 判断）。
	GatewayID     string `yaml:"gateway_id"`     // 网关身份，写进路由；同一套部署里不能重复
	RedisAddr     string `yaml:"redis_addr"`     // Redis 地址
	RedisPassword string `yaml:"redis_password"` // Redis 密码，空表示无密码
	RedisDB       int    `yaml:"redis_db"`       // Redis 数据库编号
	RouteTTLSec   int    `yaml:"route_ttl_sec"`  // 路由键 TTL（秒），网关按 1/3 周期续期

	// 终态记录保留策略。会话、任务、传输的记录都留在内存里供查询，
	// 任务还带着输出分块（每条最多 max_output_bytes），所以必须回收。
	TerminalRecordRetainSec   int `yaml:"terminal_record_retain_sec"`   // 终态任务/传输记录保留时长（秒）
	TerminalOutputRetainBytes int `yaml:"terminal_output_retain_bytes"` // 保留的终态任务输出总字节上限
}

// 硬编码的配置默认值
func Default() Config {
	return Config{
		Listen:                    ":25664",
		MaxConnections:            1024,
		MaxDevices:                1024,
		MaxSessions:               2048,
		MaxSessionsPerDevice:      8,
		MaxTasksPerSession:        2,
		SessionTimeoutSec:         600,
		ApprovalTimeoutSec:        120,
		TempPasswordTTLSec:        600,
		HeartbeatTimeoutSec:       45,
		HandshakeTimeoutSec:       10,
		MaxOutputBytes:            2 << 20,  //2 MiB
		MaxDownloadBytes:          32 << 20, //32 MiB
		MaxUploadBytes:            32 << 20,
		MaxFrameBytes:             256 << 10, //256 KiB
		AuthRatePerMin:            30,
		RegisterRatePerMin:        30,
		UnusedDeviceReleaseSec:    600,
		TemporaryPasswordMin:      60,
		TemporaryPasswordMax:      24 * 3600, //24h
		OfflineDeviceKeepSec:      24 * 3600,
		CloseReasonMaxLen:         256,
		PurposeMaxLen:             512,
		CommandMaxLen:             32768,     //32 KiB
		NativeArgumentMaxBytes:    128 << 10, //128 KiB
		WorkingDirMaxLen:          4096,      //4 KiB
		IdentifierMaxLen:          128,
		ObserverSlowClientBuf:     64,
		LogFile:                   "platform.log",
		CoreGRPCListen:            ":25670",
		CoreGRPCAddr:              "127.0.0.1:25670",
		GatewayGRPCListen:         ":25671",
		GatewayGRPCAddr:           "127.0.0.1:25671",
		GatewayID:                 "gw-1",
		RedisAddr:                 "127.0.0.1:6379",
		RedisDB:                   0,
		RouteTTLSec:               30,
		TerminalRecordRetainSec:   900,      // 15 分钟，够 Agent 回来取结果
		TerminalOutputRetainBytes: 64 << 20, // 常驻输出上限 64 MiB
	}
}

// Load 读取并验证包含 `platform:` 部分的 YAML 文件。
//
// 默认值先铺满结构体，再让 YAML 覆盖：这样新增字段只需改 Default()，
// 不会出现「加了字段但忘了在填充函数里补一行」的漏配。
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	// 先解成节点，才能区分「没有 platform: 段」和「有但全是空值」。
	var document yaml.Node
	if err := yaml.Unmarshal(b, &document); err != nil {
		return Config{}, fmt.Errorf("parse platform config: %w", err)
	}
	if !hasMappingKey(&document, "platform") {
		return Config{}, errors.New("platform config is missing the platform section")
	}

	c := Default()
	var file struct {
		Platform *Config `yaml:"platform"`
	}
	file.Platform = &c
	if err := document.Decode(&file); err != nil {
		return Config{}, fmt.Errorf("parse platform config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// hasMappingKey 报告顶层文档里是否真的有这个键。
func hasMappingKey(document *yaml.Node, key string) bool {
	root := document
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	if root.Kind != yaml.MappingNode {
		return false
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			return true
		}
	}
	return false
}

// ----------------------------拒绝不安全和不一致的字段-----------------------------
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
	if invalidPassword(c.OperatorPassword) {
		return errors.New("player_password must be a non-example secret")
	}
	if len(c.OperatorPassword) < MinOperatorPasswordLen {
		return fmt.Errorf("player_password must be at least %d characters", MinOperatorPasswordLen)
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
	if c.RegisterRatePerMin < 1 {
		return errors.New("register_rate_per_min must be positive")
	}
	if c.UnusedDeviceReleaseSec < 0 {
		return errors.New("unused_device_release_sec must not be negative")
	}
	if c.OfflineDeviceKeepSec < 60 {
		return errors.New("offline_device_keep_sec must be at least 60")
	}
	if c.CloseReasonMaxLen < 1 || c.PurposeMaxLen < 1 || c.CommandMaxLen < 1 || c.WorkingDirMaxLen < 1 || c.IdentifierMaxLen < 1 {
		return errors.New("length limits must be positive")
	}
	// 原生操作的参数必须是帧预算的一部分：太小会让图像模板无法上传，
	// 太大会让一帧 JSON 直接超过 max_frame_bytes。
	if c.NativeArgumentMaxBytes < 1024 || c.NativeArgumentMaxBytes > c.MaxFrameBytes/2 {
		return errors.New("native_argument_max_bytes must be between 1 KiB and half of max_frame_bytes")
	}
	if c.ObserverSlowClientBuf < 1 {
		return errors.New("slow_client_buffer must be positive")
	}
	if strings.TrimSpace(c.GatewayID) == "" || len(c.GatewayID) > 64 {
		return errors.New("gateway_id must be set and at most 64 characters")
	}
	if strings.TrimSpace(c.RedisAddr) == "" {
		return errors.New("redis_addr must be set")
	}
	if c.RedisDB < 0 || c.RedisDB > 15 {
		return errors.New("redis_db must be between 0 and 15")
	}
	if c.RouteTTLSec < 5 || c.RouteTTLSec > 300 {
		return errors.New("route_ttl_sec must be between 5 and 300")
	}
	if c.TerminalRecordRetainSec < 60 || c.TerminalRecordRetainSec > 24*3600 {
		return errors.New("terminal_record_retain_sec must be between 60 and 86400")
	}
	if c.TerminalOutputRetainBytes < 1<<20 || c.TerminalOutputRetainBytes > 1<<30 {
		return errors.New("terminal_output_retain_bytes must be between 1 MiB and 1 GiB")
	}
	return nil
}

// ----------------------获取配置中各种数值------------------------------------------
// 允许的会话时间
func (c Config) SessionTimeout() time.Duration {
	return time.Duration(c.SessionTimeoutSec) * time.Second
}

// 待审批的会话时间
func (c Config) ApprovalTimeout() time.Duration {
	return time.Duration(c.ApprovalTimeoutSec) * time.Second
}

// 临时密码的默认生存时间
func (c Config) TempPasswordTTL() time.Duration {
	return time.Duration(c.TempPasswordTTLSec) * time.Second
}

// 心跳超时时间
func (c Config) HeartbeatTimeout() time.Duration {
	return time.Duration(c.HeartbeatTimeoutSec) * time.Second
}

// 握手超时时间
func (c Config) HandshakeTimeout() time.Duration {
	return time.Duration(c.HandshakeTimeoutSec) * time.Second
}

// 临时密码的最小生存时间
func (c Config) MinTempPasswordTTL() time.Duration {
	return time.Duration(c.TemporaryPasswordMin) * time.Second
}

// 终态任务/传输记录的保留时长
func (c Config) TerminalRecordRetain() time.Duration {
	return time.Duration(c.TerminalRecordRetainSec) * time.Second
}

// 路由键的有效时间
func (c Config) RouteTTL() time.Duration {
	return time.Duration(c.RouteTTLSec) * time.Second
}

// 临时密码的最大生存时间
func (c Config) MaxTempPasswordTTL() time.Duration {
	return time.Duration(c.TemporaryPasswordMax) * time.Second
}

func invalidPassword(password string) bool {
	trimmed := strings.TrimSpace(password)
	return trimmed == "" || password == examplePassword || strings.HasPrefix(trimmed, "REPLACE_WITH_")
}
