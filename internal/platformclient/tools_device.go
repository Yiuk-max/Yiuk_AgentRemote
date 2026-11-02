package platformclient

import (
	"context"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
	"encoding/json"
)

// listDevices 请求当前操作端可见的设备列表。
func (c *Client) listDevices(ctx context.Context) (any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

// getDeviceStatus 从设备列表中查找指定设备，并返回其在线及会话状态。
func (c *Client) getDeviceStatus(ctx context.Context, raw json.RawMessage) (any, error) {
	var input struct {
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "invalid arguments"}
	}
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	for _, d := range resp.Devices {
		if d.DeviceID == input.DeviceID {
			return d, nil
		}
	}
	return nil, &agenttools.Error{Code: "DEVICE_NOT_FOUND", Message: "device is not registered"}
}
