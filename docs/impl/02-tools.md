# 2. 工具系统

## 设计目标

实现 5 个内置工具（shell、file、edit、grep、list），让 LLM 能操作文件系统和执行命令。核心原则：

- **权限内聚**：工具自行判断操作是否需要用户确认，而非外部按工具名打标签
- **Prompt 自引导**：工具向 system prompt 注入使用指南
- **Fail-closed 默认值**：安全相关方法不确定时返回 false
- **错误即数据**：工具执行错误作为结果返回给 LLM，不终止 Agent 循环

## 架构概览

```
main.go → Registry.Register(shellTool, fileTool, ...)
   │
   ├── QueryEngine → Registry.FunctionDefinitions()    → OpenAI 工具定义
   ├── QueryEngine → Registry.Descriptions()           → Prompt 中的工具描述
   └── queryLoop.executeSingleTool() → Registry.Get()  → 按名称查找并执行
```

## Tool 接口 (11 个方法)

```go
type Tool interface {
    // 基础
    Name() string
    Aliases() []string
    Description() string
    Parameters() map[string]any
    Execute(args string) (string, error)

    // 权限内聚
    CheckPermission(args string) PermissionResult

    // Prompt 自引导
    PromptGuide() string

    // 并发安全 (per-input 判定)
    IsConcurrencySafe(args string) bool
    IsReadOnly(args string) bool

    // 结果上限
    ResultLimit() int
}
```

### 为什么是 11 个方法而不是更少？

参照 Claude Code 的 tool contract，每个方法解决一个具体问题：

| 方法 | 解决的问题 |
|------|-----------|
| `CheckPermission(args)` | 危险操作确认不应由外部统一判断 — shell 知道哪些命令危险，file 知道 write 需确认 |
| `PromptGuide()` | 工具用法不只是参数 schema，还需要告诉 LLM 何时用、怎么用 |
| `IsConcurrencySafe(args)` | 并发安全不是工具级别的属性 — `shell ls` 安全，`shell rm` 不安全 |
| `ResultLimit()` | 每个工具知道自己合理的输出长度，框架统一截断 |

## 5 个内置工具

### shell — Bash 命令执行

```
Name:        "shell"
IsReadOnly:  60+ 命令白名单 (ls, cat, grep, find, git log, go vet, docker ps, curl...)
Concurrency: 跟随 IsReadOnly
Permission:  readOnly=false 时需确认，高风险操作 (rm -rf, sudo, chmod 777) 强制确认
ResultLimit: 5000
aliases:     nil
```

**实现要点**：`isReadOnlyShellCommand(args)` 解析 JSON 参数提取 command 字段，与白名单匹配，同时检测重定向符号 (`>`, `>>`, `|` 不算重定向)。

### file — 文件读写

```
Name:        "file"
IsReadOnly:  action="read" → true, action="write" → false
Concurrency: 跟随 IsReadOnly
Permission:  read → Allow, write → 需确认
ResultLimit: 8000
aliases:     nil
```

**write 预览**：写入成功后回显前 30 行带行号的内容，超过 30 行显示 `... (N lines total)`。
共用 `formatWithLineNumbers(lines, maxLines)` 实现 read 和 write 的一致性输出。

### edit — 精确字符串替换

```
Name:        "edit"
IsReadOnly:  false (始终)
Concurrency: false (始终)
Permission:  始终需确认 (文件写入操作)
ResultLimit: 3000
aliases:     nil
```

**replace_all 支持**：新增 `replace_all` (bool) 参数，默认 false。
- `false`：要求 `old_string` 在文件中唯一匹配，否则报错并提示使用 `replace_all=true`
- `true`：使用 `strings.ReplaceAll` 替换所有匹配，返回 "替换了 N 处"

**read-before-edit 检查**：通过 `ReadStateAware` 接口从框架注入已读文件状态。
EditTool 在 Execute 中自查：文件是否已读、mtime 是否匹配（内聚在工具内部）。

### grep — 内容搜索

```
Name:        "grep"
IsReadOnly:  true (始终)
Concurrency: true (始终)
Permission:  Allow (始终)
ResultLimit: 5000
aliases:     nil
```

纯只读搜索工具，始终并发安全，无需权限确认。

### list — 目录列表

```
Name:        "list"
IsReadOnly:  true (始终)
Concurrency: true (始终)
Permission:  Allow (始终)
ResultLimit: 3000
aliases:     nil
```

支持 `depth` (递归深度) 和 `max_entries` (条目上限)。输出包含目录相对路径（便于 LLM 直接复用于后续工具调用）。

## 关键设计模式

### 1. 并发工具执行器 (StreamingToolExecutor)

参照 Claude Code 的 tool 调度模式，一轮对话可能产生多个 tool_calls，执行策略按 per-input safety 动态分类：

