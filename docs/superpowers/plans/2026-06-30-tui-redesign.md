# TUI 系统改造实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 Go ReAct agent 改造为支持 Claude Code 风格 TUI 界面，通过 UI 接口 + 适配器模式实现可插拔 UI。

**Architecture:** 定义 `UI` 接口抽象输入输出，`queryEngine` 和 `Runner` 依赖接口而非具体实现。`BubbleUI` 作为默认 TUI 实现（Bubble Tea），`TextUI` 作为子 agent stub。`QueryEvent` 扩展权限请求字段，由 `queryEngine` 层桥接 UI 确认。

**Tech Stack:** Go 1.26.4, Bubble Tea, Lip Gloss, Glamour, Bubbles

## Global Constraints

- 所有 UI 输出通过 `UI` 接口，不再直接调用 `fmt.Print`
- 所有用户输入通过 `UI.ReadInput()`，不再使用 `readline`
- `queryLoop` 不感知 UI，只通过 `QueryEvent` channel 传递事件
- 权限确认通过 `QueryEvent.PermissionCh` 异步传递
- 仅支持 TTY 环境，非 TTY 直接报错退出

---

### Task 1: UI 接口 + TextUI Stub

**Files:**
- Create: `internal/ui/ui.go`
- Create: `internal/ui/text.go`

**Interfaces:**
- Produces: `UI` interface（后续所有任务依赖）
- Produces: `TextUI` struct（子 agent 模式用）

- [ ] **Step 1: 创建 UI 接口定义**

```go
// internal/ui/ui.go
package ui

// UI 定义了 Agent 与用户交互的接口。
// 所有 UI 操作都通过此接口完成，实现可插拔。
type UI interface {
	// ReadInput 读取用户输入，阻塞直到用户按下 Enter。
	ReadInput() (string, error)

	// OnThink 通知 LLM 正在思考。
	OnThink(iteration int)

	// OnDelta 流式输出增量文本。
	OnDelta(content string)

	// OnToolCall 通知工具调用请求。
	OnToolCall(name, args string)

	// OnToolResult 通知工具执行结果。
	OnToolResult(name, result string, isError bool)

	// OnContinue 通知继续推理。
	OnContinue(iteration int)

	// OnFinal 通知最终回答。
	OnFinal(answer string)

	// OnError 通知错误。
	OnError(err error)

	// ConfirmPermission 请求用户确认权限。
	// 返回 true 表示允许，false 表示拒绝。
	ConfirmPermission(tool, args string) (bool, error)

	// Close 关闭 UI，释放资源。
	Close() error
}
```

- [ ] **Step 2: 创建 TextUI stub**

```go
// internal/ui/text.go
package ui

import "fmt"

// TextUI 用于子 agent 模式，无 TTY 依赖。
// 所有事件通过 OnEvent 回调转发给上层。
type TextUI struct {
	// OnEvent 回调函数，上层 agent 可注入处理逻辑。
	// 参数: event 事件类型, data 事件数据（类型取决于 event）。
	OnEvent func(event string, data any)
}

// NewTextUI 创建 TextUI 实例。
func NewTextUI() *TextUI {
	return &TextUI{}
}

func (t *TextUI) ReadInput() (string, error) {
	return "", fmt.Errorf("TextUI: ReadInput not implemented")
}

func (t *TextUI) OnThink(iteration int) {
	if t.OnEvent != nil {
		t.OnEvent("think", iteration)
	}
}

func (t *TextUI) OnDelta(content string) {
	if t.OnEvent != nil {
		t.OnEvent("delta", content)
	}
}

func (t *TextUI) OnToolCall(name, args string) {
	if t.OnEvent != nil {
		t.OnEvent("tool_call", map[string]string{"name": name, "args": args})
	}
}

func (t *TextUI) OnToolResult(name, result string, isError bool) {
	if t.OnEvent != nil {
		t.OnEvent("tool_result", map[string]any{"name": name, "result": result, "is_error": isError})
	}
}

func (t *TextUI) OnContinue(iteration int) {
	if t.OnEvent != nil {
		t.OnEvent("continue", iteration)
	}
}

func (t *TextUI) OnFinal(answer string) {
	if t.OnEvent != nil {
		t.OnEvent("final", answer)
	}
}

func (t *TextUI) OnError(err error) {
	if t.OnEvent != nil {
		t.OnEvent("error", err)
	}
}

func (t *TextUI) ConfirmPermission(tool, args string) (bool, error) {
	if t.OnEvent != nil {
		t.OnEvent("permission", map[string]string{"tool": tool, "args": args})
	}
	return true, nil // 默认放行
}

func (t *TextUI) Close() error { return nil }
```

- [ ] **Step 3: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/ui/`
Expected: 编译成功，无错误

- [ ] **Step 4: Commit**

```bash
git add internal/ui/ui.go internal/ui/text.go
git commit -m "feat(ui): 添加 UI 接口定义和 TextUI stub

