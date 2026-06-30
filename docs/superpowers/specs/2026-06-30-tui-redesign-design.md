# TUI 系统改造设计文档

## 概述

将当前 Go ReAct agent 项目改造为支持 Claude Code 风格的 TUI 界面。通过 UI 接口 + 适配器模式，让 runner/query_engine 可以脱离 TUI 独立运行（子 agent 模式）。

## 设计目标

1. **Claude Code 风格 TUI**：基于 Bubble Tea 的全屏交互体验
2. **适配器模式**：UI 可插拔，核心逻辑不依赖具体 UI 实现
3. **子 agent 就绪**：定义 TextUI 接口，支持后续作为子 agent 被调用
4. **完整 TUI 体验**：对话渲染、输入栏、状态栏、工具执行可视化、权限确认弹窗

## 架构设计

### 包结构

```
internal/ui/
  ui.go              # UI 接口定义 + 类型
  bubble.go          # Bubble Tea 实现（主 TUI）
  text.go            # TextUI stub（子 agent，TODO）
  styles.go          # Lip Gloss 样式常量
  components/
    input.go         # 输入栏组件（tea.Model）
    conversation.go  # 对话历史组件（滚动、Markdown 渲染）
    status.go        # 状态栏（session、token、模型信息）
    toolview.go      # 工具执行可视化（弹窗、展开/折叠）
    confirm.go       # 权限确认弹窗组件
```

### UI 接口

```go
// internal/ui/ui.go
type UI interface {
    // 输入
    ReadInput() (string, error)

    // 流式事件
    OnThink(iteration int)
    OnDelta(content string)
    OnToolCall(name, args string)
    OnToolResult(name, result string, isError bool)
    OnContinue(iteration int)
    OnFinal(answer string)
    OnError(err error)

    // 权限
    ConfirmPermission(tool, args string) (bool, error)

    // 生命周期
    Close() error
}
```

### Runner 改造

```go
// 改造前
func NewRunner(llm, history, summary, memStore, events, extractor, retriever, tools, sessions) *Runner

// 改造后
func NewRunner(llm, history, summary, memStore, events, extractor, retriever, tools, sessions, ui UI) *Runner
```

Runner 内部所有 `fmt.Print` / `readline` 调用替换为 `r.ui.OnXxx()` / `r.ui.ReadInput()`。

### main.go 改造

```go
var uiInstance ui.UI
uiInstance = ui.NewBubbleUI()  // 默认 TUI
runner := agent.NewRunner(..., uiInstance)
runner.Run(ctx)
```

## Bubble Tea 模型架构

### 核心模型

```go
// internal/ui/bubble.go
type BubbleUI struct {
    program *tea.Program

    // 子组件
    conversation *ConversationModel
    input        *InputModel
    status       *StatusModel
    toolView     *ToolViewModel

    // 状态
    waitingInput bool
    confirmCh    chan bool
    inputCh      chan string

    // 元数据
    sessionID string
    model     string
    round     int
}
```

### 接口桥接

Bubble Tea 是自驱动事件循环，UI 接口是被动调用。通过 `program.Send()` 桥接：

```go
func (b *BubbleUI) OnDelta(content string) {
    b.program.Send(deltaMsg{content})
}

func (b *BubbleUI) ReadInput() (string, error) {
    b.program.Send(waitInputMsg{})
    return <-b.inputCh, nil
}

func (b *BubbleUI) ConfirmPermission(tool, args string) (bool, error) {
    b.program.Send(confirmMsg{tool, args})
    return <-b.confirmCh, nil
}
```

### 消息类型

```go
type deltaMsg struct{ content string }
type toolCallMsg struct{ name, args string }
type toolResultMsg struct{ name, result string; isError bool }
type thinkMsg struct{ iteration int }
type continueMsg struct{ iteration int }
type finalMsg struct{ answer string }
type errorMsg struct{ err error }
type waitInputMsg struct{}
type inputDoneMsg struct{ input string }
type confirmMsg struct{ tool, args string }
type confirmDoneMsg struct{ approved bool }
```

## UI 布局

### 状态栏

```
agentic │ session: abc │ gpt-4o-mini │ ↑ 1.2k ↓ 3.4k
──────────────────────────────────────────────────────
```

- 固定顶部，单行
- session 名称（超长截断）
- 模型名称
- token 用量：`↑` 本轮累计输入 token / `↓` 本轮累计输出 token，每轮结束后重置

### 对话区

```
> 帮我写一个 hello world

──────────────────────────────────────────────────────

⏳ Thinking...

🔧 2 tool calls                        [press Enter to view]

# Hello World

这是一个简单的示例：
```go
fmt.Println("hello")
```

──────────────────────────────────────────────────────

> _
```

