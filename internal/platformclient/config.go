package platformclient

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"encoding/json"
	"yiuk-switchboard/internal/platform/protocol"
	"yiuk-switchboard/internal/userconfig"
)

const configFileName = "platform-client.json"

// minPlayerPasswordLen mirrors the server-side configuration floor so a
// password the platform accepts is never rejected here.
const minPlayerPasswordLen = 8

// Heartbeat defaults. The operator connection is idle between tool calls and
// the platform closes connections silent for heartbeat_timeout_sec (45s by
// default), so ping well inside that window.
const (
	defaultHeartbeatIntervalSeconds = 15
	// minHeartbeatIntervalSeconds is a floor so a bad configuration file cannot
	// turn the operator into a busy loop of pings.
	minHeartbeatIntervalSeconds = 1
	maxMissedHeartbeats         = 3
)

// Config configures an operator connection to the platform.
type Config struct {
	Address            string `json:"address"`
	PlayerPassword     string `json:"player_password"`
	TLSCAFile          string `json:"tls_ca,omitempty"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify,omitempty"`
	// PinnedCertSHA256 is the lowercase hex SHA-256 of the server certificate
	// DER bytes. When set, only that exact certificate is accepted, which keeps
	// self-signed deployments usable without trusting arbitrary certificates.
	PinnedCertSHA256 string `json:"pinned_cert_sha256,omitempty"`
	AllowPlaintext   bool   `json:"allow_plaintext,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds,omitempty"`
	// HeartbeatIntervalSeconds is how often an idle operator connection pings
	// the platform. Defaults to 15s and never goes below 1s.
	HeartbeatIntervalSeconds int `json:"heartbeat_interval_seconds,omitempty"`
}

// ConfigPath returns the per-user platform client configuration path.
func ConfigPath() (string, error) {
	return userconfig.Path(configFileName)
}

// NewFromUserConfig loads the platform client configuration. It returns an
// error when the file is missing or invalid so callers can fall back to the
// local bridge.
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

// New validates configuration and returns a lazy client. The connection is
// established on the first tool call.
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
		codec:     protocol.NewCodec(protocol.DefaultMaxFrameBytes),
		sessions:  make(map[string]protocol.SessionInfo),
		tasks:     make(map[string]*taskCache),
		downloads: make(map[string]*downloadCache),
	}, nil
}