- UI 接口：ReadInput, OnThink, OnDelta, OnToolCall, OnToolResult, OnContinue, OnFinal, OnError, ConfirmPermission, Close
- TextUI：子 agent 模式 stub，所有事件通过 OnEvent 回调转发"
```

---

### Task 2: QueryEvent 权限扩展

**Files:**
- Modify: `internal/agent/types.go`

**Interfaces:**
- Produces: `QueryEvent` 新增权限字段（后续 Task 12 依赖）

- [ ] **Step 1: 扩展 QueryEvent 结构体**

在 `internal/agent/types.go` 的 `QueryEvent` 结构体中添加权限相关字段：

```go
type QueryEvent struct {
	Type      QueryEventType
	Content   string
	ToolCalls []llm.ToolCall
	ToolName  string
	ToolResult string
	IsError   bool
	Iteration int
	Error     error

	InputTokens  int
	OutputTokens int
	TotalTokens  int

	// 权限请求（PermissionRequired 为 true 时有效）
	PermissionRequired bool   // 是否需要权限确认
	PermissionTool     string // 需要确认的工具名
	PermissionArgs     string // 工具参数
	PermissionReason   string // 需要确认的原因
	PermissionCh       chan bool // 上层写入确认结果
}
```

- [ ] **Step 2: 添加权限事件类型**

在 `types.go` 的常量定义中添加：

```go
const (
	QueryEventThink      QueryEventType = "think"
	QueryEventDelta      QueryEventType = "delta"
	QueryEventToolCall   QueryEventType = "tool_call"
	QueryEventToolResult QueryEventType = "tool_result"
	QueryEventContinue   QueryEventType = "continue"
	QueryEventFinal      QueryEventType = "final"
	QueryEventError      QueryEventType = "error"
	QueryEventPermission QueryEventType = "permission" // 新增：权限确认请求
)
```

- [ ] **Step 3: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/agent/`
Expected: 编译成功

- [ ] **Step 4: Commit**

```bash
git add internal/agent/types.go
git commit -m "feat(agent): 扩展 QueryEvent 支持权限确认请求

- 新增 QueryEventPermission 事件类型
- 新增 PermissionRequired, PermissionTool, PermissionArgs, PermissionReason, PermissionCh 字段"
```

---

### Task 3: Runner 改造 — 接入 UI 接口

**Files:**
- Modify: `internal/agent/runner.go`

**Interfaces:**
- Consumes: `UI` interface (from Task 1)
- Produces: `Runner` 接收 `UI` 参数（后续 Task 5 依赖）

- [ ] **Step 1: 添加 UI 字段到 Runner 结构体**

在 `internal/agent/runner.go` 中：

```go
import "agentic/internal/ui"

type Runner struct {
	llm       *llm.OpenAIClient
	history   *memory.HistoryStore
	summary   *memory.SummaryStore
	memStore  *memory.MemoryStore
	events    *memory.EventStore
	extractor *memory.Extractor
	retriever *memory.Retriever
	tools     *tool.Registry
	sessions  *session.SessionManager
	ui        ui.UI  // 新增

	isTemporary        bool
	tempID             string
	pendingSessionName string
}
```

- [ ] **Step 2: 修改 NewRunner 签名**

```go
func NewRunner(
	client *llm.OpenAIClient,
	history *memory.HistoryStore,
	summary *memory.SummaryStore,
	memStore *memory.MemoryStore,
	events *memory.EventStore,
	extractor *memory.Extractor,
	retriever *memory.Retriever,
	tools *tool.Registry,
	sessions *session.SessionManager,
	uiInstance ui.UI,  // 新增参数
) *Runner {
	return &Runner{
		llm:       client,
		history:   history,
		summary:   summary,
		memStore:  memStore,
		events:    events,
		extractor: extractor,
		retriever: retriever,
		tools:     tools,
		sessions:  sessions,
		ui:        uiInstance,
	}
}
```

- [ ] **Step 3: 替换 Run() 中的 readline 为 UI.ReadInput()**

将 `Run()` 方法中的 readline 相关代码替换：

```go
func (r *Runner) Run(ctx context.Context) error {
	printBanner(r.sessions)
	printSessionHint()

	// 启动临时会话（逻辑不变）
	r.isTemporary = true
	tempID, err := session.GenerateID()
	if err != nil {
		return fmt.Errorf("generate temp session id failed: %w", err)
	}
	r.tempID = tempID
	tempDir := r.sessions.SessionDir(tempID)
	r.history.SetPath(tempDir)
	r.summary.SetPath(tempDir)
	r.memStore.SetPath(tempDir)
	r.events.SetPath(tempDir)
	r.cleanOrphanTempDirs()

	// 移除 readline 初始化，使用 UI 接口
	for round := 1; ; round++ {
		input, err := r.ui.ReadInput()
		if err != nil {
			// Ctrl+C 或 EOF
			fmt.Printf("\n%s\n", mutedStyle.Render("Agent stopped by user."))
			return nil
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if strings.EqualFold(input, "exit") {
			fmt.Printf("\n%s\n", mutedStyle.Render("Agent stopped by user."))
			return nil
		}

		// 处理会话管理斜杠命令（不变）
		if strings.HasPrefix(input, "/") {
			newRound, handled := r.handleSessionCommand(input)
			if handled {
				if newRound > 0 {
					round = newRound - 1
				}
				continue
			}
			fmt.Printf("\n%s\n", errorStyle.Render("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory"))
			round--
			continue
		}

		// 记录用户输入事件（不变）
		r.events.Append(memory.Event{
			Type:    memory.EventUser,
			Round:   round,
			Content: input,
		})

		// 执行 QueryEngine（不变）
		answer, err := r.queryEngine(ctx, round, input)
		if err != nil {
			return fmt.Errorf("query engine failed at round %d: %w", round, err)
		}

		printAnswer(round, answer)

		// 记录模型回答事件（不变）
		r.events.Append(memory.Event{
			Type:    memory.EventAssistant,
			Round:   round,
			Content: answer,
			Model:   r.llm.Model(),
		})

		// 保存记忆（不变）
		if err := r.history.Append(round, input, answer); err != nil {
			return fmt.Errorf("save history failed at round %d: %w", round, err)
		}
		r.extractMemory(ctx, round, input, answer)

		if r.isTemporary {
			if err := r.ensurePersisted(); err != nil {
				fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("保存会话失败: %v", err)))
			}
		}
	}
}
```

- [ ] **Step 4: 移除 readline import**

从 `runner.go` 的 import 中移除 `"github.com/chzyer/readline"` 和 `"io"`。添加 `"agentic/internal/ui"`。

- [ ] **Step 5: 删除 newReadline 函数**

