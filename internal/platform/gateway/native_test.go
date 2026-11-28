package gateway_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"agentremote/internal/platform/config"
	"agentremote/internal/platform/protocol"
	"github.com/gorilla/websocket"
)

// 原生操作通道（execute_native）的平台侧验收：
// 授权、白名单、能力协商、幂等、取消、断线与能力覆盖。
//
// 这里的"设备"是测试里的 WebSocket 客户端，它自己声明能力并回原生结果，
// 不涉及真实 Qt 客户端；Qt 侧的分发器在后续提交里单独验证。

const nativeCapabilityAll = `["vision.find","input.click","input.drag"]`

// registeredDevice 注册一个声明了能力的设备。
func registeredDevice(t *testing.T, address, deviceID string, capabilities []string) *testClient {
	t.Helper()
	device := dialClient(t, address)
	device.auth(testOperatorPassword)
	payload := protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: deviceID, DeviceName: "native-device",
		Platform: "windows", TemporaryPassword: testDevicePassword,
		ClientVersion: "0.1.8", Capabilities: capabilities,
	}
	device.send(payload)
	registered := device.recv(protocol.TypeDeviceRegistered)
	if registered.Device == nil {
		t.Fatalf("registration returned no device info: %#v", registered)
	}
	return device
}

// approvedNativeSession 走完"申请 → 批准"，返回 sessionID。
func approvedNativeSession(t *testing.T, operator, device *testClient, deviceID string) string {
	t.Helper()
	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: deviceID, Purpose: "find the save button",
	})
	created := operator.recv(protocol.TypeSessionCreated)
	if created.SessionID == "" {
		t.Fatalf("session was not created: %#v", created)
	}
	device.recv(protocol.TypeSessionRequest)
	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: created.SessionID})
	approved := operator.recv(protocol.TypeSessionStatus)
	if approved.Status != protocol.StatusApproved {
		t.Fatalf("session status = %s, want approved", approved.Status)
	}
	device.recv(protocol.TypeSessionStatus)
	return created.SessionID
}

// sendRaw 直接写一帧原始 JSON，用于构造 json.Marshal 会拒绝的畸形载荷。
func (c *testClient) sendRaw(payload []byte) {
	c.t.Helper()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
		c.t.Fatalf("send raw: %v", err)
	}
}

// waitDeviceOffline 轮询设备列表直到目标设备离线：注册冲突（DEVICE_ALREADY_ONLINE）
// 依赖断线已被接入层处理，测试里必须等这个状态收敛，不能靠 sleep 猜时序。
func waitDeviceOffline(t *testing.T, observer *testClient, deviceID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		observer.send(protocol.Message{Type: protocol.TypeListDevices})
		list := observer.recv(protocol.TypeDeviceList)
		for _, device := range list.Devices {
			if device.DeviceID == deviceID && !device.Online {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("device %s never went offline", deviceID)
}

func TestNativeOperationRequiresAdvertisedCapability(t *testing.T) {
	address, _ := startServer(t, nil)
	// 老客户端：注册时不带 capabilities。
	device := registeredDevice(t, address, "device-old", nil)
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, device, "device-old")

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-1",
		Operation: "vision.find", Arguments: json.RawMessage(`{"text":"收藏"}`),
	})
	failure := operator.recv(protocol.TypeError)
	if failure.Code != "UNSUPPORTED_CLIENT" {
		t.Fatalf("code = %s, want UNSUPPORTED_CLIENT", failure.Code)
	}
	if !strings.Contains(failure.Message, "vision.find") {
		t.Fatalf("error message should name the missing capability: %q", failure.Message)
	}
	// 能力缺失时不能把帧转给设备。
	device.expectNone(150 * time.Millisecond)
}

func TestNativeOperationValidatesWhitelistAndArguments(t *testing.T) {
	address, _ := startServer(t, nil)
	device := registeredDevice(t, address, "device-1", []string{"vision.find"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, device, "device-1")

	cases := []struct {
		name string
		send protocol.Message
		code string
	}{
		{
			name: "unknown operation",
			send: protocol.Message{Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "bad-1",
				Operation: "shell.exec", Arguments: json.RawMessage(`{}`)},
			code: "UNSUPPORTED_OPERATION",
		},
		{
			name: "timeout above native ceiling",
			send: protocol.Message{Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "bad-3",
				Operation: "vision.find", TimeoutSeconds: 26},
			code: "INVALID_ARGUMENT",
		},
		{
			name: "missing request id",
			send: protocol.Message{Type: protocol.TypeExecuteNative, SessionID: sessionID,
				Operation: "vision.find"},
			code: "INVALID_ARGUMENT",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			operator.send(testCase.send)
			failure := operator.recv(protocol.TypeError)
			if failure.Code != testCase.code {
				t.Fatalf("code = %s, want %s (%s)", failure.Code, testCase.code, failure.Message)
			}
		})
	}
	device.expectNone(100 * time.Millisecond)
}

