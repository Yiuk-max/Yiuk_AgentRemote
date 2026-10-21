package mcpadapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"yiuk-switchboard/internal/agenttools"
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
	}
	if len(toolNames) != 9 {
		t.Fatalf("got %d tools, want 9: %v", len(toolNames), toolNames)
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
