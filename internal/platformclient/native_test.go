package platformclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platformclient"
)

// 原生操作通道的客户端侧验收：能力校验、信封解码、错误语义与超时。
// 设备仍是测试里的假设备：它按协议回 execute_native 的 stdout/result。

// registerWithCapabilities 以指定能力注册假设备（默认 register 不带能力）。
func (d *fakeDevice) registerWithCapabilities(id string, capabilities []string) {
	d.t.Helper()
	d.send(protocol.Message{Type: protocol.TypeAuthenticateOperator, IdentityType: "player", OperatorPassword: playerPassword})
	d.recv(protocol.TypeAuthenticated)
	d.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: id, DeviceName: "fake", Platform: "windows",
		TemporaryPassword: devicePassword, ClientVersion: "0.1.8", Capabilities: capabilities,
	})
	registered := d.recv(protocol.TypeDeviceRegistered)
	if registered.Device == nil {
		d.t.Fatalf("registration returned no device info")
	}
}

// newNativeSession 打开一个带临时密码的直连会话，并让假设备消费掉状态通知。
func newNativeSession(t *testing.T, backend *platformclient.Client, device *fakeDevice) string {
	t.Helper()
	session := invoke(t, backend, agenttools.RequestConnection, map[string]any{
		"device_id": "device-1", "purpose": "find the save button",
		"temporary_password": devicePassword,
	}).(map[string]any)
	sessionID, _ := session["session_id"].(string)
	if sessionID == "" || session["status"] != protocol.StatusApproved {
		t.Fatalf("unexpected session: %#v", session)
	}
	device.recv(protocol.TypeSessionStatus)
	return sessionID
}