// 畸形 JSON 在 codec 解码阶段就是致命错误：连接会被关闭，而不是回一个错误帧。
// 这条行为对 execute_native 和 execute_command 一致，测试固定住它，
// 免得以后有人把解码失败改成“忽略这一帧”。
func TestNativeMalformedFrameClosesConnection(t *testing.T) {
	address, _ := startServer(t, nil)
	_ = registeredDevice(t, address, "device-1", []string{"vision.find"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)

	operator.sendRaw([]byte(`{"version":1,"type":"execute_native","operation":"vision.find","arguments":{oops}}`))
	_ = operator.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		if _, _, err := operator.conn.ReadMessage(); err != nil {
			return // 连接被服务端关闭
		}
	}
}

func TestNativeOperationRejectsOversizedArguments(t *testing.T) {
	// 把 native_argument_max_bytes 调到最小值，避免构造 128 KiB 的帧拖慢测试。
	address, _ := startServer(t, func(cfg *config.Config) { cfg.NativeArgumentMaxBytes = 1024 })
	device := registeredDevice(t, address, "device-1", []string{"vision.find"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, device, "device-1")

	big := `{"image_b64":"` + strings.Repeat("A", 2048) + `"}`
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "big-1",
		Operation: "vision.find", Arguments: json.RawMessage(big),
	})
	failure := operator.recv(protocol.TypeError)
	if failure.Code != "INVALID_ARGUMENT" {
		t.Fatalf("code = %s, want INVALID_ARGUMENT", failure.Code)
	}
	device.expectNone(100 * time.Millisecond)
}

func TestNativeOperationFlowAndIdempotency(t *testing.T) {
	address, _ := startServer(t, nil)
	device := registeredDevice(t, address, "device-1", []string{"vision.find", "input.click"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, device, "device-1")

	// 设备列表要能看到自报的能力与版本。
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	list := operator.recv(protocol.TypeDeviceList)
	if len(list.Devices) != 1 {
		t.Fatalf("device list size = %d", len(list.Devices))
	}
	if got := strings.Join(list.Devices[0].Capabilities, ","); got != "vision.find,input.click" {
		t.Fatalf("capabilities = %q, want vision.find,input.click", got)
	}
	if list.Devices[0].ClientVersion != "0.1.8" {
		t.Fatalf("client version = %q", list.Devices[0].ClientVersion)
	}

	arguments := json.RawMessage(`{"text":"收藏","match":"contains"}`)
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-1",
		Operation: "vision.find", Arguments: arguments, TimeoutSeconds: 20,
	})
	accepted := operator.recv(protocol.TypeCommandAccepted)
	if accepted.RequestID != "native-1" || accepted.Status != protocol.TaskRunning {
		t.Fatalf("unexpected accepted frame: %#v", accepted)
	}
	forwarded := device.recv(protocol.TypeExecuteNative)
	if forwarded.Operation != "vision.find" || forwarded.SessionID != sessionID {
		t.Fatalf("unexpected forwarded operation: %#v", forwarded)
	}
	if string(forwarded.Arguments) != string(arguments) {
		t.Fatalf("arguments changed in flight: %s", forwarded.Arguments)
	}
	if forwarded.TimeoutSeconds != 20 {
		t.Fatalf("timeout = %d, want 20", forwarded.TimeoutSeconds)
	}

	// 同一 request_id 重发：返回原状态，不能再次下发。
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-1",
		Operation: "vision.find", Arguments: arguments,
	})
	replay := operator.recv(protocol.TypeCommandAccepted)
	if replay.RequestID != "native-1" || replay.Status != protocol.TaskRunning {
		t.Fatalf("replay returned %#v", replay)
	}
	// 用一个新请求证明设备端队列里没有重复帧：下一条 execute_native 必须是 native-2。
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-2",
		Operation: "vision.find", Arguments: json.RawMessage(`{"text":"首页"}`),
	})
	operator.recv(protocol.TypeCommandAccepted)
	nextForwarded := device.recv(protocol.TypeExecuteNative)
	if nextForwarded.RequestID != "native-2" {
		t.Fatalf("device received %q, want native-2 (duplicate replay leaked to device)", nextForwarded.RequestID)
	}
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: "native-2", Status: protocol.TaskSucceeded,
	})
	operator.recvWhere(protocol.TypeCommandResult, func(m protocol.Message) bool { return m.RequestID == "native-2" })

	// 原生结果走同一套输出/终态帧：stdout 是业务 JSON 信封。
	envelope := `{"ok":true,"screenshot_id":"a1b2c3d4e5f6","matches":[]}`
	device.send(protocol.Message{
		Type: protocol.TypeCommandOutput, SessionID: sessionID, RequestID: "native-1",
		Stream: "stdout", Data: envelope,
	})
	output := operator.recv(protocol.TypeCommandOutput)
	if output.Data != envelope {
		t.Fatalf("output = %q, want %q", output.Data, envelope)
	}
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: "native-1", Status: protocol.TaskSucceeded,
	})
	result := operator.recv(protocol.TypeCommandResult)
	if result.Status != protocol.TaskSucceeded || result.RequestID != "native-1" {
		t.Fatalf("unexpected result frame: %#v", result)
	}

	// 轮到 get_command_result 也要能取到同一份输出。
	operator.send(protocol.Message{
		Type: protocol.TypeGetCommandResult, SessionID: sessionID, RequestID: "native-1",
	})
	view := operator.recv(protocol.TypeCommandResult)
	var payload strings.Builder
	for _, chunk := range view.Chunks {
		payload.WriteString(chunk.Data)
	}
	if payload.String() != envelope {
		t.Fatalf("paged output = %q", payload.String())
	}
}