- 横线 `──` 分隔每次对话轮次
- `>` 用户输入前缀
- 工具调用在弹窗中交互查看，对话区只显示摘要行（如 `🔧 2 tool calls`）
- Assistant 回答直接 Markdown 渲染（glamour），无额外包装
- 工具弹窗关闭后，摘要行留在对话区供回看

## 工具执行弹窗

### 工具列表

```
┌─ Tool Calls ─────────────────────────────────────────┐
│ > 🔧 shell: echo "hello"              ✓ hello        │
│   🔧 file: read /tmp/test.go           ✓ (2.1 KB)    │
│   🔧 shell: rm -rf /tmp/data           ⚠ 高危命令     │
└──────────────────────────────────────────────────────┘
```

- 弹窗出现时自动获得焦点
- `↑` / `↓` 或 `j` / `k` 上下移动选中项
- `Enter` 展开选中工具的详情（完整参数 + 完整结果）
- `Esc` 或 `q` 关闭弹窗，焦点回到输入区
- `✓` / `✗` / `⏳` 标记状态
- 所有工具执行完成后弹窗自动收起，结果摘要留在对话区

### 展开详情

```
┌─ Tool: shell ────────────────────────────────────────┐
│ Command: echo "hello world"                           │
│ Status: ✓ 成功                                         │
│                                                       │
│ Output:                                               │
│ hello world                                           │
└──────────────────────────────────────────────────────┘
```

## 权限确认弹窗

### 弹窗样式

```
┌─ Permission Required ────────────────────────────────┐
│                                                       │
│  🔧 shell: rm -rf /tmp/data                           │
│                                                       │
│  ⚠ 此命令可能删除文件                                  │
│                                                       │
│  > Yes, execute    No, deny                           │
└──────────────────────────────────────────────────────┘
```

- 弹窗出现时锁定焦点，无法操作其他区域
- `←` / `→` 或 `Tab` 切换 Yes / No
- `Enter` 确认选择
- 选择后弹窗关闭，继续执行或跳过

### 交互流程

1. LLM 返回工具调用 → 工具列表弹窗出现（自动聚焦）
2. 遍历每个工具：
   - 低危 → 直接执行，显示 `✓`
   - 高危 → 权限确认弹窗覆盖出现 → 用户选择 → 关闭 → 继续
3. 全部完成 → 弹窗收起 → 结果摘要写入对话区

## 权限确认的数据流

通过 QueryEvent 传递权限请求，保持 queryLoop 不感知 UI：

### QueryEvent 扩展

```go
type QueryEvent struct {
    // ... 现有字段

    // 新增：权限请求
    PermissionRequired bool
    PermissionTool     string
    PermissionArgs     string
    PermissionReason   string
    PermissionCh       chan bool  // 上层写入结果
}
```

### 流程

```
queryLoop 检测到高危工具
  → yield QueryEvent{
      PermissionRequired: true,
      PermissionTool: "shell",
      PermissionArgs: "rm -rf /tmp/data",
      PermissionReason: "高危命令",
      PermissionCh: make(chan bool),
    }
  → 阻塞等待 PermissionCh

queryEngine 消费事件:
  → 调用 ui.ConfirmPermission(tool, args)
    → BubbleUI 弹出确认弹窗
      → 用户选择 Yes/No
        → 结果写入 PermissionCh

queryLoop 收到结果:
  → true: 执行工具
  → false: 跳过，yield QueryEventToolResult{IsError: true, Result: "用户拒绝执行"}
```

**子 agent 模式**：上层 agent 收到 `PermissionRequired` 事件后，可自行决定（自动放行、转发给更上层、基于策略判断），不需要 UI。

## 输入栏组件

### 功能

```
────────────────────────────────────────────────────────
> _
/new /list /switch /delete /rename /memory /compress
```

- 底部横线分隔，`>` 输入前缀
- 下方显示可用 `/` 命令提示（灰色，非交互）

### 快捷键

| 按键 | 功能 |
|------|------|
| `Enter` | 发送输入 |
| `↑` / `↓` | 历史记录翻页 |
| `Ctrl+C` | 中断当前操作 / 退出 |
| `Ctrl+L` | 清屏 |
| `Tab` | `/` 命令补全 |
| `Esc` | 关闭弹窗 / 取消当前操作 |

### `/` 命令补全

输入 `/` 后自动弹出命令列表：

```
> /l
┌─ Commands ───────────────────────────────────────────┐
│ /list          列出所有 session                       │
│ /switch        切换 session                           │
└──────────────────────────────────────────────────────┘
```

- 继续输入自动过滤
- `↑` / `↓` 选择，`Enter` 确认
- 非 `/` 开头时自动关闭

**多行输入**：第一版不支持，单行输入 + Enter 发送。

## 子 Agent 模式（TextUI）

### 实现

