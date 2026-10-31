package platformclient

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"agentremote/internal/platform/protocol"
	"agentremote/internal/userconfig"
	"encoding/json"
)

const configFileName = "platform-client.json"

// minPlayerPasswordLen 与服务端配置的密码最小长度一致，避免拒绝平台可接受的密码
const minPlayerPasswordLen = 8

// 心跳默认值：操作端连接在工具调用之间处于空闲状态；平台会在连接静默超过 heartbeat_timeout_sec（默认 45 秒）后将其关闭，因此心跳应在此期限内发送
const (
	defaultHeartbeatIntervalSeconds = 15
	// minHeartbeatIntervalSeconds 设置最小间隔，避免错误配置导致操作端频繁发送心跳
	minHeartbeatIntervalSeconds = 1
	maxMissedHeartbeats         = 3
)

// Config 配置与平台的操作端连接
type Config struct {
	Address            string `json:"address"`
	PlayerPassword     string `json:"player_password"`
	TLSCAFile          string `json:"tls_ca,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	// PinnedCertSHA256 是服务器证书 DER 字节的 SHA-256 小写十六进制值
	// 设置后只接受该证书，从而支持使用自签名证书，而无需信任任意证书
	PinnedCertSHA256 string `json:"pinned_cert_sha256,omitempty"`
	AllowPlaintext   bool   `json:"allow_plaintext,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
	// HeartbeatIntervalSeconds 指定空闲操作端连接向平台发送心跳的间隔
	// 默认值为 15 秒，且不能小于 1 秒
	HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds,omitempty"`
}

// ConfigPath 返回当前用户的平台客户端配置文件路径
func ConfigPath() (string, error) {
	return userconfig.Path(configFileName)
}

// NewFromUserConfig 加载平台客户端配置
// 如果文件缺失或无效则返回错误，调用方可回退到本地桥接
func NewFromUserConfig() (*Client, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse platform client config: %w", err)
	}
	return New(cfg)
}

// New 验证配置并返回延迟创建连接的客户端
// 首次工具调用时才会建立连接
func New(cfg Config) (*Client, error) {
	cfg.Address = strings.TrimSpace(cfg.Address)
	if cfg.Address == "" {
		return nil, errors.New("platform client address is required")
	}
	if len(cfg.PlayerPassword) < minPlayerPasswordLen {
		return nil, fmt.Errorf("platform client player_password must be at least %d characters", minPlayerPasswordLen)
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	if cfg.HeartbeatIntervalSeconds <= 0 {
		cfg.HeartbeatIntervalSeconds = defaultHeartbeatIntervalSeconds
	}
	if cfg.HeartbeatIntervalSeconds < minHeartbeatIntervalSeconds {
		cfg.HeartbeatIntervalSeconds = minHeartbeatIntervalSeconds
	}
	return &Client{
		cfg:       cfg,
		codec:     protocol.NewCodec(protocol.DefaultMaxMessageBytes),
		sessions:  make(map[string]protocol.SessionInfo),
		tasks:     make(map[string]*taskCache),
		downloads: make(map[string]*downloadCache),
	}, nil
}