func TestNativeOperationRequiresApprovedSession(t *testing.T) {
	address, _ := startServer(t, nil)
	device := registeredDevice(t, address, "device-1", []string{"vision.find"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)

	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-1", Purpose: "look for a button",
	})
	created := operator.recv(protocol.TypeSessionCreated)
	device.recv(protocol.TypeSessionRequest)

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: created.SessionID, RequestID: "native-pending",
		Operation: "vision.find",
	})
	failure := operator.recv(protocol.TypeError)
	if failure.Code != "INVALID_SESSION_STATE" {
		t.Fatalf("code = %s, want INVALID_SESSION_STATE", failure.Code)
	}
	device.expectNone(100 * time.Millisecond)
}

func TestNativeOperationCancelAndCrossSessionReuse(t *testing.T) {
	address, _ := startServer(t, nil)
	device := registeredDevice(t, address, "device-1", []string{"vision.find"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, device, "device-1")

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-cancel",
		Operation: "vision.find", Arguments: json.RawMessage(`{"text":"收藏"}`),
	})
	operator.recv(protocol.TypeCommandAccepted)
	device.recv(protocol.TypeExecuteNative)

	operator.send(protocol.Message{
		Type: protocol.TypeCancelCommand, SessionID: sessionID, RequestID: "native-cancel",
	})
	device.recv(protocol.TypeCancelCommand)
	cancelled := operator.recv(protocol.TypeCommandCanceled)
	if cancelled.RequestID != "native-cancel" {
		t.Fatalf("unexpected cancel ack: %#v", cancelled)
	}
	// 设备确认取消后，操作者拿到终态。
	device.send(protocol.Message{
		Type: protocol.TypeCommandResult, SessionID: sessionID, RequestID: "native-cancel",
		Status: protocol.TaskCancelled,
	})
	result := operator.recv(protocol.TypeCommandResult)
	if result.Status != protocol.TaskCancelled {
		t.Fatalf("status = %s, want cancelled", result.Status)
	}

	// 同一 request_id 换一个会话必须被拒绝，不能跨会话复用幂等键。
	operator.send(protocol.Message{
		Type: protocol.TypeRequestSession, DeviceID: "device-1", Purpose: "second session",
	})
	second := operator.recv(protocol.TypeSessionCreated)
	device.recv(protocol.TypeSessionRequest)
	device.send(protocol.Message{Type: protocol.TypeApproveSession, SessionID: second.SessionID})
	operator.recv(protocol.TypeSessionStatus)
	device.recv(protocol.TypeSessionStatus)

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: second.SessionID, RequestID: "native-cancel",
		Operation: "vision.find",
	})
	conflict := operator.recv(protocol.TypeError)
	if conflict.Code != "REQUEST_ID_CONFLICT" {
		t.Fatalf("code = %s, want REQUEST_ID_CONFLICT", conflict.Code)
	}
}

