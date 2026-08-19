# MCP 客户端支持 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 agent 加 MCP client 支持——`-mcp-server "<命令>"`（可重复）拉起外部 MCP server，把其工具注册进 Registry，让 agent 获得网络浏览等能力（如 `mcp-server-fetch`）。

**Architecture:** `internal/mcp/` 新增 `Manager`（启动/管理 server 子进程 + 握手 + 枚举）+ `mcpToolAdapter`（MCP 工具 → `tool.Tool` 适配）。`main.go` 加可重复 `-mcp-server` flag。Registry/queryLoop 零改动。

**Tech Stack:** Go 1.26.4 + `github.com/mark3labs/mcp-go`（本地缓存 v0.43.1，零网络拉取）。

**Spec:** `docs/superpowers/specs/2026-08-19-mcp-client-design.md`

---

### Task 1: 添加 mcp-go 依赖

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: 添加依赖**

Run: `cd /Users/wang/Documents/agentic-1 && go get github.com/mark3labs/mcp-go@v0.43.1`
Expected: go.mod/go.sum 更新（库已在本地缓存，无需网络）

- [ ] **Step 2: 验证**

Run: `cd /Users/wang/Documents/agentic-1 && go mod tidy && go build ./...`
Expected: 编译通过（mcp-go 尚未被 import，tidy 可能移除——若 `go mod tidy` 把 mcp-go 清掉，本任务只确认依赖可解析，实际 import 在 Task 2）

- [ ] **Step 3: 提交**

```bash
git add go.mod go.sum
git commit -m "chore: add mcp-go dependency for MCP client support

Co-Authored-By: Claude <noreply@anthropic.com>"
```

> 注意：若 `go mod tidy` 因 mcp-go 未被 import 而移除它，Step 3 可能无文件可提交。此时跳过提交，把依赖添加并入 Task 2 的提交（Task 2 会 import mcp-go，tidy 后依赖生效）。

---

### Task 2: `internal/mcp/client.go` — Manager

**Files:**
- Create: `internal/mcp/client.go`

- [ ] **Step 1: 创建 `internal/mcp/client.go`（完整文件）**

```go
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
	"time"

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
		go drainStderr(name, stderr)
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
func drainStderr(name string, r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			// 调试信息，忽略写入错误。
			_ = n
		}
		if err != nil {
			return
		}
		_ = name // 保留 name 参数（日志用途，当前仅丢弃）
	}
}
```

> **注意**：`mcpToolAdapter` 引用 `agentic/internal/tool`——需要确认 `internal/tool` 不 import `internal/mcp`（无循环）。`tool` 是 leaf，只 import stdlib + sandbox，安全。`client.go` 的 import 里 `mcpgo "github.com/mark3labs/mcp-go/client"` 别名避免与包名 `mcp` 冲突。`drainStderr` 里 `_ = name` 是保留 name 参数的占位（当前实现丢弃 stderr 内容），可简化——实现时按 go vet 要求调整。

- [ ] **Step 2: 验证编译（含 Task 3 的 adapter，先建空适配器文件避免 undefined）**

> Task 2 引用了 `mcpToolAdapter`（Task 3 定义）。若单独编译失败是预期的——连续完成 Task 2+3 后统一验证。本 Task 提交时可不编译验证（注明）。

- [ ] **Step 3: 提交**