删除 `runner.go` 中的 `newReadline` 函数。

- [ ] **Step 6: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/agent/`
Expected: main.go 会报错（NewRunner 签名变了），其他包正常

- [ ] **Step 7: Commit**

```bash
git add internal/agent/runner.go
git commit -m "refactor(agent): Runner 接入 UI 接口，移除 readline 依赖

- NewRunner 新增 ui.UI 参数
- Run() 使用 ui.ReadInput() 替代 readline
- 移除 newReadline 函数和 readline import"
```

---

### Task 4: queryEngine 改造 — 使用 UI 接口输出

**Files:**
- Modify: `internal/agent/query_engine.go`

**Interfaces:**
- Consumes: `UI` interface (from Task 1)
- Consumes: `QueryEvent` with permission fields (from Task 2)

- [ ] **Step 1: 替换 printXxx 为 ui.OnXxx**

修改 `queryEngine` 方法中的事件处理循环：

```go
func (r *Runner) queryEngine(ctx context.Context, round int, userInput string) (string, error) {
	// 步骤 1-3 不变（构建上下文、压缩检查、构建提示）

	// 步骤 4: 调用 queryLoop
	r.ui.OnThink(0) // 通知开始推理
	contextLimit := r.llm.ContextLimit()
	eventChan := queryLoop(ctx, r.llm, messages, tools, r.tools, maxIterations, contextLimit)

	// 步骤 5: 从 channel 读取事件
	var finalAnswer string
	var finalIteration int

	for event := range eventChan {
		switch event.Type {
		case QueryEventThink:
			r.ui.OnThink(event.Iteration)

		case QueryEventDelta:
			r.ui.OnDelta(event.Content)

		case QueryEventToolCall:
			// 记录事件到 EventStore
			toolCallEvents := make([]memory.ToolCallEvent, len(event.ToolCalls))
			for i, tc := range event.ToolCalls {
				toolCallEvents[i] = memory.ToolCallEvent{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			}
			r.events.Append(memory.Event{
				Type:      memory.EventToolUse,
				Round:     round,
				ToolCalls: toolCallEvents,
			})
			// 通知 UI
			for _, tc := range event.ToolCalls {
				r.ui.OnToolCall(tc.Name, tc.Arguments)
			}

		case QueryEventToolResult:
			r.events.Append(memory.Event{
				Type:       memory.EventToolResult,
				ToolCallID: "",
				ToolName:   event.ToolName,
				ToolResult: event.ToolResult,
				IsError:    event.IsError,
			})
			r.ui.OnToolResult(event.ToolName, event.ToolResult, event.IsError)

		case QueryEventPermission:
			// 权限确认：调用 UI，结果写回 channel
			approved, _ := r.ui.ConfirmPermission(event.PermissionTool, event.PermissionArgs)
			if event.PermissionCh != nil {
				event.PermissionCh <- approved
			}

		case QueryEventContinue:
			r.ui.OnContinue(event.Iteration)

		case QueryEventFinal:
			finalAnswer = event.Content
			finalIteration = event.Iteration

		case QueryEventError:
			r.ui.OnError(event.Error)
			return "", fmt.Errorf("query loop error: %w", event.Error)
		}
	}

	// 步骤 6: 通知推理完成
	_ = finalIteration
	return finalAnswer, nil
}
```

- [ ] **Step 2: 替换 extractMemory 和 handleCompress 中的 fmt.Print**

将 `memory.go` 和 `query_engine.go` 中的 `fmt.Printf` 替换为 `r.ui.OnError` 或保留（这些是辅助信息，不属于核心 UI 流程）。第一阶段保留 `fmt.Print` 用于辅助信息输出，后续可进一步清理。

- [ ] **Step 3: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/agent/`
Expected: main.go 仍会报错，agent 包内部正常

- [ ] **Step 4: Commit**

```bash
git add internal/agent/query_engine.go
git commit -m "refactor(agent): queryEngine 使用 UI 接口输出事件

- printThink → ui.OnThink
- printDelta → ui.OnDelta
- printToolCall → ui.OnToolCall
- printToolResult → ui.OnToolResult
- printContinue → ui.OnContinue
- 新增 QueryEventPermission 处理：调用 ui.ConfirmPermission"
```

---

### Task 5: main.go 改造 + 移除 readline 依赖

**Files:**
- Modify: `main.go`
- Modify: `go.mod`

**Interfaces:**
- Consumes: `NewRunner(..., ui)` (from Task 3)

- [ ] **Step 1: 修改 main.go 创建 UI 并传入 Runner**

```go
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"agentic/internal/agent"
	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/session"
	"agentic/internal/tool"
	"agentic/internal/ui" // 新增
)

// loadEnvFile 不变

func main() {
	sessionsDir := flag.String("sessions", "./data/sessions", "sessions directory path")
	sessionID := flag.String("session", "", "resume a specific session by ID (optional)")
	envFile := flag.String("env", ".env", "env file path")
	flag.Parse()

	if err := loadEnvFile(*envFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: load env file failed: %v\n", err)
	}

	client, err := llm.NewOpenAIClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init llm client failed: %v\n", err)
		os.Exit(1)
	}

	sessions, err := session.NewSessionManager(*sessionsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init session manager failed: %v\n", err)
		os.Exit(1)
	}

	if *sessionID != "" {
		if err := sessions.Switch(*sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "switch session failed: %v\n", err)
			os.Exit(1)
		}
	}

	activeDir := sessions.ActiveSessionDir()

	history, err := memory.NewHistoryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init history store failed: %v\n", err)
		os.Exit(1)
	}

	summary, err := memory.NewSummaryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init summary store failed: %v\n", err)
		os.Exit(1)
	}

	memStore, err := memory.NewMemoryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init memory store failed: %v\n", err)
		os.Exit(1)
	}

	events := memory.NewEventStore(activeDir)
	extractor := memory.NewExtractor(client)
	retriever := memory.NewRetriever(history, summary, memStore, events)

	tools := tool.NewRegistry()
	tools.Register(tool.NewShellTool())
	tools.Register(tool.NewFileTool())

	// 创建 UI 实例（第一版用 BubbleUI，后续实现）
	// 暂时用 TextUI 保持编译通过
	uiInstance := ui.NewTextUI()

	runner := agent.NewRunner(client, history, summary, memStore, events, extractor, retriever, tools, sessions, uiInstance)
	if err := runner.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed: %v\n", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: 移除 readline 依赖**

Run: `cd /home/ubuntu/workspace/agentic && go mod tidy`
Expected: `readline` 从 go.mod 中移除（如果不再被其他地方引用）

- [ ] **Step 3: 验证编译和运行**

Run: `cd /home/ubuntu/workspace/agentic && go build -o agentic .`
Expected: 编译成功

- [ ] **Step 4: Commit**

```bash
git add main.go go.mod go.sum
git commit -m "refactor: main.go 创建 UI 实例并传入 Runner

- 新增 ui.NewTextUI() 作为临时 UI 实现
- NewRunner 调用添加 UI 参数
- 移除 readline 依赖"
```

---

### Task 6: Lip Gloss 样式定义

**Files:**
- Create: `internal/ui/styles.go`

**Interfaces:**
- Produces: 样式常量（后续所有 TUI 组件依赖）

- [ ] **Step 1: 创建样式文件**

```go
// internal/ui/styles.go
package ui

import "github.com/charmbracelet/lipgloss"

// ──────────────────────────────────────────────────────────
// TUI 样式定义
// ──────────────────────────────────────────────────────────

var (
	// Status bar
	StatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("238")).
			Padding(0, 1)

	StatusSeparatorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	// User input prefix
	UserPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	// Thinking indicator
	ThinkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Italic(true)

	// Tool call
	ToolPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Bold(true)

	ToolSummaryStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	ToolSuccessStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10"))

	ToolErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	// Separator line
	SeparatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	// Error message
	ErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Bold(true)

	// Success message
	SuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10"))

	// Muted/secondary text
	MutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	// Prompt style
	PromptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	// Answer label
	AnswerLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10")).
				Bold(true)
)