func TestNativeOperationResultUnknownOnDeviceDisconnect(t *testing.T) {
	address, _ := startServer(t, nil)
	device := registeredDevice(t, address, "device-1", []string{"input.drag"})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, device, "device-1")

	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-drag",
		Operation: "input.drag", Arguments: json.RawMessage(`{"from":[10,20],"to":[300,200]}`),
	})
	operator.recv(protocol.TypeCommandAccepted)
	device.recv(protocol.TypeExecuteNative)

	_ = device.conn.Close()

	// 断线后任务必须收敛成 result_unknown：拖动可能已经生效，不能当失败重试。
	result := operator.recvWhere(protocol.TypeCommandResult, func(m protocol.Message) bool {
		return m.RequestID == "native-drag"
	})
	if result.Status != protocol.TaskResultUnknown {
		t.Fatalf("status = %s, want result_unknown", result.Status)
	}
	if result.ResultKnown {
		t.Fatalf("result_known = true for an interrupted native operation")
	}
}

func TestNativeCapabilitiesReplacedOnReRegister(t *testing.T) {
	address, _ := startServer(t, nil)
	device := registeredDevice(t, address, "device-1", []string{"vision.find", "input.drag"})
	device.send(protocol.Message{Type: protocol.TypeDeviceHeartbeat})
	device.recv(protocol.TypeHeartbeatAck)
	// 监听设备列表的连接要在关闭前建好，否则断线后没有可用的观察者。
	observer := dialClient(t, address)
	observer.auth(testOperatorPassword)
	_ = device.conn.Close()
	waitDeviceOffline(t, observer, "device-1")

	// 重新注册（同一密码）时只声明一个能力：旧的 input.drag 必须消失。
	again := dialClient(t, address)
	again.auth(testOperatorPassword)
	again.send(protocol.Message{
		Type: protocol.TypeRegisterDevice, DeviceID: "device-1", DeviceName: "native-device",
		Platform: "windows", TemporaryPassword: testDevicePassword,
		ClientVersion: "0.1.9", Capabilities: []string{"vision.find"},
	})
	registered := again.recv(protocol.TypeDeviceRegistered)
	if registered.Device == nil {
		t.Fatalf("registration returned no device info")
	}
	if got := strings.Join(registered.Device.Capabilities, ","); got != "vision.find" {
		t.Fatalf("capabilities = %q, want vision.find", got)
	}
	if registered.Device.ClientVersion != "0.1.9" {
		t.Fatalf("client version = %q, want 0.1.9", registered.Device.ClientVersion)
	}

	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	sessionID := approvedNativeSession(t, operator, again, "device-1")
	operator.send(protocol.Message{
		Type: protocol.TypeExecuteNative, SessionID: sessionID, RequestID: "native-drag",
		Operation: "input.drag", Arguments: json.RawMessage(`{"from":[1,2],"to":[3,4]}`),
	})
	failure := operator.recv(protocol.TypeError)
	if failure.Code != "UNSUPPORTED_CLIENT" {
		t.Fatalf("code = %s, want UNSUPPORTED_CLIENT for the dropped capability", failure.Code)
	}
}

func TestNativeCapabilitiesAreSanitized(t *testing.T) {
	address, _ := startServer(t, nil)
	// 空白、重复、未知名字与空串都要被清掉，客户端升级后上报未知能力不应导致注册失败。
	registeredDevice(t, address, "device-1", []string{
		" vision.find ", "vision.find", "", "vision.unknown", "input.click",
	})
	operator := dialClient(t, address)
	operator.auth(testOperatorPassword)
	operator.send(protocol.Message{Type: protocol.TypeListDevices})
	list := operator.recv(protocol.TypeDeviceList)
	if len(list.Devices) != 1 {
		t.Fatalf("device list size = %d", len(list.Devices))
	}
	if got := strings.Join(list.Devices[0].Capabilities, ","); got != "vision.find,input.click" {
		t.Fatalf("capabilities = %q, want vision.find,input.click", got)
	}
}