func newNativeBackend(t *testing.T, address string) *platformclient.Client {
	t.Helper()
	backend, err := platformclient.New(platformclient.Config{
		Address: address, OperatorPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func TestInvokeNativeDecodesEnvelope(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.registerWithCapabilities("device-1", []string{"vision.find", "input.drag"})
	backend := newNativeBackend(t, address)
	// 先拉一次设备列表，把能力放进客户端缓存；真实使用中 list_devices / get_device_status 也会做这件事。
	invoke(t, backend, agenttools.ListDevices, map[string]any{})
	sessionID := newNativeSession(t, backend, device)

	done := make(chan struct{})
	go func() {
		defer close(done)
		forwarded := device.recv(protocol.TypeExecuteNative)
		if forwarded.Operation != "vision.find" || forwarded.SessionID != sessionID {
			device.t.Errorf("unexpected forwarded operation: %#v", forwarded)
			return
		}
		if forwarded.TimeoutSeconds != 12 {
			device.t.Errorf("timeout = %d, want 12", forwarded.TimeoutSeconds)
		}
		if string(forwarded.Arguments) != `{"text":"收藏","match":"contains"}` {
			device.t.Errorf("arguments = %s", forwarded.Arguments)
		}
		device.send(protocol.Message{
			Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: forwarded.RequestID,
			Stream: "stdout",
			Data:   `{"ok":true,"data":{"screenshot_id":"a1b2c3d4e5f6","matches":[{"center":[440,96],"score":0.94}]}}`,
		})
		device.send(protocol.Message{
			Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: forwarded.RequestID,
			Status: protocol.TaskSucceeded,
		})
	}()

	data, err := backend.InvokeNative(context.Background(), sessionID, "vision.find",
		json.RawMessage(`{"text":"收藏","match":"contains"}`), 12)
	if err != nil {
		t.Fatalf("InvokeNative: %v", err)
	}
	<-done
	var decoded struct {
		ScreenshotID string `json:"screenshot_id"`
		Matches      []struct {
			Center []float64 `json:"center"`
			Score  float64   `json:"score"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("decode data: %v (%s)", err, data)
	}
	if decoded.ScreenshotID != "a1b2c3d4e5f6" || len(decoded.Matches) != 1 || decoded.Matches[0].Score != 0.94 {
		t.Fatalf("unexpected decoded envelope: %s", data)
	}
}

func TestInvokeNativeRequiresAdvertisedCapability(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.registerWithCapabilities("device-1", []string{"vision.find"})
	backend := newNativeBackend(t, address)
	invoke(t, backend, agenttools.ListDevices, map[string]any{})
	sessionID := newNativeSession(t, backend, device)

	_, err := backend.InvokeNative(context.Background(), sessionID, "input.drag",
		json.RawMessage(`{"from":[1,1],"to":[2,2]}`), 0)
	toolErr := asToolError(t, err)
	if toolErr.Code != "UNSUPPORTED_CLIENT" {
		t.Fatalf("code = %s, want UNSUPPORTED_CLIENT", toolErr.Code)
	}
	if toolErr.Hint == "" {
		t.Fatalf("UNSUPPORTED_CLIENT should carry an upgrade hint")
	}
}

func TestInvokeNativePropagatesBusinessError(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.registerWithCapabilities("device-1", []string{"vision.find"})
	backend := newNativeBackend(t, address)
	invoke(t, backend, agenttools.ListDevices, map[string]any{})
	sessionID := newNativeSession(t, backend, device)

	go func() {
		forwarded := device.recv(protocol.TypeExecuteNative)
		// 业务失败仍然是"执行成功"的任务，错误码在信封里。
		device.send(protocol.Message{
			Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: forwarded.RequestID,
			Stream: "stdout",
			Data:   `{"ok":false,"code":"NOT_FOUND","message":"no candidate above threshold","hint":"lower threshold or take a new screenshot"}`,
		})
		device.send(protocol.Message{
			Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: forwarded.RequestID,
			Status: protocol.TaskSucceeded,
		})
	}()

	_, err := backend.InvokeNative(context.Background(), sessionID, "vision.find",
		json.RawMessage(`{"text":"不存在"}`), 0)
	toolErr := asToolError(t, err)
	if toolErr.Code != "NOT_FOUND" || toolErr.Hint == "" {
		t.Fatalf("unexpected business error: %#v", toolErr)
	}
}

func TestInvokeNativeReportsUnknownResultOnDeviceDisconnect(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.registerWithCapabilities("device-1", []string{"input.drag"})
	backend := newNativeBackend(t, address)
	invoke(t, backend, agenttools.ListDevices, map[string]any{})
	sessionID := newNativeSession(t, backend, device)

	go func() {
		device.recv(protocol.TypeExecuteNative)
		_ = device.conn.Close()
	}()

	_, err := backend.InvokeNative(context.Background(), sessionID, "input.drag",
		json.RawMessage(`{"from":[1,1],"to":[2,2]}`), 2)
	toolErr := asToolError(t, err)
	if toolErr.Code != "RESULT_UNKNOWN" {
		t.Fatalf("code = %s, want RESULT_UNKNOWN (可能已生效，不能当失败重试)", toolErr.Code)
	}
}

// 并发验证：两个原生请求同时在飞，设备故意反序回复。
// 旧实现用 requestMu 把整趟往返串起来，第二个请求根本发不出去（这里会超时）；
// 现在按 correlation_id 分发，各自的响应只落到各自的等待者。
func TestInvokeNativeConcurrentRequestsRouteByCorrelation(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.registerWithCapabilities("device-1", []string{"vision.find", "input.click"})
	backend := newNativeBackend(t, address)
	invoke(t, backend, agenttools.ListDevices, map[string]any{})
	sessionID := newNativeSession(t, backend, device)

	type forwarded struct {
		operation string
		requestID string
	}
	frames := make(chan forwarded, 2)
	go func() {
		for i := 0; i < 2; i++ {
			message := device.recv(protocol.TypeExecuteNative)
			frames <- forwarded{operation: message.Operation, requestID: message.RequestID}
		}
		// 收集完两帧再反序回复：如果两个请求被串行化，第二帧永远不会到，这里会阻塞。
		first := <-frames
		second := <-frames
		send := func(frame forwarded, payload string) {
			device.send(protocol.Message{
				Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: frame.requestID,
				Stream: "stdout", Data: payload,
			})
			device.send(protocol.Message{
				Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: frame.requestID,
				Status: protocol.TaskSucceeded,
			})
		}
		send(second, `{"ok":true,"data":{"who":"second"}}`)
		send(first, `{"ok":true,"data":{"who":"first"}}`)
	}()

	var wg sync.WaitGroup
	results := make([]string, 2)
	errs := make([]error, 2)
	operations := []string{"vision.find", "input.click"}
	for i, operation := range operations {
		wg.Add(1)
		go func(index int, op string) {
			defer wg.Done()
			data, err := backend.InvokeNative(context.Background(), sessionID, op, json.RawMessage(`{}`), 5)
			errs[index] = err
			results[index] = string(data)
		}(i, operation)
	}
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("request %d failed: %v", i, errs[i])
		}
	}
	// 两个响应分别属于不同的请求；只要没有串行化，二者都必须拿到自己的信封。
	if !strings.Contains(results[0], `"who"`) || !strings.Contains(results[1], `"who"`) {
		t.Fatalf("unexpected results: %v", results)
	}
	if results[0] == results[1] {
		t.Fatalf("both requests got the same response: %v", results)
	}
}

// asToolError 断言错误是结构化工具错误并返回它。
func asToolError(t *testing.T, err error) *agenttools.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error is not *agenttools.Error: %T %v", err, err)
	}
	return toolErr
}
