// Package mcp 提供 MCP（Model Context Protocol）客户端支持。
//
// 让外部 MCP server（如 mcp-server-fetch 提供 WebFetch）的工具
// 注册进 agent 的 tool.Registry，queryLoop 通过既有 Tool 接口无感知调用。
//
// 用法（main.go）：
//
//	mgr := mcp.New()
//	mgr.Connect(ctx, mcp.ServerConfig{Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-fetch"}})
//	for _, t := range mgr.Tools() {
//		tools.Register(t)
//	}
//	defer mgr.Close()
package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"

	"agentic/internal/tool"

	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// ServerConfig 描述一个 MCP server（子进程命令）。
type ServerConfig struct {
	Name    string   // server 名称（如 "fetch"）
	Command string   // 命令（如 "npx"）
	Args    []string // 参数（如 ["-y", "@modelcontextprotocol/server-fetch"]）
}

// serverConn 一个已连接的 MCP server。
type serverConn struct {
	cfg    ServerConfig
	client *mcpgo.Client
	tools  []tool.Tool // 该 server 暴露的工具适配器
}

// Manager 管理 MCP server 子进程和工具适配。
type Manager struct {
	conns []*serverConn
}

// New 创建 Manager（不启动任何 server）。
func New() *Manager {
	return &Manager{}
}

// Connect 启动一个 server 子进程，握手，枚举工具并构建适配器。
// 失败返回 error（调用方决定警告跳过还是退出）。
func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) error {
	name := cfg.Name
	if name == "" {
		name = cfg.Command
	}

	// ── 创建 stdio client（自动启动子进程 transport） ──
	c, err := mcpgo.NewStdioMCPClient(cfg.Command, nil, cfg.Args...)
	if err != nil {
		return fmt.Errorf("create mcp client for %s: %w", name, err)
	}

	// drain server stderr，防止管道阻塞。
	if stderr, ok := mcpgo.GetStderr(c); ok {
		go drainStderr(stderr)
	}

	// ── 握手（必须最先调用） ──
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "agentic", Version: "0.1"}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		c.Close()
		return fmt.Errorf("initialize mcp server %s: %w", name, err)
	}

	// ── 枚举工具（自动分页） ──
	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		c.Close()
		return fmt.Errorf("list tools from %s: %w", name, err)
	}

	// ── 构建适配器 ──
	var adapters []tool.Tool
	for _, t := range toolsResult.Tools {
		adapters = append(adapters, &mcpToolAdapter{
			name:        mcpToolName(name, t.Name),
			description: t.Description,
			schema:      buildSchema(t),
			client:      c,
			toolName:    t.Name,
		})
	}

	m.conns = append(m.conns, &serverConn{cfg: cfg, client: c, tools: adapters})
	return nil
}

// Tools 返回所有已连接的 MCP 工具适配器。
func (m *Manager) Tools() []tool.Tool {
	var all []tool.Tool
	for _, conn := range m.conns {
		all = append(all, conn.tools...)
	}
	return all
}

// Close 关闭所有 server 子进程。
func (m *Manager) Close() error {
	var errs []string
	for _, conn := range m.conns {
		if err := conn.client.Close(); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", conn.cfg.Name, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("close mcp servers: %s", strings.Join(errs, "; "))
	}
	return nil
}

// mcpToolName 为 MCP 工具生成唯一注册名。
// 带 server 前缀避免不同 server 的同名工具冲突（如两个 server 都有 "fetch"）。
func mcpToolName(serverName, toolName string) string {
	return serverName + "_" + toolName
}

// buildSchema 把 MCP Tool 的 InputSchema 转成 tool.Parameters() 需要的 map。
func buildSchema(t mcp.Tool) map[string]any {
	schema := map[string]any{
		"type":       t.InputSchema.Type,
		"properties": t.InputSchema.Properties,
	}
	if len(t.InputSchema.Required) > 0 {
		schema["required"] = t.InputSchema.Required
	}
	return schema
}

// drainStderr 持续读取 server stderr，防止管道缓冲填满阻塞子进程。
func drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		if _, err := r.Read(buf); err != nil {
			return
		}
	}
}
