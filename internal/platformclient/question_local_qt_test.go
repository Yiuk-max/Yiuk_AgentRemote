package platformclient_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/localbridge"
)

func TestLocalBridgeQuestionQtEndToEnd(t *testing.T) {
	executable := os.Getenv("YIUK_QUESTION_QT_EXE")
	if executable == "" {
		t.Skip("set YIUK_QUESTION_QT_EXE")
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable)
	control := filepath.Join(root, "control.txt")
	cmd.Env = append(os.Environ(), "LOCALAPPDATA="+root, "APPDATA="+root, "YIUK_PLATFORM_ADDRESS=", "YIUK_PLATFORM_PASSWORD="+playerPassword, "QT_QPA_PLATFORM=offscreen", "YIUK_QUESTION_LOCAL=1", "YIUK_QUESTION_CONTROL="+control)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	path := filepath.Join(root, "Yiuk", "DeviceAssist", "local-api.json")
	var cfg localbridge.Config
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		data, _ := os.ReadFile(path)
		if json.Unmarshal(data, &cfg) == nil && cfg.Token != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if cfg.Token == "" {
		t.Fatal("local bridge did not start")
	}
	core := agenttools.NewCore(localbridge.New(cfg))
	call := func(name string, args any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(args)
		r := core.Call(ctx, name, raw)
		if !r.OK {
			t.Fatal(r)
		}
		return r.Data.(map[string]any)
	}
	session := call(agenttools.RequestConnection, map[string]any{"device_id": "123456789", "purpose": "local UI test"})
	sid := session["session_id"]
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if call(agenttools.GetSessionStatus, map[string]any{"session_id": sid})["status"] == "active" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	pending := call(agenttools.AskUser, map[string]any{"session_id": sid, "question": "本地请求", "options": []string{"选项一", "选项二"}})
	rid := pending["request_id"]
	var result map[string]any
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		result = call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid})
		if result["status"] != "pending" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if result["status"] != "answered" || result["answer"] != "本地桥接回复" {
		t.Fatal(result)
	}
	call(agenttools.CloseSession, map[string]any{"session_id": sid})
	_ = os.WriteFile(control, []byte("quit"), 0600)
}
