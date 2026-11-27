package platformclient_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentremote/internal/agenttools"
	"agentremote/internal/mcpadapter"
	"agentremote/internal/platformclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Opt in after building yiuk-question-interop. Exercises the real Qt MainWindow
// and user widgets through the MCP adapter and a real local WebSocket platform.
func TestMCPQuestionQtEndToEnd(t *testing.T) {
	executable := os.Getenv("YIUK_QUESTION_QT_EXE")
	if executable == "" {
		t.Skip("set YIUK_QUESTION_QT_EXE to built yiuk-question-interop executable")
	}
	address := startPlatform(t)
	root := t.TempDir()
	control := filepath.Join(root, "control.txt")
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable)
	qpa := os.Getenv("YIUK_QUESTION_QPA")
	if qpa == "" {
		qpa = "offscreen"
	}
	cmd.Env = append(os.Environ(), "LOCALAPPDATA="+root, "APPDATA="+root, "YIUK_PLATFORM_ADDRESS="+strings.TrimPrefix(address, "ws://"), "YIUK_PLATFORM_PASSWORD="+playerPassword, "QT_QPA_PLATFORM="+qpa, "YIUK_QUESTION_CONTROL="+control)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	events := make(chan map[string]any, 128)
	go func() {
		defer close(events)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			var event map[string]any
			if json.Unmarshal(scanner.Bytes(), &event) == nil {
				events <- event
			}
		}
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(stderr.String())
		}
	})
	var ready map[string]any
	for ready == nil {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Qt exited before ready")
			}
			if event["ready"] == true {
				ready = event
			}
			if event["error"] != nil {
				t.Fatal(event)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("Qt registration timeout")
		}
	}
	backend, err := platformclient.New(platformclient.Config{Address: address, PlayerPassword: playerPassword, AllowPlaintext: true, TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	var clientTransport mcp.Transport
	if mcpExe := os.Getenv("YIUK_QUESTION_MCP_EXE"); mcpExe != "" {
		command := exec.CommandContext(ctx, mcpExe)
		command.Env = append(os.Environ(), "LOCALAPPDATA="+root, "APPDATA="+root)
		clientTransport = &mcp.CommandTransport{Command: command, TerminateDuration: time.Second}
	} else {
		server := mcpadapter.NewServer(agenttools.NewCore(backend))
		ct, st := mcp.NewInMemoryTransports()
		ss, connectErr := server.Connect(ctx, st, nil)
		if connectErr != nil {
			t.Fatal(connectErr)
		}
		defer ss.Close()
		clientTransport = ct
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "qt-question-e2e", Version: "test"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError {
			t.Fatalf("%s: %+v", name, result.Content)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		var response struct {
			Data map[string]any `json:"data"`
		}
		if err = json.Unmarshal([]byte(text), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data
	}
	session := call(agenttools.RequestConnection, map[string]any{"device_id": ready["device_id"], "temporary_password": ready["password"], "purpose": "automated UI interaction"})
	sid := session["session_id"]
	for _, mode := range []string{"option", "text", "input", "long", "cancel", "close", "escape", "timeout", "wait"} {
		t.Run(mode, func(t *testing.T) {
			args := map[string]any{"session_id": sid, "question": mode, "options": []string{"默认位置", "D 盘", "暂不安装"}, "timeout_seconds": 15}
			if mode == "input" {
				delete(args, "options")
			}
			if mode == "long" {
				args["question"] = "long:" + strings.Repeat("请确认你的选择，", 100)
				args["options"] = []string{strings.Repeat("较长选项文字", 16), "普通选项", "第三个选项", "第四个选项", "第五个选项", "第六个选项"}
			}
			pending := call(agenttools.AskUser, args)
			rid := pending["request_id"]
			if pending["status"] != "pending" {
				t.Fatal(pending)
			}
			if mode == "wait" {
				call(agenttools.CloseSession, map[string]any{"session_id": sid})
			}
			deadline := time.Now().Add(20 * time.Second)
			var answer map[string]any
			for time.Now().Before(deadline) {
				answer = call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid})
				if answer["status"] != "pending" {
					break
				}
				time.Sleep(80 * time.Millisecond)
			}
			expected := "answered"
			if mode == "cancel" || mode == "close" || mode == "escape" || mode == "wait" {
				expected = "cancelled"
			}
			if mode == "timeout" {
				expected = "timed_out"
			}
			if answer["status"] != expected {
				var seen []map[string]any
				for deadline := time.Now().Add(500 * time.Millisecond); time.Now().Before(deadline); {
					select {
					case event, ok := <-events:
						if ok {
							seen = append(seen, event)
						}
					case <-time.After(50 * time.Millisecond):
					}
				}
				t.Fatalf("answer=%v events=%v stderr=%s", answer, seen, stderr.String())
			}
			if mode == "option" && (answer["answer"] != "D 盘" || answer["selected_index"] != float64(1)) {
				t.Fatal(answer)
			}
			if (mode == "text" || mode == "input" || mode == "long") && (answer["answer"] != "自定义答案 😀" || answer["answer_source"] != "text") {
				t.Fatal(answer)
			}
		})
	}
	// A second session verifies real device disconnect dismisses the live window.
	session = call(agenttools.RequestConnection, map[string]any{"device_id": ready["device_id"], "temporary_password": ready["password"], "purpose": "disconnect cleanup"})
	sid = session["session_id"]
	pending := call(agenttools.AskUser, map[string]any{"session_id": sid, "question": "wait"})
	rid := pending["request_id"]
	time.Sleep(200 * time.Millisecond)
	if err = os.WriteFile(control, []byte("disconnect"), 0600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var answer map[string]any
	for time.Now().Before(deadline) {
		answer = call(agenttools.GetUserReply, map[string]any{"session_id": sid, "request_id": rid})
		if answer["status"] != "pending" {
			break
		}
		time.Sleep(80 * time.Millisecond)
	}
	if answer["status"] != "result_unknown" {
		t.Fatal(answer)
	}
	_ = os.WriteFile(control, []byte("quit"), 0600)
	// Drain events, checking Qt's own UI assertions and lifecycle notifications.
	closed := map[string]int{}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				if closed["answered"] < 4 || closed["cancelled"] < 4 || closed["timed_out"] < 1 || closed["result_unknown"] < 1 {
					t.Fatal("missing Qt lifecycle events", closed)
				}
				return
			}
			if event["error"] != nil {
				t.Fatal(event)
			}
			if status, ok := event["finished"].(string); ok {
				closed[status]++
			}
		case <-timer.C:
			t.Fatal("Qt did not quit")
		}
	}
}
