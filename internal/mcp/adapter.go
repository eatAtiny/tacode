package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"agentic/internal/tool"

	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// mcpCallTimeout MCP 工具单次调用超时（与 shell 工具 30s 一致）。
const mcpCallTimeout = 30 * time.Second

// mcpToolAdapter 把 MCP 工具适配成 tool.Tool。
// queryLoop 通过既有 Tool 接口无感知调用 MCP 工具。
type mcpToolAdapter struct {
	name        string         // 注册名（server_tool，如 "fetch_fetch"）
	description string         // MCP 工具描述
	schema      map[string]any // inputSchema → Parameters()
	client      *mcpgo.Client  // 所属 server 的 client
	toolName    string         // MCP 原始工具名（如 "fetch"）
}

// ── tool.Tool 接口 ──

func (a *mcpToolAdapter) Name() string      { return a.name }
func (a *mcpToolAdapter) Aliases() []string { return nil }

func (a *mcpToolAdapter) Description() string {
	return a.description
}

func (a *mcpToolAdapter) Parameters() map[string]any {
	return a.schema
}

// CheckPermission MCP 工具默认放行（如 fetch 是可信只读工具）。
func (a *mcpToolAdapter) CheckPermission(args string) tool.PermissionResult {
	return tool.PermissionResult{Allow: true}
}

func (a *mcpToolAdapter) PromptGuide() string { return "" }

// IsConcurrencySafe MCP 工具保守串行（server 是外部进程，并发可能撞内部状态）。
func (a *mcpToolAdapter) IsConcurrencySafe(args string) bool { return false }

// IsReadOnly 保守返回 false（不知道 MCP 工具是否有副作用）。
func (a *mcpToolAdapter) IsReadOnly(args string) bool { return false }

// ResultLimit MCP 工具输出上限 12000 字符（与 git 工具一致）。
func (a *mcpToolAdapter) ResultLimit() int { return 12000 }

// Execute 调用 MCP server 的 tools/call。
// 带 mcpCallTimeout 超时：卡死的 MCP server 不应永久阻塞 queryLoop
// （与 shell 工具的 30s 超时一致）。
func (a *mcpToolAdapter) Execute(args string) (string, error) {
	// ── 解析参数 JSON → map[string]any ──
	var arguments map[string]any
	if err := json.Unmarshal([]byte(args), &arguments); err != nil {
		return "", fmt.Errorf("parse mcp args: %w", err)
	}

	// ── 调用 MCP 工具（带超时） ──
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = a.toolName
	callReq.Params.Arguments = arguments

	ctx, cancel := context.WithTimeout(context.Background(), mcpCallTimeout)
	defer cancel()
	result, err := a.client.CallTool(ctx, callReq)
	if err != nil {
		return "", fmt.Errorf("mcp tool %s call failed: %w", a.name, err)
	}

	// ── 提取文本内容 ──
	var parts []string
	for _, content := range result.Content {
		parts = append(parts, mcp.GetTextFromContent(content))
	}
	text := strings.Join(parts, "\n")
	if result.IsError {
		return "", fmt.Errorf("mcp tool %s returned error: %s", a.name, text)
	}
	if strings.TrimSpace(text) == "" {
		return "(无输出)", nil
	}
	return text, nil
}
