package platformclient

import (
	"context"

	"encoding/json"
	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/platform/protocol"
)

func (c *Client) listDevices(ctx context.Context) (any, error) {
	resp, err := c.call(ctx, protocol.Message{Type: protocol.TypeListDevices}, func(m protocol.Message) bool {
		return m.Type == protocol.TypeDeviceList
	})
	if err != nil {
		return nil, err
	}
	return resp.Devices, nil
}

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
