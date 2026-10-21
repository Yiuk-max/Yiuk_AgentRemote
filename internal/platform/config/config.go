// Package config defines the in-memory platform server configuration.
//
// The platform server intentionally uses a separate configuration section from
// the legacy Minecraft relay so player passwords, ports and limits can be
// isolated as required by docs/server-refactor-plan.md.
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

// Config is the platform server configuration. All fields map to the
// `platform:` section of the YAML file.
type Config struct {
	Listen string `yaml:"listen"`

	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
	// AllowInsecure permits a plaintext TCP listener. It exists only for local
	// development and tests and must never be enabled in production.
	AllowInsecure bool `yaml:"allow_insecure"`

	PlayerPassword string `yaml:"player_password"`

	MaxConnections       int `yaml:"max_connections"`
	MaxDevices           int `yaml:"max_devices"`
	MaxSessions          int `yaml:"max_sessions"`
	MaxSessionsPerDevice int `yaml:"max_sessions_per_device"`
	MaxTasksPerSession   int `yaml:"max_tasks_per_session"`

	SessionTimeoutSec     int    `yaml:"session_timeout_sec"`
	ApprovalTimeoutSec    int    `yaml:"approval_timeout_sec"`
	TempPasswordTTLSec    int    `yaml:"temp_password_ttl_sec"`
	HeartbeatTimeoutSec   int    `yaml:"heartbeat_timeout_sec"`
	HandshakeTimeoutSec   int    `yaml:"handshake_timeout_sec"`
	MaxOutputBytes        int    `yaml:"max_output_bytes"`
	MaxFrameBytes         int    `yaml:"max_frame_bytes"`
	AuthRatePerMin        int    `yaml:"auth_rate_per_min"`
	TemporaryPasswordMin  int    `yaml:"temporary_password_min_sec"`
	TemporaryPasswordMax  int    `yaml:"temporary_password_max_sec"`
	OfflineDeviceKeepSec  int    `yaml:"offline_device_keep_sec"`
	CloseReasonMaxLen     int    `yaml:"close_reason_max_len"`
	PurposeMaxLen         int    `yaml:"purpose_max_len"`
	CommandMaxLen         int    `yaml:"command_max_len"`
	WorkingDirMaxLen      int    `yaml:"working_directory_max_len"`
	IdentifierMaxLen      int    `yaml:"identifier_max_len"`
	ObserverSlowClientBuf int    `yaml:"slow_client_buffer"`
	LogFile               string `yaml:"log_file"`
}

// Default returns the documented defaults from the refactor plan.
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
		MaxOutputBytes:        2 << 20,
		MaxFrameBytes:         256 << 10,
		AuthRatePerMin:        10,
		TemporaryPasswordMin:  60,
		TemporaryPasswordMax:  24 * 3600,
		OfflineDeviceKeepSec:  24 * 3600,
		CloseReasonMaxLen:     256,
		PurposeMaxLen:         512,
		CommandMaxLen:         32768,
		WorkingDirMaxLen:      4096,
		IdentifierMaxLen:      128,
		ObserverSlowClientBuf: 64,
		LogFile:               "platform.log",
	}
}

// ApplyDefaults fills zero-valued fields with documented defaults. YAML
// unmarshalling into a partial nested section would otherwise zero fields.
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

// Load reads and validates a YAML file containing a `platform:` section.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var file struct {
		Platform *Config `yaml:"platform"`
	}
	if err := yaml.Unmarshal(b, &file); err != nil {
		return Config{}, fmt.Errorf("parse platform config: %w", err)
	}
	if file.Platform == nil {
		return Config{}, errors.New("platform config is missing the platform section")
	}
	c := *file.Platform
	c.ApplyDefaults()
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate rejects unsafe or inconsistent configuration.
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
	if len(c.PlayerPassword) < 16 {
		return errors.New("player_password must be at least 16 characters")
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