// Separator 返回一条横线。
func Separator(width int) string {
	if width <= 0 {
		width = 60
	}
	return SeparatorStyle.Render(strings.Repeat("─", width))
}
```

- [ ] **Step 2: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/ui/`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/ui/styles.go
git commit -m "feat(ui): 添加 TUI Lip Gloss 样式定义

- 状态栏、输入前缀、思考指示器、工具调用、分隔线等样式
- Separator 辅助函数"
```

---

### Task 7: BubbleUI 核心模型

**Files:**
- Create: `internal/ui/bubble.go`
- Modify: `internal/ui/styles.go` (添加 import)

**Interfaces:**
- Implements: `UI` interface (from Task 1)
- Produces: `BubbleUI` struct (后续 Task 8-12 依赖)

- [ ] **Step 1: 创建 BubbleUI 核心模型**

```go
// internal/ui/bubble.go
package ui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────
// Bubble Tea 消息类型
// ──────────────────────────────────────────────────────────

type deltaMsg struct{ content string }
type toolCallMsg struct{ name, args string }
type toolResultMsg struct {
	name    string
	result  string
	isError bool
}
type thinkMsg struct{ iteration int }
type continueMsg struct{ iteration int }
type finalMsg struct{ answer string }
type errorMsg struct{ err error }
type waitInputMsg struct{}
type inputDoneMsg struct{ input string }
type confirmMsg struct {
	tool string
	args string
}
type confirmDoneMsg struct{ approved bool }

// ──────────────────────────────────────────────────────────
// BubbleUI 核心模型
// ──────────────────────────────────────────────────────────

// BubbleUI 实现 UI 接口，使用 Bubble Tea 构建 TUI。
type BubbleUI struct {
	program *tea.Program

	// 子组件
	conversation *ConversationModel
	input        *InputModel
	status       *StatusModel
	toolView     *ToolViewModel

	// 状态
	waitingInput bool
	confirming   bool
	confirmCh    chan bool
	inputCh      chan string

	// 元数据
	sessionName string
	model       string
	round       int

	// token 统计（本轮）
	inputTokens  int
	outputTokens int
}

// NewBubbleUI 创建 BubbleUI 实例。
func NewBubbleUI() *BubbleUI {
	b := &BubbleUI{
		conversation: NewConversationModel(),
		input:        NewInputModel(),
		status:       NewStatusModel(),
		toolView:     NewToolViewModel(),
		confirmCh:    make(chan bool, 1),
		inputCh:      make(chan string, 1),
	}
	b.program = tea.NewProgram(b, tea.WithAltScreen())
	return b
}

// Init 实现 tea.Model 接口。
func (b *BubbleUI) Init() tea.Cmd {
	return tea.Batch(
		b.input.Init(),
		b.status.Init(),
	)
}

