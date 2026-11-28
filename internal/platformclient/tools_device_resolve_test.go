package platformclient

import (
	"errors"
	"testing"

	"agentremote/internal/agenttools"
	"agentremote/internal/platform/protocol"
)

func deviceFixture() []protocol.DeviceInfo {
	return []protocol.DeviceInfo{
		{DeviceID: "111111111", Name: "DESKTOP-AAA", Online: true, Mode: "direct"},
		{DeviceID: "222222222", Name: "DESKTOP-AAB", Online: true, Mode: "approval"},
		{DeviceID: "333333333", Name: "LAPTOP-OFFLINE", Online: false, Mode: "approval"},
	}
}

func TestMatchDeviceByID(t *testing.T) {
	device, err := matchDevice(deviceFixture(), "222222222", "")
	if err != nil || device.Name != "DESKTOP-AAB" {
		t.Fatalf("got %+v %v", device, err)
	}
}

func TestMatchDeviceByNameExactBeforePartial(t *testing.T) {
	devices := append(deviceFixture(), protocol.DeviceInfo{DeviceID: "444444444", Name: "DESKTOP-AAA-LAB", Online: true})
	device, err := matchDevice(devices, "", "DESKTOP-AAA")
	if err != nil || device.DeviceID != "111111111" {
		t.Fatalf("exact name should win: %+v %v", device, err)
	}
}

func TestMatchDeviceByNameIsCaseInsensitive(t *testing.T) {
	device, err := matchDevice(deviceFixture(), "", "desktop-aab")
	if err != nil || device.DeviceID != "222222222" {
		t.Fatalf("got %+v %v", device, err)
	}
}

func TestMatchDeviceAmbiguousReturnsCandidates(t *testing.T) {
	_, err := matchDevice(deviceFixture(), "", "DESKTOP-AA")
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != "DEVICE_AMBIGUOUS" {
		t.Fatalf("expected DEVICE_AMBIGUOUS, got %v", err)
	}
	data, _ := toolErr.Data.(map[string]any)
	candidates, _ := data["devices"].([]map[string]any)
	if len(candidates) != 2 {
		t.Fatalf("expected two candidates, got %v", data)
	}
	if toolErr.Hint == "" {
		t.Fatal("ambiguous name must carry a hint")
	}
}

func TestMatchDeviceNotFoundListsOnlineDevices(t *testing.T) {
	_, err := matchDevice(deviceFixture(), "", "no-such-host")
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != "DEVICE_NOT_FOUND" {
		t.Fatalf("expected DEVICE_NOT_FOUND, got %v", err)
	}
	data, _ := toolErr.Data.(map[string]any)
	candidates, _ := data["devices"].([]map[string]any)
	if len(candidates) != 2 {
		t.Fatalf("offline devices must not be listed, got %v", candidates)
	}
	for _, candidate := range candidates {
		if candidate["device_id"] == "333333333" {
			t.Fatal("offline device leaked into candidates")
		}
	}
}

func TestMatchDeviceByUnknownIDExplainsRestart(t *testing.T) {
	_, err := matchDevice(deviceFixture(), "999999999", "")
	var toolErr *agenttools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != "DEVICE_NOT_FOUND" {
		t.Fatalf("got %v", err)
	}
	if toolErr.Hint == "" {
		t.Fatal("missing id must explain how to recover")
	}
}
