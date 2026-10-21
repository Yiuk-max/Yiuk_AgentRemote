package agenttools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type backendFunc func(context.Context, Request) (any, error)

func (f backendFunc) Invoke(ctx context.Context, req Request) (any, error) { return f(ctx, req) }

func TestToolCatalog(t *testing.T) {
	core := NewCore(nil)
	tools := core.Tools()
	if len(tools) != 10 {
		t.Fatalf("got %d tools, want 10", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if tool.Name == "" || seen[tool.Name] {
			t.Fatalf("empty or duplicate tool name %q", tool.Name)
		}
		seen[tool.Name] = true
		var schema map[string]any
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatalf("%s schema is invalid JSON: %v", tool.Name, err)
		}
		if schema["type"] != "object" || schema["additionalProperties"] != false {
			t.Fatalf("%s schema must be a strict object", tool.Name)
		}
		var outputSchema map[string]any
		if err := json.Unmarshal(tool.OutputSchema, &outputSchema); err != nil {
			t.Fatalf("%s output schema is invalid JSON: %v", tool.Name, err)
		}
		if outputSchema["type"] != "object" {
			t.Fatalf("%s output schema must be an object", tool.Name)
		}
	}
	if !seen[DownloadFile] {
		t.Fatal("download_file tool is missing")
	}
	for _, spec := range tools {
		if spec.Name == ListDevices && !spec.Annotations.ReadOnly {
			t.Fatal("list_devices should be read-only")
		}
		if spec.Name == ExecuteCommand && spec.Annotations.Destructive == nil {
			t.Fatal("execute_command should declare a destructive hint")
		}
	}
}

func TestCallRejectsInvalidInputBeforeBackend(t *testing.T) {
	called := false
	core := NewCore(backendFunc(func(context.Context, Request) (any, error) {
		called = true
		return nil, nil
	}))
	cases := []struct {
		name  string
		input string
	}{
		{GetDeviceStatus, `{}`},
		{ExecuteCommand, `{"session_id":"s","command":"   "}`},
		{RequestConnection, `{"device_id":"d","purpose":"help","permissions":["admin"]}`},
		{RequestConnection, `{"device_id":"d","purpose":"help","permissions":["read_file","read_file"],"temporary_password":"0123456789abcdef0123456789abcdef"}`},
		{CloseSession, `{"session_id":"s","extra":true}`},
		{ExecuteCommand, `{"session_id":"s","command":"whoami","timeout_seconds":0}`},
		{DownloadFile, `{"session_id":"s"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.input, func(t *testing.T) {
			response := core.Call(context.Background(), tc.name, json.RawMessage(tc.input))
			if response.OK || response.Code != "INVALID_ARGUMENT" {
				t.Fatalf("got %#v, want INVALID_ARGUMENT", response)
			}
		})
	}
	if called {
		t.Fatal("backend called for invalid input")
	}
}

func TestRequestConnectionAcceptsReadFilePermission(t *testing.T) {
	core := NewCore(backendFunc(func(_ context.Context, req Request) (any, error) {
		if req.Name != RequestConnection {
			t.Fatalf("unexpected tool %q", req.Name)
		}
		return map[string]any{"session_id": "s1", "status": "approved"}, nil
	}))
	response := core.Call(context.Background(), RequestConnection, json.RawMessage(
		`{"device_id":"device-1","purpose":"read logs","permissions":["read_file"],"temporary_password":"0123456789abcdef0123456789abcdef"}`))
	if !response.OK || response.Code != "OK" {
		t.Fatalf("got %#v, want read_file request to be accepted", response)
	}
}

func TestCallStructuredResponses(t *testing.T) {
	core := NewCore(backendFunc(func(_ context.Context, req Request) (any, error) {
		if req.Name != GetDeviceStatus {
			t.Fatalf("backend received unexpected tool %q", req.Name)
		}
		return map[string]any{"device_id": "device-1", "online": true}, nil
	}))
	response := core.Call(context.Background(), GetDeviceStatus, json.RawMessage(`{"device_id":"device-1"}`))
	if !response.OK || response.Code != "OK" {
		t.Fatalf("got %#v, want OK", response)
	}

	core = NewCore(backendFunc(func(context.Context, Request) (any, error) {
		return nil, &Error{Code: "DEVICE_OFFLINE", Message: "device is offline"}
	}))
	response = core.Call(context.Background(), GetDeviceStatus, json.RawMessage(`{"device_id":"device-1"}`))
	if response.OK || response.Code != "DEVICE_OFFLINE" {
		t.Fatalf("got %#v, want DEVICE_OFFLINE", response)
	}

	core = NewCore(backendFunc(func(context.Context, Request) (any, error) {
		return nil, errors.New("secret internal details")
	}))
	response = core.Call(context.Background(), GetDeviceStatus, json.RawMessage(`{"device_id":"device-1"}`))
	if response.Code != "INTERNAL" || response.Message != "tool request failed" {
		t.Fatalf("internal error leaked or mapped incorrectly: %#v", response)
	}
}

func TestMissingBackendIsExplicit(t *testing.T) {
	response := NewCore(nil).Call(context.Background(), ListDevices, json.RawMessage(`{}`))
	if response.OK || response.Code != "NOT_CONFIGURED" {
		t.Fatalf("got %#v, want NOT_CONFIGURED", response)
	}
}
