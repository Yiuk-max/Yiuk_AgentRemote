// Package mcpadapter 将 agenttools 工具接口适配为 MCP 服务。
package mcpadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"agentremote/internal/agenttools"
)

// NewServer 将 agenttools 中的工具目录注册为 MCP 服务，并转换工具调用及其返回值。
// newHandler 是闭包工厂：传入工具名，返回一个"记住这个工具名"的 handler。
// 这种写法避免了给每个工具写重复的 handler 代码，也解决了 handler 签名固定、没法直接传工具名的矛盾。
// 当客户端调用某个工具时，对应的闭包被触发：用自己捕获的 toolName 调 core.Call，再把结果转成 MCP 格式返回。
func NewServer(core *agenttools.Core) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "agentremote", Version: "0.1.0"}, nil)
	newHandler := func(toolName string) mcp.ToolHandler { // 创建一个闭包来捕获工具名称

		return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			response := core.Call(ctx, toolName, req.Params.Arguments) // 调用工具并获取响应
			content, err := json.Marshal(response)                     //序列化
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
	// 遍历工具目录并注册每个工具
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
