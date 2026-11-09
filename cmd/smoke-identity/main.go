// Command smoke-identity drives the device-identity flows end to end against an
// in-process platform server (plaintext WebSocket, no TLS material needed):
//
//  1. 撞码：两台设备提议同一个设备码，密码不同 → 后来者拿到服务端分配的新码
//  2. 稳定性：拿新码的设备重连后码不变
//  3. 释放：从未建立过会话的设备空闲超时后被释放（devices → tombstones）
//  4. reclaim：原持有者凭原密码回到原设备码，版本号递增
//  5. 限流：注册频率超限返回可重试的 RATE_LIMITED，且连接不被断开
//
// Usage: go run ./cmd/smoke-identity
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"agentremote/internal/platform/config"
	"agentremote/internal/platform/core"
	"agentremote/internal/platform/gateway"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platform/transport"
)

const (
	playerPassword = "smoke-player-password-0123456789"
	deviceID       = "111222333"
	deviceA        = "0123456789abcdef0123456789abcdef"
	deviceB        = "fedcba9876543210fedcba9876543210"
)

type client struct {
	name  string
	conn  transport.Conn
	codec protocol.Codec
}

func main() {
	// 为了在秒级完成“释放 → reclaim”演示，把两个真实部署里是 600/86400 的
	// 配置压到最小；注册限流也压到 4，便于触发一次 RATE_LIMITED。
	cfg := config.Default()
	cfg.AllowInsecure = true
	cfg.PlayerPassword = playerPassword
	cfg.MaxDevices = 16
	cfg.RegisterRatePerMin = 4
	cfg.UnusedDeviceReleaseSec = 1
	cfg.OfflineDeviceKeepSec = 60
	cfg.TempPasswordTTLSec = 60
	if err := cfg.Validate(); err != nil {
		fail("config: %v", err)
	}

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail("listen: %v", err)
	}
	listener := transport.ServeWebSocketListener(raw, cfg.MaxFrameBytes)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	gw := gateway.NewWithListener(cfg, logger, listener, nil) // 进程内跑，不需要 Redis 路由
	coreSvc := core.New(cfg, logger, gw)
	gw.Bind(coreSvc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = gw.Run(ctx)
	}()
	go func() { _ = coreSvc.Run(ctx) }()
	defer func() {
		cancel()
		gw.Close()
		<-done
	}()
	addr := "ws://" + listener.Addr().String()
	fmt.Printf("in-process platform listening on %s\n", addr)

	operator := dial(addr, "operator")
	operator.auth(playerPassword)

	// 1. 第一台设备提议 deviceID → 服务端采纳
	a := dial(addr, "device-a")
	a.auth(playerPassword)
	adopted := a.register(deviceID, deviceA)
	expect(adopted.DeviceID == deviceID, "first claim adopts the requested device id, got %q", adopted.DeviceID)
	expect(adopted.PasswordVersion == 1, "initial password version = %d, want 1", adopted.PasswordVersion)
	step("device A adopted", "device_id", adopted.DeviceID, "version", adopted.PasswordVersion)

	// 2. 第二台设备提议同一个码但密码不同 → 分配新码，且不抢占原条目
	b := dial(addr, "device-b")
	b.auth(playerPassword)
	assigned := b.register(deviceID, deviceB)
	expect(assigned.DeviceID != deviceID, "conflicting claim must not take over %q", deviceID)
	expect(len(assigned.DeviceID) == 9, "assigned device id %q should be 9 digits", assigned.DeviceID)
	step("device B reassigned", "requested", deviceID, "assigned", assigned.DeviceID)

	// 3. 原条目不受影响：A 仍然在线，设备表里两台都在
	expect(countDevices(operator) == 2, "device table should hold both devices")

	// 4. B 拿新码重连（每次重连都是一条新连接：同一条连接重复注册会被服务端
	// 以 ALREADY_REGISTERED 拒绝），码保持稳定
	_ = b.conn.Close()
	time.Sleep(100 * time.Millisecond)
	b2 := dial(addr, "device-b2")
	b2.auth(playerPassword)
	reconnected := b2.register(assigned.DeviceID, deviceB)
	expect(reconnected.DeviceID == assigned.DeviceID, "reassigned device must keep its new id, got %q", reconnected.DeviceID)
	step("device B reconnected", "device_id", reconnected.DeviceID, "version", reconnected.PasswordVersion)

	// 5. A 断开后空闲超时被释放（它从未建立过会话）
	_ = a.conn.Close()
	time.Sleep(3 * time.Second)
	expect(countDevices(operator) == 1, "unused offline device should be released from the device table")
	step("device A released", "remaining", 1)

	// 6. A 凭原密码 reclaim 自己的码，版本号递增
	reclaimed := dial(addr, "device-a")
	reclaimed.auth(playerPassword)
	reclaimedInfo := reclaimed.register(deviceID, deviceA)
	expect(reclaimedInfo.DeviceID == deviceID, "owner reclaim must return the original id, got %q", reclaimedInfo.DeviceID)
	expect(reclaimedInfo.PasswordVersion > adopted.PasswordVersion,
		"reclaim should bump password version: %d -> %d", adopted.PasswordVersion, reclaimedInfo.PasswordVersion)
	step("device A reclaimed", "device_id", reclaimedInfo.DeviceID, "version", reclaimedInfo.PasswordVersion)

	// 7. 注册限流：上述注册共 4 次（已达配额），第 5 次必须被拒绝
	limited := dial(addr, "limited")
	limited.auth(playerPassword)
	limited.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: "444555666", DeviceName: "rate-limited",
		Platform: "windows", Mode: protocol.ModeApproval, TemporaryPassword: deviceA,
	})
	code := limited.recvError()
	expect(code == "RATE_LIMITED", "registration over quota should be RATE_LIMITED, got %q", code)
	// 非致命错误：同一条连接还能继续发请求
	limited.send(protocol.Message{Type: protocol.TypeListDevices})
	limited.recv(protocol.TypeDeviceList)
	step("registration rate limited", "code", code, "connection", "still open")

	fmt.Println("\nALL CHECKS PASSED")
}

