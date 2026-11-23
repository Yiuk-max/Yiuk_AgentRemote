package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestScreenshotThenClickMapsCoordinates(t *testing.T) {
	localImage := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(localImage, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	var lastCommand string
	polls := 0
	core := NewCore(backendFunc(func(_ context.Context, req Request) (any, error) {
		switch req.Name {
		case ExecuteCommand:
			var input struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(req.Input, &input)
			lastCommand = input.Command
			return map[string]any{"request_id": "r1", "status": "queued"}, nil
		case GetCommandResult:
			polls++
			if strings.Contains(lastCommand, "take_screenshot") {
				// 平台后端以 chunks 返回输出
				return map[string]any{"status": "succeeded", "chunks": []map[string]any{
					{"stream": "stdout", "data": `{"path":"C:\\t\\s.png","left":-1920,"top":0,"screen_width":3840,"screen_height":1080,"width":1568,"height":441}` + "\r\n"},
				}}, nil
			}
			// 本地后端以 stdout 字符串返回输出
			return map[string]any{"status": "succeeded", "stdout": `{"x":0,"y":0}`}, nil
		case DownloadFile:
			return map[string]any{"local_path": localImage}, nil
		}
		t.Fatalf("unexpected tool %q", req.Name)
		return nil, nil
	}))

	early := core.Call(context.Background(), Click, json.RawMessage(`{"session_id":"s1","x":10,"y":10}`))
	if early.OK || early.Code != "SCREENSHOT_REQUIRED" {
		t.Fatalf("got %#v, want SCREENSHOT_REQUIRED", early)
	}

	shot := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`))
	if !shot.OK || len(shot.Images) != 1 || string(shot.Images[0].Data) != "png-bytes" || shot.Images[0].MIMEType != "image/png" {
		t.Fatalf("unexpected screenshot response %#v", shot)
	}
	if !strings.Contains(lastCommand, "$maxWidth = 1568") {
		t.Fatal("default max width was not applied to the script")
	}
	if _, err := os.Stat(localImage); !os.IsNotExist(err) {
		t.Fatal("downloaded screenshot should be removed after reading")
	}
	encoded, _ := json.Marshal(shot)
	if strings.Contains(string(encoded), "png-bytes") {
		t.Fatal("image bytes must not leak into the JSON text result")
	}

	out := core.Call(context.Background(), Click, json.RawMessage(`{"session_id":"s1","x":784,"y":220,"double":true,"button":"right"}`))
	if !out.OK {
		t.Fatalf("click failed: %#v", out)
	}
	data := out.Data.(map[string]any)
	if data["screen_x"] != 0 || data["screen_y"] != 539 {
		t.Fatalf("got screen (%v,%v), want (0,539)", data["screen_x"], data["screen_y"])
	}
	if !strings.Contains(lastCommand, "SetCursorPos(0, 539)") || !strings.Contains(lastCommand, "mouse_event(8,") || !strings.Contains(lastCommand, "-lt 2;") {
		t.Fatalf("click script not parameterised as expected:\n%s", lastCommand)
	}

	outside := core.Call(context.Background(), Click, json.RawMessage(`{"session_id":"s1","x":1568,"y":0}`))
	if outside.OK || outside.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %#v, want out-of-bounds rejection", outside)
	}
}

func TestTypeTextEmbedsTextAsBase64(t *testing.T) {
	text := "你好 👋\n'; Remove-Item C:\\ -Recurse; '$(whoami)"
	var command string
	core := NewCore(backendFunc(func(_ context.Context, req Request) (any, error) {
		if req.Name == ExecuteCommand {
			var input struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(req.Input, &input)
			command = input.Command
			return map[string]any{"request_id": "r1"}, nil
		}
		return map[string]any{"status": "succeeded", "stdout": `{"characters":30}`}, nil
	}))
	input, _ := json.Marshal(map[string]any{"session_id": "s1", "text": text, "method": "paste", "press_enter": true})
	response := core.Call(context.Background(), TypeText, input)
	if !response.OK {
		t.Fatalf("type_text failed: %#v", response)
	}
	if strings.Contains(command, "Remove-Item") || strings.Contains(command, "whoami") {
		t.Fatal("text must not appear verbatim in the script")
	}
	if !strings.Contains(command, base64.StdEncoding.EncodeToString([]byte(text))) {
		t.Fatal("script does not carry the base64 text")
	}
	if !strings.Contains(command, "if ('paste' -eq 'paste')") || !strings.Contains(command, "if ($true)") {
		t.Fatalf("method / press_enter not applied:\n%s", command)
	}
	if got := response.Data.(map[string]any)["characters"]; got != utf8.RuneCountInString(text) {
		t.Fatalf("characters = %v", got)
	}
}

func TestDesktopScriptsAreASCII(t *testing.T) {
	for name, script := range map[string]string{"screenshot": screenshotScript, "click": clickScript, "type_text": typeTextScript} {
		for i, r := range script {
			if r > 127 {
				t.Fatalf("%s script has non-ASCII %q at byte %d; PowerShell 5.1 misreads it without a BOM", name, r, i)
			}
		}
	}
}

func TestDesktopCommandFailureSurfacesStderr(t *testing.T) {
	core := NewCore(backendFunc(func(_ context.Context, req Request) (any, error) {
		if req.Name == ExecuteCommand {
			return map[string]any{"request_id": "r1"}, nil
		}
		return map[string]any{"status": "failed", "stderr": "The handle is invalid."}, nil
	}))
	response := core.Call(context.Background(), TakeScreenshot, json.RawMessage(`{"session_id":"s1"}`))
	if response.OK || response.Code != "DESKTOP_COMMAND_FAILED" || !strings.Contains(response.Message, "handle is invalid") {
		t.Fatalf("got %#v", response)
	}
}
