// 创建平台TLS 监听，只有测试时才允许明文传输
package transport

import (
	"crypto/tls"
	"errors"
	"net"
)

// 如果证书和私钥都配置了，则创建TLS 的监听，如果都是空并且指定了允许明文传输，则返回明文TCP
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