func dial(addr, name string) *client {
	conn, err := transport.DialWebSocket(addr, nil)
	if err != nil {
		fail("%s dial: %v", name, err)
	}
	return &client{name: name, conn: conn, codec: protocol.NewCodec(protocol.DefaultMaxMessageBytes)}
}

func countDevices(operator *client) int {
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	return len(operator.recv(protocol.TypeDeviceList).Devices)
}

func (c *client) auth(password string) {
	c.send(protocol.Message{Type: protocol.TypeAuthenticatePlayer, IdentityType: "player", PlayerPassword: password})
	c.recv(protocol.TypeAuthenticated)
}

func (c *client) register(id, password string) protocol.Message {
	c.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: id, DeviceName: "smoke-device",
		Platform: "windows", Mode: protocol.ModeApproval, TemporaryPassword: password,
	})
	return c.recv(protocol.TypeDeviceRegistered)
}

func (c *client) send(m protocol.Message) {
	m.Version = protocol.Version
	if m.BusinessType == "" {
		m.BusinessType = protocol.BusinessAgentAssist
	}
	data, err := c.codec.Encode(m)
	if err != nil {
		fail("%s encode %s: %v", c.name, m.Type, err)
	}
	if err := c.conn.WriteMessage(data); err != nil {
		fail("%s send %s: %v", c.name, m.Type, err)
	}
}

func (c *client) recv(types ...string) protocol.Message {
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		data, err := c.conn.ReadMessage()
		if err != nil {
			fail("%s receive %v: %v", c.name, types, err)
		}
		m, err := c.codec.Decode(data)
		if err != nil {
			fail("%s decode %v: %v", c.name, types, err)
		}
		if len(want) == 0 || want[m.Type] {
			if m.Type == protocol.TypeError {
				fail("%s got error %s: %s", c.name, m.Code, m.Message)
			}
			return m
		}
	}
}

func (c *client) recvError() string {
	for {
		_ = c.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		data, err := c.conn.ReadMessage()
		if err != nil {
			fail("%s receive error: %v", c.name, err)
		}
		m, err := c.codec.Decode(data)
		if err != nil {
			fail("%s decode error: %v", c.name, err)
		}
		if m.Type == protocol.TypeError {
			return m.Code
		}
	}
}

func expect(condition bool, format string, args ...any) {
	if condition {
		return
	}
	fail(format, args...)
}

func step(label string, kv ...any) {
	fmt.Printf("  ok  %-28s", label)
	for i := 0; i+1 < len(kv); i += 2 {
		if raw, err := json.Marshal(kv[i+1]); err == nil {
			fmt.Printf(" %v=%s", kv[i], strings.Trim(string(raw), "\""))
		}
	}
	fmt.Println()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