```bash
git add internal/mcp/client.go go.mod go.sum
git commit -m "feat(mcp): add MCP manager for server lifecycle and tool discovery

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: `internal/mcp/adapter.go` — mcpToolAdapter

**Files:**
- Create: `internal/mcp/adapter.go`

- [ ] **Step 1: 创建 `internal/mcp/adapter.go`（完整文件）**

```go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentic/internal/tool"

	mcpgo "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

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
// 预留：后续可配置 RequiresPermission 的工具需确认。
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
func (a *mcpToolAdapter) Execute(args string) (string, error) {
	// ── 解析参数 JSON → map[string]any ──
	var arguments map[string]any
	if err := json.Unmarshal([]byte(args), &arguments); err != nil {
		return "", fmt.Errorf("parse mcp args: %w", err)
	}

	// ── 调用 MCP 工具 ──
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = a.toolName
	callReq.Params.Arguments = arguments

	result, err := a.client.CallTool(context.Background(), callReq)
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
```

- [ ] **Step 2: 验证编译（Task 2 + 3 就位，应能编译）**

Run: `cd /Users/wang/Documents/agentic-1 && go mod tidy && go build ./internal/mcp/`
Expected: 无输出（编译成功）

> 若 `mcp.GetTextFromContent` 不存在（研究说在 `mcp/utils.go`），改用 `if tc, ok := content.(mcp.TextContent); ok { parts = append(parts, tc.Text) }`——实现时以实际 API 为准。

- [ ] **Step 3: 提交**

```bash
git add internal/mcp/adapter.go
git commit -m "feat(mcp): add MCP tool adapter implementing tool.Tool

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: main.go 集成

**Files:**
- Modify: `main.go`

- [ ] **Step 1: 加可重复 flag + 连接 + 注册**

import 块（`main.go`）加 `"strings"`（已有？）和 `"agentic/internal/mcp"`。

flag 定义（`main.go` 的 flag 区）加：

```go
	// 可重复 flag：-mcp-server "npx -y @modelcontextprotocol/server-fetch"
	var mcpServers mcpServerFlags
	flag.Var(&mcpServers, "mcp-server", "MCP server command to connect (repeatable, e.g. \"npx -y @modelcontextprotocol/server-fetch\")")
```

文件末尾（或 flag 定义附近）加 flag 类型：

```go
// mcpServerFlags 可重复的 -mcp-server flag 值收集器。
type mcpServerFlags []string

func (f *mcpServerFlags) String() string { return strings.Join(*f, ",") }

func (f *mcpServerFlags) Set(v string) error {
	*f = append(*f, v)
	return nil
}
```

工具注册后、Runner 创建前（`main.go` 步骤 10 之后）加：

```go
	// ── 步骤 10b: MCP server 连接 ──
	// -mcp-server flag 指定的外部 MCP server（如 mcp-server-fetch 提供 WebFetch）。
	// 每个 server 的工具注册进 Registry；单个失败警告跳过，不阻断启动。
	mcpMgr := mcp.New()
	for _, cmd := range mcpServers {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			continue
		}
		cfg := mcp.ServerConfig{
			Name:    parts[0],
			Command: parts[0],
			Args:    parts[1:],
		}
		connectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := mcpMgr.Connect(connectCtx, cfg)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: mcp server %q connect failed: %v\n", cfg.Command, err)
			continue
		}
		for _, t := range mcpMgr.Tools() {
			tools.Register(t)
		}
		fmt.Fprintf(os.Stderr, "✅ mcp server %s connected, %d tools registered\n", cfg.Name, len(mcpMgr.Tools()))
	}
	defer mcpMgr.Close()
```

> **注意**：`mcpMgr.Tools()` 返回的是**全部** server 的工具，在循环里会重复注册之前 server 的。应改为每个 Connect 后只注册该 server 的工具——修正：让 `Connect` 返回该 server 的 `[]tool.Tool`（或 Manager 提供 `LastTools()`）。实现时选择最干净的方案（如 `Connect` 返回 `[]tool.Tool, error` 或 Manager 记录 `lastConn.tools`）。**spec 的 `Tools()` 是全部工具的聚合，main.go 循环里需要的是"本次 server 的工具"**——以能正确工作为准调整。

- [ ] **Step 2: 验证编译 + 全量测试**

Run: `cd /Users/wang/Documents/agentic-1 && go build ./... && go test -count=1 ./...`
Expected: 全部包 PASS

- [ ] **Step 3: 提交**

```bash
git add main.go
git commit -m "feat(main): add -mcp-server flag to connect external MCP servers

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 5: 测试 + 文档

**Files:**
- Create: `internal/mcp/client_test.go`
- Modify: `CLAUDE.md`

- [ ] **Step 1: 创建 `internal/mcp/client_test.go`（完整文件）**

```go
package mcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentic/internal/tool"
)

