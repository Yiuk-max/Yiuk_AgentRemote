package platformclient

import (
	"context"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"encoding/json"
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

// handleGetDeviceStatus 解析工具参数，从设备列表中查找指定设备的在线及会话状态。
func (c *Client) handleGetDeviceStatus(ctx context.Context, arguments json.RawMessage) (any, error) {
	var input struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(arguments, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	response, err := c.sendRequest(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(message protocol.Message) bool {
		return message.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	for _, device := range response.Devices {
		if device.DeviceID == input.DeviceID {
			return device, nil
		}
	}
	return nil, &agenttools.Error{Code: "DEVICE_NOT_FOUND", Message: "device is not registered"}
}
