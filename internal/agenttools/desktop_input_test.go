package agenttools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseKeyChord(t *testing.T) {
	valid := map[string][]uint16{
		"a":             {0x41},
		"ENTER":         {0x0D},
		"ctrl+a":        {0x11, 0x41},
		"ctrl+shift+o":  {0x11, 0x10, 0x4F},
		"alt+f4":        {0x12, 0x73},
		"win+r":         {0x5B, 0x52},
		"shift+tab":     {0x10, 0x09},
		"ctrl+space":    {0x11, 0x20},
		"ctrl+alt+del":  nil, // "del" 不是白名单键名
		"ctrl+pagedown": {0x11, 0x22},
		"f12":           {0x7B},
	}
	for spec, want := range valid {
		got, err := parseKeyChord(spec)
		if want == nil {
			if err == nil {
				t.Fatalf("%q: expected rejection", spec)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", spec, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%q: got %v want %v", spec, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%q: got %v want %v", spec, got, want)
			}
		}
	}

	invalid := []string{
		"", " ", "ctrl", "ctrl+", "+a", "a+ctrl", "ctrl+a+b", "ctrl+ctrl+a",
		"ctrl+shift+alt+win+x", "ctrl+supercmd+a", "a+a", strings.Repeat("a", 65),
	}
	for _, spec := range invalid {
		if codes, err := parseKeyChord(spec); err == nil {
			t.Fatalf("%q: expected rejection, got %v", spec, codes)
		}
	}
}

func TestKeyChordsAreCaseAndSpaceInsensitive(t *testing.T) {
	for _, spec := range []string{"CTRL+SHIFT+O", " ctrl + shift + o "} {
		codes, err := parseKeyChord(spec)
		if err != nil {
			t.Fatalf("%q: %v", spec, err)
		}
		if len(codes) != 3 || codes[2] != 0x4F {
			t.Fatalf("%q: got %v", spec, codes)
		}
	}
}

func TestInvokeWaitLocalWithoutBackend(t *testing.T) {
	core := NewCore(nil)
	started := time.Now()
	response := core.Call(context.Background(), Wait, json.RawMessage(`{"ms":60}`))
	if !response.OK || response.Code != "OK" {
		t.Fatalf("%+v", response)
	}
	data, _ := response.Data.(map[string]any)
	if waited, _ := data["waited_ms"].(int); waited < 50 {
		t.Fatalf("waited_ms = %v", data["waited_ms"])
	}
	if elapsed := time.Since(started); elapsed < 50*time.Millisecond {
		t.Fatalf("returned too early: %v", elapsed)
	}
}

func TestInvokeWaitReturnsOnCancellation(t *testing.T) {
	core := NewCore(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	response := core.Call(ctx, Wait, json.RawMessage(`{"ms":5000}`))
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation ignored: %v", elapsed)
	}
	if !response.OK {
		t.Fatalf("%+v", response)
	}
	data, _ := response.Data.(map[string]any)
	if data["cancelled"] != true {
		t.Fatalf("expected cancelled result, got %v", data)
	}
}

func TestBatchOneValidation(t *testing.T) {
	bad := []struct{ tool, input string }{
		{PressKeys, `{"session_id":"s"}`},
		{PressKeys, `{"session_id":"s","keys":"a","repeat":0}`},
		{PressKeys, `{"session_id":"s","keys":"a","repeat":21}`},
		{PressKeys, `{"session_id":"s","keys":"a","interval_ms":-1}`},
		{PressKeys, `{"session_id":"s","keys":"a","interval_ms":1001}`},
		{PressKeys, `{"session_id":"s","keys":"a","hold":"true"}`},
		{MoveMouse, `{"session_id":"s","x":0}`},
		{MoveMouse, `{"session_id":"s","x":-1,"y":0}`},
		{MoveMouse, `{"session_id":"s","x":0,"y":0,"settle_ms":3001}`},
		{Wait, `{}`},
		{Wait, `{"ms":-1}`},
		{Wait, `{"ms":25001}`},
		{WaitFor, `{"session_id":"s","condition":"window_title"}`},
		{WaitFor, `{"session_id":"s","condition":"cpu_usage","value":"x"}`},
		{WaitFor, `{"session_id":"s","condition":"process","value":"x","timeout_seconds":0}`},
		{WaitFor, `{"session_id":"s","condition":"process","value":"x","timeout_seconds":26}`},
	}
	for _, tc := range bad {
		if err := validateInput(json.RawMessage(tc.input), tc.tool); err == nil {
			t.Fatalf("%s accepted %s", tc.tool, tc.input)
		}
	}

	good := []struct{ tool, input string }{
		{PressKeys, `{"session_id":"s","keys":"ctrl+shift+o"}`},
		{PressKeys, `{"session_id":"s","keys":"alt+f4","repeat":20,"interval_ms":0}`},
		{MoveMouse, `{"session_id":"s","x":0,"y":0}`},
		{MoveMouse, `{"session_id":"s","x":10,"y":10,"settle_ms":3000}`},
		{Wait, `{"ms":0}`},
		{Wait, `{"ms":25000}`},
		{WaitFor, `{"session_id":"s","condition":"window_title","value":"记事本"}`},
		{WaitFor, `{"session_id":"s","condition":"path_exists","value":"C:\\Windows","timeout_seconds":25}`},
	}
	for _, tc := range good {
		if err := validateInput(json.RawMessage(tc.input), tc.tool); err != nil {
			t.Fatalf("%s rejected %s: %v", tc.tool, tc.input, err)
		}
	}
}

func TestMoveMouseRequiresScreenshot(t *testing.T) {
	core := NewCore(backendFuncStub{})
	_, err := core.moveMouse(context.Background(), json.RawMessage(`{"session_id":"s","x":1,"y":1}`))
	toolErr, ok := err.(*Error)
	if !ok || toolErr.Code != "SCREENSHOT_REQUIRED" {
		t.Fatalf("got %v", err)
	}
}

// backendFuncStub 只用于满足非空后端检查，不应被调用。
type backendFuncStub struct{}

func (backendFuncStub) Invoke(context.Context, Request) (any, error) {
	return nil, &Error{Code: "UNEXPECTED_CALL", Message: "backend should not be called"}
}
