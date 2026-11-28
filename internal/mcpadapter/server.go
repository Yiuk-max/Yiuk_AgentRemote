// Package mcpadapter 将 agenttools 工具接口适配为 MCP 服务。
package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"agentremote/internal/agenttools"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewServer 将 agenttools 中的工具目录注册为 MCP 服务，并转换工具调用及其返回值。
//
// handler 的签名是固定的，拿不到工具名，所以用闭包工厂：每注册一个工具就生成一个
// “记住自己叫什么”的 handler，避免给 23 个工具各写一份几乎相同的样板代码。
func NewServer(core *agenttools.Core) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "agentremote", Version: "0.1.0"}, nil)
	newHandler := func(toolName string) mcp.ToolHandler {
		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			response := core.Call(ctx, toolName, req.Params.Arguments)
			content, err := json.Marshal(response)
			if err != nil {
				return nil, fmt.Errorf("encode tool response: %w", err)
			}
			contents := []mcp.Content{&mcp.TextContent{Text: string(content)}}
			for _, image := range response.Images {
				contents = append(contents, &mcp.ImageContent{MIMEType: image.MIMEType, Data: image.Data})
			}
			return &mcp.CallToolResult{
				Content:           contents,
				StructuredContent: response,
				IsError:           !response.OK,
			}, nil
		}
	}
	for _, spec := range core.Tools() {
		server.AddTool(&mcp.Tool{
			Name:         spec.Name,
			Description:  spec.Description,
			InputSchema:  spec.InputSchema,
			OutputSchema: spec.OutputSchema,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    spec.Annotations.ReadOnly,
				DestructiveHint: spec.Annotations.Destructive,
				IdempotentHint:  spec.Annotations.Idempotent,
				OpenWorldHint:   spec.Annotations.OpenWorld,
			},
		}, newHandler(spec.Name))
	}
	return server
}

// Run 通过标准输入输出启动 MCP 服务，直到上下文取消或传输发生错误。
func Run(ctx context.Context, core *agenttools.Core) error {
	return NewServer(core).Run(ctx, &mcp.StdioTransport{})
}
