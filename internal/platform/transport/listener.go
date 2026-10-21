// Package transport creates the platform TLS listener. Plaintext is available
// only when explicitly allowed for local development or tests.
package transport

import (
	"crypto/tls"
	"errors"
	"net"
)

// Listen creates a TLS listener when cert/key are configured. If both are
// empty and allowInsecure is true it falls back to plaintext TCP.
func Listen(address, certFile, keyFile string, allowInsecure bool) (net.Listener, error) {
	if certFile != "" || keyFile != "" {
		if certFile == "" || keyFile == "" {
			return nil, errors.New("tls certificate and key must be set together")
		}
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		cfg := &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		return tls.Listen("tcp", address, cfg)
	}
	if !allowInsecure {
		return nil, errors.New("TLS is required: configure tls_cert/tls_key or explicitly set allow_insecure")
	}
	return net.Listen("tcp", address)
}
