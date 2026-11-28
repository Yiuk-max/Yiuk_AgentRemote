package platformclient

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
)

// maxDeviceErrorCandidates 限制错误信息里列出的设备数量，避免把整张表塞回上下文。
const maxDeviceErrorCandidates = 20

// matchDevice 实现设备解析规则：id 精确优先；name 先精确、再包含；
// 包含匹配命中多个时不猜，直接报候选。纯函数，便于单测。
func matchDevice(devices []protocol.DeviceInfo, deviceID, deviceName string) (protocol.DeviceInfo, error) {
	id := strings.TrimSpace(deviceID)
	name := strings.TrimSpace(deviceName)
	if id != "" {
		for _, device := range devices {
			if device.DeviceID == id {
				return device, nil
			}
		}
		return protocol.DeviceInfo{}, &agenttools.Error{
			Code:    "DEVICE_NOT_FOUND",
			Message: fmt.Sprintf("no device with id %s; the platform is in-memory, so ids change after a restart. Check list_devices and use the current device_id or the device name.", id),
			Hint:    "call list_devices and retry with the current device_id, or pass device_name instead",
			Data:    map[string]any{"devices": deviceCandidates(devices)},
		}
	}

	var exact []protocol.DeviceInfo
	var partial []protocol.DeviceInfo
	for _, device := range devices {
		if device.Name == name {
			exact = append(exact, device)
			continue
		}
		if strings.Contains(strings.ToLower(device.Name), strings.ToLower(name)) {
			partial = append(partial, device)
		}
	}
	if len(exact) == 1 {
		return exact[0], nil
	}
	if len(exact) > 1 {
		return protocol.DeviceInfo{}, &agenttools.Error{
			Code:    "DEVICE_AMBIGUOUS",
			Message: fmt.Sprintf("%d devices are named %q; use device_id instead", len(exact), name),
			Hint:    "pick one device_id from data.devices",
			Data:    map[string]any{"devices": deviceCandidates(exact)},
		}
	}
	switch len(partial) {
	case 1:
		return partial[0], nil
	case 0:
		return protocol.DeviceInfo{}, &agenttools.Error{
			Code:    "DEVICE_NOT_FOUND",
			Message: fmt.Sprintf("no device name contains %q", name),
			Hint:    "call list_devices to see the available device_id and name values",
			Data:    map[string]any{"devices": deviceCandidates(devices)},
		}
	default:
		return protocol.DeviceInfo{}, &agenttools.Error{
			Code:    "DEVICE_AMBIGUOUS",
			Message: fmt.Sprintf("%d devices match %q: %s", len(partial), name, joinDeviceNames(partial)),
			Hint:    "pass the exact device_name or a device_id from data.devices",
			Data:    map[string]any{"devices": deviceCandidates(partial)},
		}
	}
}

// deviceCandidates 生成在线设备摘要（最多 20 条），让错误回复自带现场。
func deviceCandidates(devices []protocol.DeviceInfo) []map[string]any {
	candidates := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		if !device.Online {
			continue
		}
		candidates = append(candidates, map[string]any{
			"device_id": device.DeviceID,
			"name":      device.Name,
		})
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i]["device_id"].(string) < candidates[j]["device_id"].(string)
	})
	if len(candidates) > maxDeviceErrorCandidates {
		candidates = candidates[:maxDeviceErrorCandidates]
	}
	return candidates
}

func joinDeviceNames(devices []protocol.DeviceInfo) string {
	names := make([]string, 0, len(devices))
	for _, device := range devices {
		names = append(names, device.Name+" ("+device.DeviceID+")")
	}
	return strings.Join(names, ", ")
}

// resolveDevice 取一次设备列表并解析目标设备。
func (c *Client) resolveDevice(ctx context.Context, deviceID, deviceName string) (protocol.DeviceInfo, error) {
	if strings.TrimSpace(deviceID) == "" && strings.TrimSpace(deviceName) == "" {
		return protocol.DeviceInfo{}, &agenttools.Error{Code: "INVALID_ARGUMENT", Message: "provide device_id or device_name"}
	}
	list, err := c.handleListDevices(ctx)
	if err != nil {
		return protocol.DeviceInfo{}, err
	}
	devices, _ := list.([]protocol.DeviceInfo)
	device, err := matchDevice(devices, deviceID, deviceName)
	if err != nil {
		return protocol.DeviceInfo{}, err
	}
	if !device.Online {
		return protocol.DeviceInfo{}, &agenttools.Error{
			Code:    "DEVICE_OFFLINE",
			Message: fmt.Sprintf("device %s is registered but offline", device.DeviceID),
			Hint:    "ask the owner to start the client, then retry",
			Data:    map[string]any{"device": map[string]any{"device_id": device.DeviceID, "name": device.Name}},
		}
	}
	return device, nil
}
