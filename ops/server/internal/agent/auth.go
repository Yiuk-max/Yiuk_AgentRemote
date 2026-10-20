package agent

import (
	"crypto/subtle"
	"os"

	"gopkg.in/yaml.v3"
	relayconfig "yiuk-switchboard/internal/config"
)

type authenticator struct{ relayConfigPath string }

func newAuthenticator(relayConfigPath string) *authenticator {
	return &authenticator{relayConfigPath: relayConfigPath}
}

func (a *authenticator) verifyHostPassword(password string) bool {
	b, err := os.ReadFile(a.relayConfigPath)
	if err != nil {
		return false
	}
	var config relayconfig.Config
	if yaml.Unmarshal(b, &config) != nil || config.HostPassword == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(password), []byte(config.HostPassword)) == 1
}