// Update 实现 tea.Model 接口。
func (b *BubbleUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch m := msg.(type) {
	case tea.KeyMsg:
		// 全局按键处理
		switch m.String() {
		case "ctrl+c":
			if b.confirming {
				// 确认中按 Ctrl+C = 拒绝
				b.confirmCh <- false
				b.confirming = false
				return b, nil
			}
			return b, tea.Quit
		case "esc":
			if b.toolView.IsOpen() {
				b.toolView.Close()
				return b, nil
			}
			if b.confirming {
				b.confirmCh <- false
				b.confirming = false
				return b, nil
			}
		}

		// 确认弹窗优先处理
		if b.confirming {
			return b.updateConfirm(m)
		}

		// 工具弹窗获得焦点时处理
		if b.toolView.IsOpen() {
			b.toolView, _ = b.toolView.Update(m)
			return b, nil
		}

		// 输入模式
		if b.waitingInput {
			return b.updateInput(m)
		}

	// UI 接口桥接消息
	case waitInputMsg:
		b.waitingInput = true
		b.input.Focus()
		return b, nil

	case inputDoneMsg:
		b.waitingInput = false
		b.inputCh <- m.input
		return b, nil

	case thinkMsg:
		b.conversation.AddThink(m.iteration)
		return b, nil

	case deltaMsg:
		b.conversation.AddDelta(m.content)
		return b, nil

	case toolCallMsg:
		b.toolView.AddTool(m.name, m.args)
		if !b.toolView.IsOpen() {
			b.toolView.Open()
		}
		return b, nil

	case toolResultMsg:
		b.toolView.SetResult(m.name, m.result, m.isError)
		return b, nil

	case confirmMsg:
		b.confirming = true
		b.toolView.SetConfirming(m.tool, m.args)
		return b, nil

	case confirmDoneMsg:
		b.confirming = false
		b.confirmCh <- m.approved
		return b, nil

	case continueMsg:
		b.conversation.AddContinue(m.iteration)
		b.inputTokens = 0
		b.outputTokens = 0
		return b, nil

	case finalMsg:
		b.conversation.AddFinal(m.answer)
		b.toolView.Close()
		return b, nil

	case errorMsg:
		b.conversation.AddError(m.err)
		return b, nil

	case tea.WindowSizeMsg:
		b.status.SetWidth(m.Width)
		b.conversation.SetSize(m.Width, m.Height-4) // 减去状态栏和输入栏高度
		b.input.SetWidth(m.Width)
		b.toolView.SetSize(m.Width, m.Height)
		return b, nil
	}

	// 子组件更新
	b.input, _ = b.input.Update(msg)
	b.status, _ = b.status.Update(msg)

	return b, tea.Batch(cmds...)
}

// View 实现 tea.Model 接口。
func (b *BubbleUI) View() string {
	// 状态栏
	statusBar := b.status.View()

	// 对话区
	conversationView := b.conversation.View()

	// 工具弹窗（覆盖在对话区上方）
	if b.toolView.IsOpen() {
		toolPopup := b.toolView.View()
		conversationView = toolPopup
	}

	// 输入栏
	inputView := b.input.View()

	return fmt.Sprintf("%s\n%s\n%s", statusBar, conversationView, inputView)
}

// ──────────────────────────────────────────────────────────
// UI 接口实现
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) ReadInput() (string, error) {
	b.program.Send(waitInputMsg{})
	input := <-b.inputCh
	if input == "" {
		return "", fmt.Errorf("empty input")
	}
	return input, nil
}

func (b *BubbleUI) OnThink(iteration int) {
	b.program.Send(thinkMsg{iteration})
}

func (b *BubbleUI) OnDelta(content string) {
	b.program.Send(deltaMsg{content})
}

func (b *BubbleUI) OnToolCall(name, args string) {
	b.program.Send(toolCallMsg{name, args})
}

func (b *BubbleUI) OnToolResult(name, result string, isError bool) {
	b.program.Send(toolResultMsg{name, result, isError})
}

func (b *BubbleUI) OnContinue(iteration int) {
	b.program.Send(continueMsg{iteration})
}

func (b *BubbleUI) OnFinal(answer string) {
	b.program.Send(finalMsg{answer})
}

func (b *BubbleUI) OnError(err error) {
	b.program.Send(errorMsg{err})
}

func (b *BubbleUI) ConfirmPermission(tool, args string) (bool, error) {
	b.program.Send(confirmMsg{tool, args})
	approved := <-b.confirmCh
	return approved, nil
}

func (b *BubbleUI) Close() error {
	b.program.Quit()
	return nil
}

// ──────────────────────────────────────────────────────────
// 内部按键处理
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		input := b.input.Value()
		b.input.SetValue("")
		b.program.Send(inputDoneMsg{input})
		return b, nil
	case "up":
		// TODO: 历史上翻
	case "down":
		// TODO: 历史下翻
	}
	b.input, _ = b.input.Update(msg)
	return b, nil
}

func (b *BubbleUI) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "tab":
		b.toolView.ConfirmToggle()
	case "right":
		b.toolView.ConfirmToggle()
	case "enter":
		approved := b.toolView.ConfirmSelection()
		b.program.Send(confirmDoneMsg{approved})
	}
	return b, nil
}

// ──────────────────────────────────────────────────────────
// 元数据设置
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) SetSessionName(name string) {
	b.sessionName = name
	b.status.SetSession(name)
}

func (b *BubbleUI) SetModel(model string) {
	b.model = model
	b.status.SetModel(model)
}

func (b *BubbleUI) UpdateTokens(input, output int) {
	b.inputTokens += input
	b.outputTokens += output
	b.status.SetTokens(b.inputTokens, b.outputTokens)
}
```

- [ ] **Step 2: 验证编译（会因为组件未实现而失败，跳过）**

此步骤预期失败，因为 `ConversationModel`、`InputModel`、`StatusModel`、`ToolViewModel` 尚未实现。继续下一个 Task。

- [ ] **Step 3: Commit**

```bash
git add internal/ui/bubble.go
git commit -m "feat(ui): 添加 BubbleUI 核心模型框架

- BubbleUI 实现 UI 接口和 tea.Model 接口
- 消息类型定义（delta, toolCall, confirm 等）
- program.Send 桥接 UI 接口和 Bubble Tea 事件循环
- 全局按键处理（Ctrl+C, Esc）
- 确认弹窗和工具弹窗的焦点管理"
```

---

### Task 8: Input 组件

**Files:**
- Create: `internal/ui/components/input.go`

**Interfaces:**
- Produces: `InputModel` (Task 7 依赖)

- [ ] **Step 1: 创建 Input 组件**

```go
// internal/ui/components/input.go
package components

