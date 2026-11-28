package platformclient

import (
	"context"
	"encoding/json"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
)

// handleListDevices 请求当前操作端可见的设备列表。
func (c *Client) handleListDevices(ctx context.Context) (any, error) {
	response, err := c.sendRequest(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	return response.Devices, nil
}

// handleGetDeviceStatus 解析 device_id / device_name，返回设备的在线与会话状态。
func (c *Client) handleGetDeviceStatus(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	device, err := c.resolveDevice(ctx, input.DeviceID, input.DeviceName)
	if err != nil {
		return nil, err
	}
	return device, nil
}
