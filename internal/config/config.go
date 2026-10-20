package config

import (
	"crypto/subtle"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

const examplePassword = "REPLACE_WITH_A_LONG_RANDOM_SECRET"

type Config struct {
	ControlPort           int    `yaml:"control_port"`
	RoomPortMin           int    `yaml:"room_port_min"`
	RoomPortMax           int    `yaml:"room_port_max"`
	HostPassword          string `yaml:"host_password"`
	PlayerPassword        string `yaml:"player_password"`
	MaxRooms              int    `yaml:"max_rooms"`
	MaxPlayersPerRoom     int    `yaml:"max_players_per_room"`
	MaxPendingPerRoom     int    `yaml:"max_pending_per_room"`
	PendingTimeoutSec     int    `yaml:"pending_timeout_sec"`
	ReconnectGraceSec     int    `yaml:"reconnect_grace_sec"`
	HeartbeatTimeoutSec   int    `yaml:"heartbeat_timeout_sec"`
	SessionIdleTimeoutSec int    `yaml:"session_idle_timeout_sec"`
	MaxConnections        int    `yaml:"max_connections"`
	AuthRatePerMin        int    `yaml:"auth_rate_per_min"`
	LogFile               string `yaml:"log_file"`
}

func Load(path string) (Config, error) {
	c := Config{
		ControlPort: 25564, RoomPortMin: 25570, RoomPortMax: 25599,
		MaxRooms: 30, MaxPlayersPerRoom: 8, MaxPendingPerRoom: 16,
		PendingTimeoutSec: 10, ReconnectGraceSec: 30,
		HeartbeatTimeoutSec: 45, SessionIdleTimeoutSec: 300, MaxConnections: 1024,
		AuthRatePerMin: 10, LogFile: "relay.log",
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if c.ControlPort < 1 || c.ControlPort > 65535 {
		return fmt.Errorf("control_port must be between 1 and 65535")
	}
	if c.RoomPortMin < 1 || c.RoomPortMin > c.RoomPortMax || c.RoomPortMax > 65535 {
		return fmt.Errorf("invalid room port range")
	}
	if c.ControlPort >= c.RoomPortMin && c.ControlPort <= c.RoomPortMax {
		return fmt.Errorf("control_port must not overlap the room port range")
	}
	if invalidPassword(c.HostPassword) {
		return fmt.Errorf("host_password must be set to a non-example secret")
	}
	if invalidPassword(c.PlayerPassword) {
		return fmt.Errorf("player_password must be set to a non-example secret")
	}
	if subtle.ConstantTimeCompare([]byte(c.HostPassword), []byte(c.PlayerPassword)) == 1 {
		return fmt.Errorf("host_password and player_password must be different")
	}
	if c.MaxRooms < 1 || c.MaxRooms > c.RoomPortMax-c.RoomPortMin+1 {
		return fmt.Errorf("max_rooms must fit within the room port range")
	}
	if c.MaxPlayersPerRoom < 1 || c.MaxPendingPerRoom < 1 || c.PendingTimeoutSec < 1 || c.ReconnectGraceSec < 1 || c.HeartbeatTimeoutSec < 1 || c.AuthRatePerMin < 1 {
		return fmt.Errorf("limits and timeout values must be positive")
	}
	if c.MaxConnections < 1 || c.MaxConnections > 65535 {
		return fmt.Errorf("max_connections must be between 1 and 65535")
	}
	if c.SessionIdleTimeoutSec < 30 || c.SessionIdleTimeoutSec > 3600 {
		return fmt.Errorf("session_idle_timeout_sec must be between 30 and 3600")
	}
	return nil
}

func invalidPassword(password string) bool {
	return strings.TrimSpace(password) == "" || password == examplePassword || strings.HasPrefix(password, "REPLACE_WITH_") || password == "12345678" || password == "123456789"
}