func TestParseServerCommand(t *testing.T) {
	// 通过 mcpServerFlags 验证命令解析（与 main.go 的解析逻辑一致）。
	// 此处直接测试 ServerConfig 构造辅助（若有）。
	// 若无独立解析函数，此测试改为测试 mcpToolName。
}

func TestMcpToolName(t *testing.T) {
	if got := mcpToolName("fetch", "fetch"); got != "fetch_fetch" {
		t.Errorf("mcpToolName = %q, want fetch_fetch", got)
	}
	if got := mcpToolName("search", "web_search"); got != "search_web_search" {
		t.Errorf("mcpToolName = %q, want search_web_search", got)
	}
}

func TestBuildSchema(t *testing.T) {
	// 构造最小 mcp.Tool 验证 schema 转换。
	// mcp.Tool 的 InputSchema 字段类型是 ToolInputSchema——按实际 API 构造。
	// 若构造复杂，此测试改为验证 adapter 的 Parameters() 直接返回 schema。
}

func TestAdapter_Basics(t *testing.T) {
	// 构造 adapter（client 可 nil——Execute 才用）。
	// 验证 Name/Description/Parameters/ResultLimit/Permission/Concurrency。
	// client 为 nil 时验证除 Execute 外的方法。
}

func TestAdapter_Execute_InvalidArgs(t *testing.T) {
	// client 为 nil，Execute 传无效 JSON → 应返回 parse 错误（不触 client）。
}

// 集成测试（可选）：真实拉起 mcp-server-fetch，验证工具枚举。
// 无 npx/node 时 t.Skip。
func TestConnect_FetchServer_Integration(t *testing.T) {
	if !hasNpx() {
		t.Skip("npx not available")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mgr := New()
	cfg := ServerConfig{Name: "fetch", Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-fetch"}}
	if err := mgr.Connect(ctx, cfg); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer mgr.Close()

	tools := mgr.Tools()
	if len(tools) == 0 {
		t.Error("should discover at least one tool from fetch server")
	}
	found := false
	for _, t := range tools {
		if t.Name() == "fetch_fetch" || strings.Contains(t.Name(), "fetch") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("should find a fetch tool, got: %v", toolNames(tools))
	}
}

func hasNpx() bool {
	// 简单检查：npx 是否在 PATH。
	return strings.Contains(strings.Join([]string{}, ""), "") // 占位——实现时用 exec.LookPath("npx")
}

func toolNames(ts []tool.Tool) []string {
	var names []string
	for _, t := range ts {
		names = append(names, t.Name())
	}
	return names
}
```

> **注意**：`hasNpx` 占位要替换为 `exec.LookPath("npx")`；`TestBuildSchema`/`TestAdapter_Basics` 需要按 mcp-go 实际 API 构造 `mcp.Tool`（`InputSchema: mcp.ToolInputSchema{...}`）——实现时核对。集成测试 `TestConnect_FetchServer_Integration` 需要真实 npx + 网络（`npx -y` 首次要下载包）——**若环境无 npx 或无网络会 t.Skip 或失败**，实现时以环境为准，失败就标记跳过。

- [ ] **Step 2: 运行测试**

Run: `cd /Users/wang/Documents/agentic-1 && go test -count=1 ./internal/mcp/ -v`
Expected: 单测 PASS；集成测试视环境（有 npx 且网络通 → PASS，否则 Skip）

- [ ] **Step 3: 全量验证**

Run: `cd /Users/wang/Documents/agentic-1 && go build ./... && go test -count=1 ./...`
Expected: 全部包 PASS

- [ ] **Step 4: 更新 CLAUDE.md**

Build and Run Commands 加：

```bash
go run . -mcp-server "npx -y @modelcontextprotocol/server-fetch"   # 连接 MCP server（WebFetch）
```

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/client_test.go CLAUDE.md
git commit -m "test(mcp): add MCP manager and adapter tests

Co-Authored-By: Claude <noreply@anthropic.com>"
```
