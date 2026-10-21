package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAppliesDefaultsAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	content := `
platform:
  listen: "127.0.0.1:0"
  allow_insecure: true
  player_password: "a-very-long-player-password"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxConnections != Default().MaxConnections {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
	if cfg.SessionTimeout().Seconds() != 600 {
		t.Fatalf("session timeout = %v", cfg.SessionTimeout())
	}
}

func TestLoadRejectsExamplePasswordAndMissingTLS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	content := `
platform:
  listen: ":25664"
  player_password: "REPLACE_WITH_A_LONG_RANDOM_PLAYER_SECRET"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}

	content = `
platform:
  listen: ":25664"
  player_password: "a-very-long-player-password"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "tls_cert") {
		t.Fatalf("got %v, want TLS validation error", err)
	}
}

func TestLoadRequiresPlatformSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform.yaml")
	if err := os.WriteFile(path, []byte("minecraft: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected missing platform section error")
	}
}

func TestValidateRejectsInconsistentLimits(t *testing.T) {
	cfg := Default()
	cfg.AllowInsecure = true
	cfg.PlayerPassword = "a-very-long-player-password"
	cfg.MaxSessionsPerDevice = cfg.MaxSessions + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected limit validation error")
	}
	cfg = Default()
	cfg.AllowInsecure = true
	cfg.PlayerPassword = "a-very-long-player-password"
	cfg.ApprovalTimeoutSec = cfg.SessionTimeoutSec + 1
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected approval timeout validation error")
	}
}
