package agent

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Listen          string `yaml:"listen"`
	TLSCert         string `yaml:"tls_cert"`
	TLSKey          string `yaml:"tls_key"`
	RelayBinary     string `yaml:"relay_binary"`
	RelayConfig     string `yaml:"relay_config"`
	RelayWorkingDir string `yaml:"relay_working_dir"`
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("parse ops config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Listen) == "" || c.TLSCert == "" || c.TLSKey == "" {
		return fmt.Errorf("listen, tls_cert and tls_key are required")
	}
	if c.RelayBinary == "" || c.RelayConfig == "" {
		return fmt.Errorf("relay_binary and relay_config are required")
	}
	return nil
}
