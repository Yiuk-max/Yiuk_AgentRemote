package platformclient_test

import (
	"context"
	"encoding/json"
	"testing"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"agentremote/internal/platformclient"
)

func TestDeviceNameWorksOverThePlatform(t *testing.T) {
	address := startPlatform(t)
	device := dialFakeDevice(t, address)
	device.register("555666777")
	backend, err := platformclient.New(platformclient.Config{Address: address, OperatorPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	core := agenttools.NewCore(backend)

	// fakeDevice 注册时把名字写成 "fake"；按名字查询应命中同一台设备。
	// GetDeviceStatus 返回 protocol.DeviceInfo，这里直接走原始的 Call 结果。
	rawStatus, _ := json.Marshal(map[string]any{"device_name": "fake"})
	statusResponse := core.Call(context.Background(), agenttools.GetDeviceStatus, rawStatus)
	if !statusResponse.OK {
		t.Fatalf("%+v", statusResponse)
	}
	deviceInfo, ok := statusResponse.Data.(protocol.DeviceInfo)
	if !ok || deviceInfo.DeviceID != "555666777" {
		t.Fatalf("device_name did not resolve: %#v", statusResponse.Data)
	}
	// 未知 id 必须自带现场：在线设备摘要与可执行建议。
	raw, _ := json.Marshal(map[string]any{"device_id": "000000000"})
	response := core.Call(context.Background(), agenttools.GetDeviceStatus, raw)
	if response.OK || response.Code != "DEVICE_NOT_FOUND" {
		t.Fatalf("%+v", response)
	}
	if response.Hint == "" {
		t.Fatal("DEVICE_NOT_FOUND should carry a hint")
	}
	data, _ := response.Data.(map[string]any)
	devices, _ := data["devices"].([]map[string]any)
	if len(devices) != 1 {
		t.Fatalf("expected one online device in the error payload, got %v", data)
	}
}