```
executeToolCalls(toolCalls)
  ├── 并发组: IsConcurrencySafe + IsReadOnly + Allow → goroutine + WaitGroup
  └── 串行组: 其余工具 → 逐个 executeSingleTool()
```

并发组内的工具在独立 goroutine 中执行，但共享 mutex 保护的 `fileReads` map（记录 mtime）。
串行组在并发组完成后逐个执行（保证副作用顺序）。

性能验证：3 个 100ms sleep 工具，并行 ~105ms (2.8x 加速 vs 串行 300ms)。

### 2. ReadState / ReadStateAware (Read-Before-Edit)

```go
type ReadState map[string]time.Time  // absPath → mtime

type ReadStateAware interface {
    Tool
    SetReadState(state ReadState)
}
```

queryLoop 在每次 Execute 前通过类型断言注入 `fileReads`：
```go
if aware, ok := t.(tool.ReadStateAware); ok {
    aware.SetReadState(lc.fileReads)
}
```

EditTool 实现了 ReadStateAware，在 Execute 内部检查：
1. 目标文件是否在 readState 中存在（是否被读取过）
2. 当前 mtime 是否与记录匹配（是否被外部修改）

检查失败返回 error（作为 tool result 给 LLM），而非软 warning。

### 3. 大结果磁盘持久化

参照 Claude Code 的 `maxResultSizeChars` + 磁盘持久化：

```
Execute → result 超过 ResultLimit?
  ├── 否 → 直接返回
  └── 是 → SaveLargeResult(dir, toolName, fullContent)
            → 返回 TruncateResult(head 70% + tail 30% + 持久化路径提示)
```

`SaveLargeResult` 写入 `data/tool-results/<toolName>_<timestamp>_<hash>.txt`。
LLM 收到截断结果后可通过 `file read` 读取完整内容。

### 4. TruncateResult — 头尾保留策略

保留前 70% + 后 30% 而非仅头部。理由：很多输出（编译错误、测试结果）的关键信息在末尾。
中间插入截断提示 `…(已截断，共 N 字符，保留头尾)…`。

### 5. ToolHook — 执行前后切面

```go
type ToolHook interface {
    BeforeExecute(toolName, args string) error
    AfterExecute(toolName, args, result string, execErr error)
}
```

典型用途：

| 场景 | 方式 |
|------|------|
| 日志审计 | AfterExecute 记录每次调用 |
| 安全策略 | BeforeExecute 返回 error 阻止危险操作 |
| 性能监控 | Before + After 记录耗时 |

- BeforeExecute 阻断时，错误信息作为 tool result 反馈给 LLM
- AfterExecute 始终执行（成功/失败/被阻断均触发）
- AfterExecute 有 panic recover（单个钩子崩溃不影响主流程）
- 按 `AddHook()` 注册顺序依次执行

### 6. 工具别名

```go
Aliases() []string  // 返回历史名称列表
```

Registry.Get() 在名称不匹配时回退到别名查找。
Register() 检测别名冲突（与已有工具名或其他别名冲突时 panic）。

当前所有工具返回 nil（无历史名称），保留扩展空间。

## 添加新工具

```go
// 1. 实现 Tool 接口
type MyTool struct{}

func (t *MyTool) Name() string          { return "my_tool" }
func (t *MyTool) Aliases() []string     { return nil }
func (t *MyTool) Description() string   { return "..." }
func (t *MyTool) Parameters() map[string]any { return ... }
func (t *MyTool) Execute(args string) (string, error) { ... }
func (t *MyTool) CheckPermission(args string) PermissionResult { ... }
func (t *MyTool) PromptGuide() string   { return "" }
func (t *MyTool) IsConcurrencySafe(args string) bool { return false }
func (t *MyTool) IsReadOnly(args string) bool        { return false }
func (t *MyTool) ResultLimit() int      { return 2000 }

// 2. 注册
reg.Register(&MyTool{})
```

## 文件清单

| 文件 | 职责 |
|------|------|
| `internal/tool/tool.go` | Tool 接口、ReadState、ToolHook、Registry、TruncateResult、SaveLargeResult |
| `internal/tool/shell.go` | ShellTool — bash 命令执行 + 只读命令白名单 |
| `internal/tool/file.go` | FileTool — 文件读写 + write 预览 |
| `internal/tool/edit.go` | EditTool — 字符串替换 + replace_all + read-before-edit |
| `internal/tool/grep.go` | GrepTool — 内容搜索 |
| `internal/tool/list.go` | ListTool — 目录列表 |
| `internal/tool/tool_test.go` | 27 个测试（单元 + 别名 + 钩子） |
| `internal/agent/query_loop.go` | executeSingleTool / executeConcurrentTools |
| `internal/agent/query_loop_test.go` | 5 个集成测试（并行/持久化/混合/权限） |