```go
// internal/ui/text.go
type TextUI struct {
    OnEvent func(event string, data any)
}

func NewTextUI() *TextUI { return &TextUI{} }

func (t *TextUI) ReadInput() (string, error) {
    return "", fmt.Errorf("TextUI: ReadInput not implemented")
}

func (t *TextUI) OnDelta(content string) {
    if t.OnEvent != nil {
        t.OnEvent("delta", content)
    }
}

func (t *TextUI) ConfirmPermission(tool, args string) (bool, error) {
    return true, nil  // 默认放行
}

func (t *TextUI) Close() error { return nil }
```

### 使用方式

```go
ui := ui.NewTextUI()
ui.OnEvent = func(event string, data any) {
    log.Printf("sub-agent event: %s %v", event, data)
}
runner := agent.NewRunner(..., ui)
```

### 扩展点

- `OnEvent` 回调：上层 agent 可注入处理逻辑
- `ConfirmPermission` 默认放行，上层可通过 `OnEvent` 拦截后自己决定
- `ReadInput` TODO：后续可改为从 channel 读取上层注入的输入

## 整体数据流

```
main.go
  │
  ├── 创建 UI: ui.NewBubbleUI()
  │       └── 内部创建 tea.Program
  │
  ├── 创建 Runner: agent.NewRunner(..., ui)
  │
  └── runner.Run(ctx)
        │
        ├── ui.Init()  → 启动 Bubble Tea 程序（goroutine）
        │
        ├── REPL 循环:
        │     input, _ := ui.ReadInput()     → 阻塞等待用户输入
        │     │
        │     ├── "/" 前缀 → 处理命令（内部调用 ui 显示结果）
        │     │
        │     └── 正常输入
        │           │
        │           ├── queryEngine(ctx, round, input)
        │           │     │
        │           │     ├── retriever.BuildContext()
        │           │     ├── prompt.Build()
        │           │     ├── queryLoop() → <-chan QueryEvent
        │           │     │
        │           │     └── 消费事件:
        │           │           Think           → ui.OnThink()
        │           │           Delta           → ui.OnDelta()
        │           │           ToolCall         → ui.OnToolCall()
        │           │           ToolResult       → ui.OnToolResult()
        │           │           PermissionReq   → ui.ConfirmPermission()
        │           │           Final           → ui.OnFinal()
        │           │           Error           → ui.OnError()
        │           │
        │           ├── 记录事件到 EventStore
        │           ├── 提取记忆 (Extractor)
        │           └── 写入 L1/L2/L3
        │
        └── ui.Close()
```

## 错误处理

| 场景 | 处理方式 |
|------|----------|
| TTY 检测失败 | 启动时检测 `term.IsTerminal()`，非 TTY 直接报错退出 |
| Bubble Tea 程序崩溃 | `recover` 捕获，回退到 TextUI，打印错误 |
| LLM 调用失败 | `QueryEventError` → `ui.OnError()` → 显示错误信息，REPL 继续 |
| 工具执行超时 | 30s 超时（现有逻辑），结果标记 `✗` |
| 权限确认超时 | 60s 无响应自动拒绝，弹窗关闭 |
| 用户 Ctrl+C | 单次：取消当前操作；双次：退出程序 |

## 依赖变化

| 依赖 | 变化 |
|------|------|
| `bubbletea` | 已有，升级使用 |
| `lipgloss` | 已有，扩展样式 |
| `glamour` | 已有，Markdown 渲染 |
| `bubbles` | **新增**，提供 textarea、viewport、spinner、list 等组件 |
| `readline` | **移除**，Bubble Tea 替代 |

## 测试策略

- **UI 接口测试**：用 `TextUI` 跑集成测试，不依赖 TTY
- **Bubble Tea 组件测试**：每个组件的 `Update` 方法可独立测试（输入消息 → 验证状态变化）
- **queryLoop 测试**：现有 channel 模式天然可测，注入 mock UI
- **不做的事**：不做终端截图对比测试（成本高、收益低）

## 实施阶段

| 阶段 | 内容 | 改动文件 |
|------|------|----------|
| Phase 1 | 定义 UI 接口 + TextUI 实现 | `internal/ui/ui.go`, `internal/ui/text.go` |
| Phase 2 | 改造 Runner 和 queryEngine 依赖 UI 接口 | `runner.go`, `query_engine.go`, `permission.go`, `main.go` |
| Phase 3 | 实现 BubbleUI 基础框架（Bubble Tea model） | `internal/ui/bubble.go`, `internal/ui/styles.go` |
| Phase 4 | 实现 TUI 组件（input、conversation、status、toolview、confirm） | `internal/ui/components/` |
| Phase 5 | 权限确认弹窗 + QueryEvent 扩展 | `types.go`, `query_loop.go`, `permission.go` |
| Phase 6 | 子 agent 模式验证 + 集成测试 | `internal/ui/text.go`, 测试文件 |
