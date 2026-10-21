package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"yiuk-switchboard/internal/agenttools"
)

func NewServer(core *agenttools.Core) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "yiuk-switchboard", Version: "0.1.0"}, nil)
	newHandler := func(toolName string) mcp.ToolHandler {
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			response := core.Call(ctx, toolName, req.Params.Arguments)
			content, err := json.Marshal(response)
			if err != nil {
				return nil, fmt.Errorf("encode tool response: %w", err)
			}
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: string(content)}},
				StructuredContent: response,
				IsError:           !response.OK,
			}, nil
		}
	}
	for _, spec := range core.Tools() {
		server.AddTool(&mcp.Tool{
			Name:        spec.Name,
			Description: spec.Description,
			InputSchema: spec.InputSchema,
		}, newHandler(spec.Name))
	}
	return server
}

func Run(ctx context.Context, core *agenttools.Core) error {
	return NewServer(core).Run(ctx, &mcp.StdioTransport{})
}
