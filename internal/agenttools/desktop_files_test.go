package agenttools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

type backendFuncStub2 func(context.Context, Request) (any, error)

func (f backendFuncStub2) Invoke(ctx context.Context, req Request) (any, error) { return f(ctx, req) }

// list_dir / stat_path 都是组合工具：脚本通过 execute_command 提交，
// 输出从 get_command_result 取回。下面用桩后端验证解析与错误映射。
func fileToolBackend(t *testing.T, scriptOut string, seenScript *string) Backend {
	t.Helper()
	return backendFuncStub2(func(_ context.Context, req Request) (any, error) {
		switch req.Name {
		case ExecuteCommand:
			var input struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(req.Input, &input); err != nil {
				t.Fatal(err)
			}
			if seenScript != nil {
				*seenScript = input.Command
			}
			return map[string]any{"request_id": "r1", "status": "running"}, nil
		case GetCommandResult:
			return map[string]any{"status": "succeeded", "stdout": scriptOut}, nil
		default:
			t.Fatalf("unexpected tool %q", req.Name)
		}
		return nil, nil
	})
}

func TestListDirParsesEntries(t *testing.T) {
	var script string
	payload := `{"kind":"ok","truncated":true,"returned":2,"entries":[
		{"name":"a.txt","type":"file","size":12,"modified":"2026-01-02T03:04:05.0000000Z","target":null},
		{"name":"link","type":"symlink","size":null,"modified":"2026-01-02T03:04:05.0000000Z","target":"C:\\real"}]}`
	payload = strings.Join(strings.Fields(payload), "")
	core := NewCore(fileToolBackend(t, payload, &script))
	response := core.Call(context.Background(), ListDir, json.RawMessage(`{"session_id":"s","path":"C:\\中文 目录"}`))
	if !response.OK {
		t.Fatalf("%+v", response)
	}
	data := response.Data.(map[string]any)
	entries := data["entries"].([]map[string]any)
	if len(entries) != 2 || data["truncated"] != true {
		t.Fatalf("unexpected result %+v", data)
	}
	if entries[0]["name"] != "a.txt" || entries[0]["size"] != int64(12) {
		t.Fatalf("file entry wrong: %+v", entries[0])
	}
	if _, ok := entries[0]["target"]; ok {
		t.Fatalf("empty target must be omitted: %+v", entries[0])
	}
	if entries[1]["type"] != "symlink" || entries[1]["target"] != `C:\real` {
		t.Fatalf("symlink entry wrong: %+v", entries[1])
	}
	// 路径必须以 base64 进入脚本，不能被当成 PowerShell 代码
	if strings.Contains(script, "中文") {
		t.Fatal("raw path leaked into the script")
	}
	if !strings.Contains(script, base64.StdEncoding.EncodeToString([]byte(`C:\中文 目录`))) {
		t.Fatal("expected base64-encoded path in the script")
	}
}

func TestListDirSymlinkRootRequiresRealPath(t *testing.T) {
	core := NewCore(fileToolBackend(t, `{"kind":"symlink_root","target":"D:\\real"}`, nil))
	response := core.Call(context.Background(), ListDir, json.RawMessage(`{"session_id":"s","path":"C:\\link"}`))
	if response.OK || response.Code != "INVALID_ARGUMENT" {
		t.Fatalf("%+v", response)
	}
	if !strings.Contains(response.Hint, `D:\real`) {
		t.Fatalf("hint must point at the real path: %q", response.Hint)
	}
}

func TestListDirOnFileSuggestsStatPath(t *testing.T) {
	core := NewCore(fileToolBackend(t, `{"kind":"not_directory"}`, nil))
	response := core.Call(context.Background(), ListDir, json.RawMessage(`{"session_id":"s","path":"C:\\a.txt"}`))
	if response.OK || !strings.Contains(response.Hint, "stat_path") {
		t.Fatalf("%+v", response)
	}
}

func TestStatPathMissingCarriesHint(t *testing.T) {
	core := NewCore(fileToolBackend(t, `{"kind":"missing"}`, nil))
	response := core.Call(context.Background(), StatPath, json.RawMessage(`{"session_id":"s","path":"C:\\nope"}`))
	if response.OK || response.Code != "FILE_NOT_FOUND" || response.Hint == "" {
		t.Fatalf("%+v", response)
	}
}

func TestStatPathReturnsSizeAndLinkTarget(t *testing.T) {
	core := NewCore(fileToolBackend(t, `{"kind":"ok","type":"symlink","size":null,"modified":"2026-01-02T03:04:05Z","target":"C:\\real"}`, nil))
	response := core.Call(context.Background(), StatPath, json.RawMessage(`{"session_id":"s","path":"C:\\link"}`))
	if !response.OK {
		t.Fatalf("%+v", response)
	}
	data := response.Data.(map[string]any)
	if data["type"] != "symlink" || data["target"] != `C:\real` {
		t.Fatalf("%+v", data)
	}
	if _, ok := data["size"]; ok {
		t.Fatalf("directory size must be omitted: %+v", data)
	}
}
