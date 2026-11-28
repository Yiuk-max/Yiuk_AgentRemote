package mcpadapter

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"agentremote/internal/agenttools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type backendFunc func(context.Context, agenttools.Request) (any, error)

func (f backendFunc) Invoke(ctx context.Context, req agenttools.Request) (any, error) {
	return f(ctx, req)
}

func TestMCPListsToolsAndReturnsStructuredToolResponse(t *testing.T) {
	core := agenttools.NewCore(backendFunc(func(_ context.Context, req agenttools.Request) (any, error) {
		if req.Name != agenttools.GetDeviceStatus {
			t.Fatalf("unexpected tool %q", req.Name)
		}
		return map[string]any{"device_id": "device-1", "online": true}, nil
	}))
	server := NewServer(core)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	var toolNames []string
	for tool, err := range clientSession.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		toolNames = append(toolNames, tool.Name)
		if tool.OutputSchema == nil {
			t.Fatalf("%s is missing an output schema", tool.Name)
		}
		if tool.Name == agenttools.ListDevices && (tool.Annotations == nil || !tool.Annotations.ReadOnlyHint) {
			t.Fatal("list_devices should be annotated read-only for pi permission policies")
		}
		if tool.Name == agenttools.DownloadFile && (tool.Annotations == nil || tool.Annotations.DestructiveHint == nil) {
			t.Fatal("download_file should declare a destructive hint")
		}
	}
	if len(toolNames) != 24 {
		t.Fatalf("got %d tools, want 24: %v", len(toolNames), toolNames)
	}

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      agenttools.GetDeviceStatus,
		Arguments: map[string]any{"device_id": "device-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected MCP tool error: %#v", result.Content)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected MCP content type %T", result.Content[0])
	}
	var response agenttools.Response
	if err := json.Unmarshal([]byte(text.Text), &response); err != nil {
		t.Fatalf("decode structured result: %v", err)
	}
	if !response.OK || response.Code != "OK" {
		t.Fatalf("got %#v, want successful tool response", response)
	}
}

func TestMCPRunsLocalWaitWithoutBackend(t *testing.T) {
	server := NewServer(agenttools.NewCore(nil))
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	// wait 不依赖任何后端：没有配置后端时也必须成功。
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agenttools.Wait, Arguments: map[string]any{"ms": 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected MCP tool error: %#v", result.Content)
	}
	var response agenttools.Response
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("unexpected content %T", result.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), &response); err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("got %#v", response)
	}
}

func TestMCPRejectsBadKeyChordBeforeBackend(t *testing.T) {
	called := false
	core := agenttools.NewCore(backendFunc(func(_ context.Context, req agenttools.Request) (any, error) {
		called = true
		return map[string]any{}, nil
	}))
	response := core.Call(context.Background(), agenttools.PressKeys, json.RawMessage(`{"session_id":"s","keys":"ctrl"}`))
	if called {
		t.Fatal("backend must not be called for an invalid key chord")
	}
	if response.OK || response.Code != "INVALID_ARGUMENT" {
		t.Fatalf("got %#v", response)
	}
}

func TestMCPComposesPressKeysIntoExecuteCommand(t *testing.T) {
	var script string
	core := agenttools.NewCore(backendFunc(func(_ context.Context, req agenttools.Request) (any, error) {
		switch req.Name {
		case agenttools.ExecuteCommand:
			var input struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(req.Input, &input); err != nil {
				t.Fatal(err)
			}
			script = input.Command
			return map[string]any{"request_id": "r1", "status": "running"}, nil
		case agenttools.GetCommandResult:
			return map[string]any{"status": "succeeded", "stdout": `{"sent":true}`}, nil
		default:
			t.Fatalf("unexpected tool %q", req.Name)
		}
		return nil, nil
	}))
	response := core.Call(context.Background(), agenttools.PressKeys, json.RawMessage(`{"session_id":"s","keys":"ctrl+l"}`))
	if !response.OK {
		t.Fatalf("got %#v", response)
	}
	if !strings.Contains(script, "Yiuk: press_keys") {
		t.Fatalf("script does not look like press_keys: %q", script)
	}
	// ctrl+l 必须变成白名单里的 VK 码，而不是把原文透传进脚本。
	if !strings.Contains(script, "0x11,0x4C") {
		t.Fatalf("script is missing the ctrl+l virtual key codes: %q", script)
	}
}

func TestMCPDispatchesRunCommandToTheMatchingBackendTool(t *testing.T) {
	core := agenttools.NewCore(backendFunc(func(_ context.Context, req agenttools.Request) (any, error) {
		if req.Name != agenttools.RunCommand {
			t.Fatalf("unexpected tool %q", req.Name)
		}
		return map[string]any{"status": "succeeded"}, nil
	}))
	server := NewServer(core)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: agenttools.RunCommand,
		Arguments: map[string]any{
			"device_id":          "device-1",
			"temporary_password": "0123456789abcdef0123456789abcdef",
			"command":            "whoami",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected MCP tool error: %#v", result.Content)
	}
	response := result.StructuredContent.(map[string]any)
	if response["code"] != "OK" {
		t.Fatalf("got %#v, want successful tool response", response)
	}
}

func TestMCPDispatchesDownloadFileAndReturnsLocalPath(t *testing.T) {
	core := agenttools.NewCore(backendFunc(func(_ context.Context, req agenttools.Request) (any, error) {
		if req.Name != agenttools.DownloadFile {
			t.Fatalf("unexpected tool %q", req.Name)
		}
		return map[string]any{"local_path": "/tmp/yiuk/server.log", "size": 12, "status": "succeeded"}, nil
	}))
	server := NewServer(core)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      agenttools.DownloadFile,
		Arguments: map[string]any{"session_id": "session-1", "remote_path": `C:\logs\server.log`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected MCP tool error: %#v", result.Content)
	}
	response := result.StructuredContent.(map[string]any)
	data := response["data"].(map[string]any)
	if data["local_path"] != "/tmp/yiuk/server.log" {
		t.Fatalf("unexpected structured download result: %#v", response)
	}
}

func TestMCPReportsBackendUnavailableAsToolError(t *testing.T) {
	server := NewServer(agenttools.NewCore(nil))
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	clientSession, err := mcp.NewClient(&mcp.Implementation{Name: "test-client"}, nil).Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: agenttools.ListDevices, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("expected unavailable backend to be reported as tool error")
	}
	text := result.Content[0].(*mcp.TextContent).Text
	var response agenttools.Response
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}
	if response.Code != "NOT_CONFIGURED" {
		t.Fatalf("got error code %q, want NOT_CONFIGURED", response.Code)
	}
}