import (
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// InputModel 输入栏组件。
type InputModel struct {
	textInput textinput.Model
	width     int
	focused   bool
}

// NewInputModel 创建输入栏组件。
func NewInputModel() InputModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务开始对话..."
	ti.Focus()
	return InputModel{
		textInput: ti,
	}
}

func (m InputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

func (m InputModel) View() string {
	prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true).Render("> ")
	input := m.textInput.View()

	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(
		"  /new /list /switch /delete /rename /memory /compress",
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", m.width)),
		prefix+input,
		hint,
	)
}

func (m *InputModel) SetValue(value string) {
	m.textInput.SetValue(value)
}

func (m *InputModel) Value() string {
	return m.textInput.Value()
}

func (m *InputModel) Focus() {
	m.textInput.Focus()
}

func (m *InputModel) Blur() {
	m.textInput.Blur()
}

func (m *InputModel) SetWidth(width int) {
	m.width = width
	m.textInput.Width = width - 4 // 减去前缀宽度
}
```

- [ ] **Step 2: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/ui/components/`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/ui/components/input.go
git commit -m "feat(ui): 添加输入栏组件

- 基于 bubbles/textinput
- 支持焦点管理、宽度自适应
- 显示 / 命令提示"
```

---

### Task 9: Conversation 组件

**Files:**
- Create: `internal/ui/components/conversation.go`

**Interfaces:**
- Produces: `ConversationModel` (Task 7 依赖)

- [ ] **Step 1: 创建 Conversation 组件**

```go
// internal/ui/components/conversation.go
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ConversationModel 对话历史组件。
type ConversationModel struct {
	lines   []string
	width   int
	height  int
	glamour *glamour.TermRenderer
}

// NewConversationModel 创建对话历史组件。
func NewConversationModel() *ConversationModel {
	g, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	return &ConversationModel{
		glamour: g,
	}
}

func (m *ConversationModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// AddThink 添加思考指示器。
func (m *ConversationModel) AddThink(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render("⏳ Thinking..."))
}

// AddDelta 添加增量文本（流式）。
func (m *ConversationModel) AddDelta(content string) {
	// 追加到最后一行，或新建一行
	if len(m.lines) > 0 {
		m.lines[len(m.lines)-1] += content
	} else {
		m.lines = append(m.lines, content)
	}
}

// AddContinue 添加继续推理指示。
func (m *ConversationModel) AddContinue(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render(fmt.Sprintf("🔄 Continuing... (iteration %d)", iteration)))
}

// AddFinal 添加最终回答（Markdown 渲染）。
func (m *ConversationModel) AddFinal(answer string) {
	// 清除之前的流式文本（最后一行是 delta 累积）
	if len(m.lines) > 0 {
		m.lines = m.lines[:len(m.lines)-1]
	}

	// 用 glamour 渲染
	rendered, err := m.glamour.Render(answer)
	if err != nil {
		rendered = answer
	}
	m.lines = append(m.lines, rendered)

	// 添加分隔线
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
}

// AddError 添加错误信息。
func (m *ConversationModel) AddError(err error) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render(fmt.Sprintf("❌ Error: %v", err)))
}

// AddToolSummary 添加工具调用摘要行。
func (m *ConversationModel) AddToolSummary(count int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("🔧 %d tool calls                        [press Enter to view]", count)))
}

// AddUserInput 添加用户输入。
func (m *ConversationModel) AddUserInput(input string) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("14")).
		Bold(true).
		Render("> ")+input)
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
}

func (m *ConversationModel) View() string {
	// 只显示最后 N 行，超出高度时截断
	maxLines := m.height - 2
	if maxLines < 1 {
		maxLines = 10
	}

	displayLines := m.lines
	if len(displayLines) > maxLines {
		displayLines = displayLines[len(displayLines)-maxLines:]
	}

	return strings.Join(displayLines, "\n")
}
```

- [ ] **Step 2: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/ui/components/`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/ui/components/conversation.go
git commit -m "feat(ui): 添加对话历史组件

- 支持流式文本累积（AddDelta）
- Markdown 渲染（glamour）
- 思考/继续/错误/工具摘要等状态行
- 自动截断超出高度的内容"
```

---

### Task 10: Status Bar 组件

**Files:**
- Create: `internal/ui/components/status.go`

**Interfaces:**
- Produces: `StatusModel` (Task 7 依赖)

- [ ] **Step 1: 创建 Status Bar 组件**

```go
// internal/ui/components/status.go
package components

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StatusModel 状态栏组件。
type StatusModel struct {
	session      string
	model        string
	inputTokens  int
	outputTokens int
	width        int
}

// NewStatusModel 创建状态栏组件。
func NewStatusModel() StatusModel {
	return StatusModel{
		session: "new",
		model:   "unknown",
	}
}

func (m StatusModel) Init() tea.Cmd {
	return nil
}

func (m StatusModel) Update(msg tea.Msg) (StatusModel, tea.Cmd) {
	return m, nil
}

func (m StatusModel) View() string {
	statusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("238")).
		Padding(0, 1)

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// 截断 session 名称
	session := m.session
	if len(session) > 20 {
		session = session[:17] + "..."
	}

	// token 显示
	tokenText := fmt.Sprintf("↑ %s ↓ %s",
		formatTokenCount(m.inputTokens),
		formatTokenCount(m.outputTokens),
	)

	left := fmt.Sprintf("agentic%s%s%s%s%s",
		sepStyle.Render(" │ "),
		"session: "+session,
		sepStyle.Render(" │ "),
		m.model,
		sepStyle.Render(" │ "),
	)

	bar := statusStyle.Render(left + tokenText)

	// 填充到宽度
	barWidth := lipgloss.Width(bar)
	if barWidth < m.width {
		bar += statusStyle.Render(fmt.Sprintf("%*s", m.width-barWidth, ""))
	}

	return bar
}

