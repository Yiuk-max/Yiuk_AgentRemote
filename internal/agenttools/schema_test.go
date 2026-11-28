package agenttools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// 这条测试盯的是「schema 与运行时校验漂移」——历史上出现过：schema 说
// device_id 必填、且没有 device_name 属性，而校验器与后端都允许 device_name。
// 现在校验完全由 schema 驱动，所以只要这里通过，两边就不可能再分叉。
func TestToolSchemasMatchRuntimeValidation(t *testing.T) {
	if len(compiledSchemas) != len(toolSpecs) {
		t.Fatalf("compiled %d schemas for %d tools", len(compiledSchemas), len(toolSpecs))
	}
	for _, spec := range toolSpecs {
		if _, ok := compiledSchemas[spec.Name]; !ok {
			t.Fatalf("%s has no compiled schema", spec.Name)
		}
	}
}

func TestDeviceNameIsAcceptedWhereDeviceIDIs(t *testing.T) {
	called := map[string]json.RawMessage{}
	core := NewCore(backendFunc(func(_ context.Context, req Request) (any, error) {
		called[req.Name] = req.Input
		return map[string]any{"ok": true}, nil
	}))
	accepted := []struct{ name, input string }{
		{GetDeviceStatus, `{"device_name":"lab-pc"}`},
		{RequestConnection, `{"device_name":"lab-pc","purpose":"install driver"}`},
		// run_command 的临时密码是可选的：不带就走审批。
		{RunCommand, `{"device_name":"lab-pc","command":"whoami"}`},
		{RunCommand, `{"device_name":"lab-pc","command":"whoami","temporary_password":"0123456789abcdef0123456789abcdef"}`},
	}
	for _, tc := range accepted {
		response := core.Call(context.Background(), tc.name, json.RawMessage(tc.input))
		if !response.OK {
			t.Fatalf("%s %s: got %#v, want accepted", tc.name, tc.input, response)
		}
	}

	// 两个都给、或一个都不给，都必须在本地被拒（oneOf）。
	rejected := []struct{ name, input string }{
		{GetDeviceStatus, `{"device_id":"d","device_name":"d"}`},
		{GetDeviceStatus, `{}`},
		{RequestConnection, `{"purpose":"help"}`},
		{RequestConnection, `{"device_id":"d","device_name":"d","purpose":"help"}`},
		{RunCommand, `{"command":"whoami"}`},
	}
	for _, tc := range rejected {
		response := core.Call(context.Background(), tc.name, json.RawMessage(tc.input))
		if response.OK || response.Code != "INVALID_ARGUMENT" {
			t.Fatalf("%s %s: got %#v, want INVALID_ARGUMENT", tc.name, tc.input, response)
		}
	}
}

// schema 里的数字必须真在生效，不能只是写给人看的。
func TestSchemaBoundsAreEnforced(t *testing.T) {
	accepted := []struct{ name, input string }{
		{Wait, `{"ms":0}`},
		{Wait, `{"ms":25000}`},
		{ExecuteCommand, `{"session_id":"s","command":"whoami","timeout_seconds":3600}`},
		{TypeText, `{"session_id":"s","text":"` + strings.Repeat("字", 4000) + `"}`},
	}
	for _, tc := range accepted {
		if err := validateInput(json.RawMessage(tc.input), tc.name); err != nil {
			t.Fatalf("%s %s boundary should be accepted: %v", tc.name, tc.input, err)
		}
	}
	rejected := []struct{ name, input string }{
		{Wait, `{"ms":25001}`},
		{ExecuteCommand, `{"session_id":"s","command":"whoami","timeout_seconds":3601}`},
		{TypeText, `{"session_id":"s","text":"` + strings.Repeat("字", 4001) + `"}`},
		// 只由空白字符组成的必填字符串应被判为非法（schema 里的 pattern:"\\S"）。
		{ExecuteCommand, `{"session_id":"  ","command":"whoami"}`},
		{RunCommand, `{"device_id":"d","command":"  "}`},
	}
	for _, tc := range rejected {
		if err := validateInput(json.RawMessage(tc.input), tc.name); err == nil {
			t.Fatalf("%s %s should be rejected", tc.name, tc.input)
		}
	}
}

// 报错必须能指出是哪个字段坏了，否则 Agent 只能瞎改。
func TestSchemaErrorsNameTheField(t *testing.T) {
	err := validateInput(json.RawMessage(`{"session_id":"s","wait_seconds":90}`), GetSessionStatus)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "wait_seconds") {
		t.Fatalf("message should name the field, got %q", err)
	}
	if strings.Contains(err.Error(), "jsonschema validation failed") {
		t.Fatalf("message should not leak the library's header: %q", err)
	}
}
