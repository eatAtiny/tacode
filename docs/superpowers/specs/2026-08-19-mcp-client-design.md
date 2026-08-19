# MCP 客户端支持设计文档

- 日期：2026-08-19
- 状态：已批准
- 范围：`internal/mcp/`（新增包：Manager + 适配器）+ `main.go`（`-mcp-server` flag）+ 测试 + CLAUDE.md

## 背景

Agent 目前只有 7 个本地工具（shell/file/edit/grep/list/git），无网络浏览能力。MCP（Model Context Protocol）生态有现成的开源 server（如 `mcp-server-fetch` 提供 WebFetch）。本项目需要 MCP client 支持，让任何 MCP server 的工具都能接入——这是平台化的关键一步。

## 决策

| 决策点 | 结论 |
|--------|------|
| 客户端 | `github.com/mark3labs/mcp-go`（本地缓存 v0.43.1，零网络拉取） |
| 配置 | 重复 `-mcp-server "<命令>"` flag（可多个） |
| 生命周期 | 启动拉起子进程 + 握手 + 枚举工具 + 退出关闭；单 server 失败警告跳过 |
| 适配 | MCP 工具 → `tool.Tool` 接口 → 注册进 `Registry`，queryLoop 无感知 |
| 权限 | 默认放行；适配器预留 `RequiresPermission` 配置（后续扩展） |
| 并发 | `IsConcurrencySafe=false`（MCP server 是外部进程，保守串行） |

## 架构

```
internal/mcp/（新增包）
  ├─ client.go  — MCPManager：启动/管理 server 子进程，握手，枚举工具
  ├─ adapter.go — mcpTool → tool.Tool 适配器（Name/Description/Parameters/Execute）
  └─ mcp_test.go

main.go
  └─ -mcp-server flag（可重复，flag.Var 自定义类型收集多个）
       └─ 启动时 mcp.NewManager() → Connect 所有 server → 枚举工具
            → 每个工具 NewAdapter(...) → tools.Register(adapter)
       └─ defer manager.Close()

Registry + queryLoop：零改动（适配器实现既有 Tool 接口）
```

## MCPManager

```go
// ServerConfig 描述一个 MCP server。
type ServerConfig struct {
	Name    string   // server 名称（如 "fetch"）
	Command string   // 命令（如 "npx"）
	Args    []string // 参数（如 ["-y", "@modelcontextprotocol/server-fetch"]）
}

// Manager 管理 MCP server 子进程和工具适配。
type Manager struct {
	servers []*serverConn
	tools   []*mcpToolAdapter
}

// New 创建 Manager（不启动）。
func New() *Manager

// Connect 启动一个 server 子进程，握手，枚举工具。
// 失败返回 error（调用方决定警告跳过还是退出）。
func (m *Manager) Connect(ctx context.Context, cfg ServerConfig) error

// Tools 返回所有已连接的 MCP 工具适配器。
func (m *Manager) Tools() []tool.Tool

// Close 关闭所有 server 子进程。
func (m *Manager) Close() error
```

## 适配器

```go
// mcpToolAdapter 把 MCP 工具适配成 tool.Tool。
// queryLoop 通过既有 Tool 接口无感知调用 MCP 工具。
type mcpToolAdapter struct {
	name        string
	description string
	schema      map[string]any // MCP inputSchema → Parameters()
	client      *mcpgo.Client
	tool        mcpgo.Tool
}

func (a *mcpToolAdapter) Name() string          { return a.name }
func (a *mcpToolAdapter) Aliases() []string     { return nil }
func (a *mcpToolAdapter) Description() string   { return a.description }
func (a *mcpToolAdapter) Parameters() map[string]any { return a.schema }
func (a *mcpToolAdapter) CheckPermission(args string) PermissionResult { return PermissionResult{Allow: true} }
func (a *mcpToolAdapter) PromptGuide() string   { return "" }
func (a *mcpToolAdapter) IsConcurrencySafe(args string) bool { return false }
func (a *mcpToolAdapter) IsReadOnly(args string) bool { return false }
func (a *mcpToolAdapter) ResultLimit() int      { return 12000 }
func (a *mcpToolAdapter) Execute(args string) (string, error) {
	// 解析 args JSON → map[string]any
	// client.CallTool(ctx, a.name, arguments)
	// 拼接 content 列表 → 文本返回
}
```

**注意**：`IsReadOnly` 返回 false（保守——不知道 MCP 工具是否有副作用），与 `IsConcurrencySafe=false` 一致（只读+并发安全才走并行路径，MCP 工具两者都不满足 → 串行）。

## main.go 集成

```go
// 可重复 flag：-mcp-server "npx -y @modelcontextprotocol/server-fetch"
type mcpServerFlags []string
func (f *mcpServerFlags) String() string { return strings.Join(*f, ",") }
func (f *mcpServerFlags) Set(v string) error { *f = append(*f, v); return nil }

var mcpServers mcpServerFlags
flag.Var(&mcpServers, "mcp-server", "MCP server command (repeatable)")

// 启动时（工具注册后）：
mcpMgr := mcp.New()
for _, cmd := range mcpServers {
	cfg := parseServerCommand(cmd) // 首词=命令，其余=args
	if err := mcpMgr.Connect(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "warning: mcp server %q connect failed: %v\n", cfg.Command, err)
		continue
	}
	for _, t := range mcpMgr.Tools() {
		tools.Register(t)
	}
}
defer mcpMgr.Close()
```

## 测试计划

| 测试 | 覆盖点 |
|------|--------|
| `TestAdapter_Basics` | Name/Description/Parameters/ResultLimit 正确 |
| `TestAdapter_Permission` | CheckPermission 默认 Allow |
| `TestAdapter_Concurrency` | IsConcurrencySafe=false, IsReadOnly=false |
| `TestAdapter_Execute_Error` | 无效 args JSON → 错误 |
| `TestParseServerCommand` | "npx -y pkg" → {Command:"npx", Args:["-y","pkg"]} |
| `TestManager_Connect_NoBinary` | 命令不存在 → Connect 返回错误 |

集成测试（可选，需 node/npx）：`TestManager_Connect_FetchServer` —— 真实拉起 `mcp-server-fetch`，枚举工具，验证 adapter 注册。无 npx 时 `t.Skip`。

## 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/mcp/client.go` | 新增：Manager + Connect + Tools + Close |
| `internal/mcp/adapter.go` | 新增：mcpToolAdapter |
| `internal/mcp/client_test.go` | 新增：单元测试 |
| `main.go` | `-mcp-server` flag + 连接 + 注册 |
| `CLAUDE.md` | 文档 |

## 依赖

- `github.com/mark3labs/mcp-go`（本地缓存 v0.43.1）

## 集成

- 完成标准：`go build ./...` + `go test ./...` 全绿
- 无 `-mcp-server` flag 时行为与现状完全一致（向后兼容）
- 使用示例：`go run . -mcp-server "npx -y @modelcontextprotocol/server-fetch"`