func (m *StatusModel) SetSession(name string) {
	m.session = name
}

func (m *StatusModel) SetModel(model string) {
	m.model = model
}

func (m *StatusModel) SetTokens(input, output int) {
	m.inputTokens = input
	m.outputTokens = output
}

func (m *StatusModel) SetWidth(width int) {
	m.width = width
}

// formatTokenCount 格式化 token 数量（1234 → 1.2k）。
func formatTokenCount(count int) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	return fmt.Sprintf("%.1fk", float64(count)/1000)
}
```

- [ ] **Step 2: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/ui/components/`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/ui/components/status.go
git commit -m "feat(ui): 添加状态栏组件

- 显示 session、model、token 用量
- token 格式化（1.2k）
- 宽度自适应填充"
```

---

### Task 11: Tool View 弹窗组件

**Files:**
- Create: `internal/ui/components/toolview.go`

**Interfaces:**
- Produces: `ToolViewModel` (Task 7 依赖)

- [ ] **Step 1: 创建 Tool View 组件**

```go
// internal/ui/components/toolview.go
package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// toolEntry 单个工具调用条目。
type toolEntry struct {
	name     string
	args     string
	result   string
	isError  bool
	executed bool // 是否已执行完成
}

// ToolViewModel 工具执行弹窗组件。
type ToolViewModel struct {
	tools    []toolEntry
	cursor   int
	open     bool
	width    int
	height   int

	// 确认状态
	confirming   bool
	confirmTool  string
	confirmArgs  string
	confirmYes   bool // true = Yes 选中
}

// NewToolViewModel 创建工具弹窗组件。
func NewToolViewModel() *ToolViewModel {
	return &ToolViewModel{}
}

func (m *ToolViewModel) Init() tea.Cmd {
	return nil
}

func (m *ToolViewModel) Update(msg tea.Msg) (*ToolViewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.tools)-1 {
				m.cursor++
			}
		case "enter":
			// 展开详情（TODO: 弹出详情子弹窗）
		case "q", "esc":
			m.Close()
		}
	}
	return m, nil
}

func (m *ToolViewModel) View() string {
	if !m.open {
		return ""
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	var lines []string
	lines = append(lines, titleStyle.Render("┌─ Tool Calls ─────────────────────────────────────────┐"))

	for i, tool := range m.tools {
		prefix := "  "
		if i == m.cursor {
			prefix = selectedStyle.Render("> ")
		}

		status := "⏳"
		statusStyle := mutedStyle
		if tool.executed {
			if tool.isError {
				status = "✗"
				statusStyle = errorStyle
			} else {
				status = "✓"
				statusStyle = successStyle
			}
		}

		// 截断 args
		args := tool.args
		if len(args) > 30 {
			args = args[:27] + "..."
		}

		// 截断 result
		result := tool.result
		if len(result) > 20 {
			result = result[:17] + "..."
		}

		line := fmt.Sprintf("%s🔧 %-20s %-30s %s %s",
			prefix,
			tool.name,
			args,
			statusStyle.Render(status),
			mutedStyle.Render(result),
		)
		lines = append(lines, line)
	}

	lines = append(lines, titleStyle.Render("└──────────────────────────────────────────────────────┘"))

	// 确认弹窗覆盖
	if m.confirming {
		confirmLines := m.renderConfirm()
		lines = append(lines, confirmLines...)
	}

	return borderStyle.Render(strings.Join(lines, "\n"))
}

func (m *ToolViewModel) renderConfirm() []string {
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	var lines []string
	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("┌─ Permission Required ────────────────────────────────┐"))
	lines = append(lines, fmt.Sprintf("  🔧 %s: %s", m.confirmTool, m.confirmArgs))
	lines = append(lines, mutedStyle.Render("  ⚠ 此操作需要确认"))

	yesStyle := mutedStyle
	noStyle := mutedStyle
	if m.confirmYes {
		yesStyle = selectedStyle
	} else {
		noStyle = selectedStyle
	}
	lines = append(lines, fmt.Sprintf("  %s    %s",
		yesStyle.Render("Yes, execute"),
		noStyle.Render("No, deny"),
	))
	lines = append(lines, titleStyle.Render("└──────────────────────────────────────────────────────┘"))

	return lines
}

// AddTool 添加工具调用。
func (m *ToolViewModel) AddTool(name, args string) {
	m.tools = append(m.tools, toolEntry{name: name, args: args})
}

// SetResult 设置工具执行结果。
func (m *ToolViewModel) SetResult(name, result string, isError bool) {
	for i := range m.tools {
		if m.tools[i].name == name && !m.tools[i].executed {
			m.tools[i].result = result
			m.tools[i].isError = isError
			m.tools[i].executed = true
			break
		}
	}
}

// Open 打开弹窗。
func (m *ToolViewModel) Open() {
	m.open = true
	m.cursor = 0
}

// Close 关闭弹窗。
func (m *ToolViewModel) Close() {
	m.open = false
	m.confirming = false
	m.tools = nil
}

// IsOpen 返回弹窗是否打开。
func (m *ToolViewModel) IsOpen() bool {
	return m.open
}

// SetConfirming 设置确认状态。
func (m *ToolViewModel) SetConfirming(tool, args string) {
	m.confirming = true
	m.confirmTool = tool
	m.confirmArgs = args
	m.confirmYes = true // 默认选中 Yes
}

// ConfirmToggle 切换 Yes/No 选择。
func (m *ToolViewModel) ConfirmToggle() {
	m.confirmYes = !m.confirmYes
}

// ConfirmSelection 返回当前选择并关闭确认。
func (m *ToolViewModel) ConfirmSelection() bool {
	m.confirming = false
	return m.confirmYes
}

func (m *ToolViewModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}
```

- [ ] **Step 2: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/ui/components/`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/ui/components/toolview.go
git commit -m "feat(ui): 添加工具执行弹窗组件

- 可导航的工具列表（j/k 上下移动）
- 执行状态标记（⏳/✓/✗）
- 权限确认内联显示（←/→ 切换，Enter 确认）
- Esc/q 关闭弹窗"
```

---

### Task 12: 组装 + 集成测试

**Files:**
- Modify: `internal/ui/bubble.go` (修复 import)
- Modify: `main.go` (切换到 BubbleUI)
- Modify: `go.mod` (添加 bubbles 依赖)

**Interfaces:**
- Consumes: 所有组件 (Task 8-11)
- Produces: 可运行的 TUI 程序

- [ ] **Step 1: 修复 bubble.go 的 import**

更新 `internal/ui/bubble.go` 的 import，引用 components 包：

```go
import (
	"fmt"
	"strings"

	"agentic/internal/ui/components"

	tea "github.com/charmbracelet/bubbletea"
)
```

更新 BubbleUI 结构体中的组件类型：

```go
type BubbleUI struct {
	program *tea.Program

	conversation *components.ConversationModel
	input        components.InputModel
	status       components.StatusModel
	toolView     *components.ToolViewModel

	// ... 其余不变
}
```

更新 NewBubbleUI：

```go
func NewBubbleUI() *BubbleUI {
	b := &BubbleUI{
		conversation: components.NewConversationModel(),
		input:        components.NewInputModel(),
		status:       components.NewStatusModel(),
		toolView:     components.NewToolViewModel(),
		confirmCh:    make(chan bool, 1),
		inputCh:      make(chan string, 1),
	}
	b.program = tea.NewProgram(b, tea.WithAltScreen())
	return b
}
```

- [ ] **Step 2: 更新 main.go 使用 BubbleUI**

```go
// 替换 TextUI 为 BubbleUI
uiInstance := ui.NewBubbleUI()
```

- [ ] **Step 3: 添加 bubbles 依赖**

Run: `cd /home/ubuntu/workspace/agentic && go get github.com/charmbracelet/bubbles && go mod tidy`

- [ ] **Step 4: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build -o agentic .`
Expected: 编译成功

- [ ] **Step 5: 运行测试**

Run: `cd /home/ubuntu/workspace/agentic && go test ./...`
Expected: 通过（如果有测试）

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "feat: 集成 BubbleUI 作为默认 TUI

- main.go 切换到 NewBubbleUI()
- 添加 bubbles 依赖
- 修复 bubble.go 组件引用
- 全量编译通过"
```

---

### Task 13: queryLoop 权限事件集成

**Files:**
- Modify: `internal/agent/query_loop.go`

**Interfaces:**
- Consumes: `QueryEvent` with permission fields (from Task 2)

- [ ] **Step 1: 修改 checkToolPermission 方法**

将 `checkToolPermission` 中的 TODO 替换为真正的权限事件 yield：

```go
func (lc *queryLoopContext) checkToolPermission(tc llm.ToolCall, iter int) bool {
	permResult := checkToolPermission(tc.Name, tc.Arguments)

	if permResult.Action == "deny" {
		errMsg := fmt.Sprintf("权限拒绝: %s", permResult.Message)
		lc.yieldToolError(tc, errMsg, iter)
		return false
	}

	if permResult.Action == "confirm" {
		// yield 权限确认事件，等待上层返回结果
		ch := make(chan bool, 1)
		lc.events <- QueryEvent{
			Type:             QueryEventPermission,
			PermissionRequired: true,
			PermissionTool:   tc.Name,
			PermissionArgs:   tc.Arguments,
			PermissionReason: permResult.Message,
			PermissionCh:     ch,
			Iteration:        iter + 1,
		}

		// 阻塞等待用户确认
		approved := <-ch
		if !approved {
			errMsg := "用户拒绝执行"
			lc.yieldToolError(tc, errMsg, iter)
			return false
		}
	}

	return true
}
```

- [ ] **Step 2: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build ./internal/agent/`
Expected: 编译成功

- [ ] **Step 3: Commit**

```bash
git add internal/agent/query_loop.go
git commit -m "feat(agent): queryLoop 集成权限确认事件

- checkToolPermission 在 confirm 时 yield QueryEventPermission
- 通过 PermissionCh 异步等待用户确认
- 拒绝时 yieldToolError 并跳过执行"
```

---

### Task 14: ui.go 清理 + runner.go 签名最终调整

**Files:**
- Modify: `internal/agent/runner.go` (prompt 传递给 UI)
- Modify: `internal/agent/ui.go` (保留辅助函数，移除 printXxx)

**Interfaces:**
- 最终整合

- [ ] **Step 1: runner.go 中传递 prompt 给 UI**

在 `Run()` 方法中，读取用户输入前将 prompt 信息传递给 UI：

```go
for round := 1; ; round++ {
	// 更新 UI 元数据
	if b, ok := r.ui.(*ui.BubbleUI); ok {
		b.SetModel(r.llm.Model())
		b.UpdateTokens(0, 0) // 重置本轮 token
	}

	input, err := r.ui.ReadInput()
	// ... 后续不变
}
```

- [ ] **Step 2: 保留 ui.go 中的辅助函数**

`ui.go` 中的 `printBanner`、`printAnswer` 等函数暂时保留，用于 `handleSessionCommand` 中的输出。后续可进一步迁移到 UI 接口。

- [ ] **Step 3: 验证编译**

Run: `cd /home/ubuntu/workspace/agentic && go build -o agentic .`
Expected: 编译成功

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: 最终整合 UI 接口到 Runner

- Run() 传递模型和 token 信息给 BubbleUI
- 保留 ui.go 辅助函数用于 session 命令输出
- 全量编译通过"
```
