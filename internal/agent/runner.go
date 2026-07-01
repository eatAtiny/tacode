package agent

import (
	"context"
	"fmt"
	"strings"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/session"
	"agentic/internal/tool"
	"agentic/internal/ui"
)

// ReAct 最大循环次数，防止无限循环。
const maxIterations = 10

// ──────────────────────────────────────────────────────────
// Runner 结构体和主循环
// ──────────────────────────────────────────────────────────

// Runner 负责驱动 Agent 的交互循环执行。
//
// 架构：
//   - QueryEngine 层（queryEngine 方法）：负责上层协调
//   - queryLoop 层（queryLoop 函数）：负责核心循环
//
// 职责：
//   - 读取用户输入
//   - 处理会话管理命令（/new, /list, /switch 等）
//   - 调用 QueryEngine 执行任务
//   - 保存记忆（三层：L1 原始日志 + L2 摘要 + L3 结构化记忆）
type Runner struct {
	llm       *llm.OpenAIClient      // LLM 客户端
	history   *memory.HistoryStore    // L1 原始对话日志
	summary   *memory.SummaryStore    // L2 摘要
	memStore  *memory.MemoryStore     // L3 结构化记忆
	events    *memory.EventStore      // 事件日志
	extractor *memory.Extractor       // 记忆提取器
	retriever *memory.Retriever       // 记忆检索器
	tools     *tool.Registry          // 工具注册表
	sessions  *session.SessionManager // 会话管理器
	ui        ui.UI                   // UI 接口

	isTemporary        bool   // 临时会话：启动时创建，有对话后才落盘
	tempID             string // 临时会话 ID
	pendingSessionName string // /new 指定的会话名，ensurePersisted 时使用
}

// NewRunner 构造 Agent 执行器。
//
// 参数：
//   - client: LLM 客户端
//   - history: L1 原始对话日志
//   - summary: L2 摘要
//   - memStore: L3 结构化记忆
//   - events: 事件日志
//   - extractor: 记忆提取器
//   - retriever: 记忆检索器
//   - tools: 工具注册表
//   - sessions: 会话管理器
//   - uiInstance: UI 接口
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
	uiInstance ui.UI,
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

// Run 进入交互循环：读用户输入 -> QueryEngine -> 保存记忆。
//
// 流程：
//  1. 打印启动横幅和会话提示
//  2. 初始化临时会话
//  3. 进入主循环（channel select 模式）：
//     - 从 inputCh 读取用户输入
//     - 从 queryResultCh 读取查询结果
//     - 查询运行中输入 /stop 可取消
//
// 启动时创建临时会话，只有真正对话后才落盘到 manifest。
func (r *Runner) Run(ctx context.Context) error {
	// 启动临时会话
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

	// 设置 UI 初始状态
	if b, ok := r.ui.(*ui.BubbleUI); ok {
		b.SetSessionName("new")
		b.SetModel(r.llm.Model())
	}

	// 显示欢迎信息
	r.ui.Welcome(r.llm.Model())

	// 启动异步输入读取
	inputCh := r.ui.ReadInputChan()

	// 查询状态
	var queryResultCh <-chan queryResult
	var queryCancel context.CancelFunc
	var queryRunning bool
	var round int
	var currentInput string    // 当前查询的用户输入，用于保存记忆
	var inputForward chan string // 查询期间转发输入到此 channel（权限确认等）

	// 显示初始提示符
	fmt.Print("> ")

	for {
		select {
		case input, ok := <-inputCh:
			if !ok {
				// EOF，退出。
				if queryRunning {
					queryCancel()
				}
				return nil
			}

			if input == "" {
				if !queryRunning {
					fmt.Print("> ")
				}
				continue
			}

			if strings.EqualFold(input, "exit") {
				if queryRunning {
					queryCancel()
				}
				return nil
			}

			// 查询运行中：转发输入到 query 侧（/stop 优先）
			if queryRunning {
				if input == "/stop" {
					queryCancel()
					queryRunning = false
					inputForward = nil
					r.ui.OnMessage("⏹️  已停止")
					round++
					fmt.Print("> ")
				} else if inputForward != nil {
					// 转发给 query 侧（权限确认等场景）
					inputForward <- input
				}
				continue
			}

			// 空闲状态：处理输入
			if strings.HasPrefix(input, "/") {
				newRound, handled := r.handleSessionCommand(input)
				if handled {
					if newRound > 0 {
						round = newRound - 1
					}
				} else {
					r.ui.OnError(fmt.Errorf("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory"))
				}
				round++
				fmt.Print("> ")
				continue
			}

			// 普通输入：启动异步查询
			round++
			if b, ok := r.ui.(*ui.BubbleUI); ok {
				b.ResetTokens()
			}

			r.events.Append(memory.Event{
				Type:    memory.EventUser,
				Round:   round,
				Content: input,
			})

			queryCtx, cancel := context.WithCancel(ctx)
			queryCancel = cancel
			queryRunning = true
			currentInput = input
			inputForward = make(chan string, 1)

			queryResultCh = r.runQueryAsync(queryCtx, round, input, inputForward)

		case result, ok := <-queryResultCh:
			if !ok {
				queryResultCh = nil
				continue
			}

			queryRunning = false
			queryResultCh = nil
			inputForward = nil

			if result.err != nil {
				r.ui.OnError(result.err)
			} else {
				r.events.Append(memory.Event{
					Type:    memory.EventAssistant,
					Round:   round,
					Content: result.answer,
					Model:   r.llm.Model(),
				})

				// 保存记忆（后台执行）
				go func(round int, input, answer string) {
					if err := r.history.Append(round, input, answer); err != nil {
						r.ui.OnError(fmt.Errorf("save history failed at round %d: %w", round, err))
						return
					}
					r.extractMemory(ctx, round, input, answer)
					if r.isTemporary {
						if err := r.ensurePersisted(); err != nil {
							r.ui.OnError(fmt.Errorf("保存会话失败: %v", err))
						}
					}
				}(round, currentInput, result.answer)
			}

			fmt.Print("> ")
		}
	}
}

// runQueryAsync 在后台 goroutine 中执行查询，返回结果 channel。
func (r *Runner) runQueryAsync(ctx context.Context, round int, input string, inputForward <-chan string) <-chan queryResult {
	ch := make(chan queryResult, 1)
	go func() {
		answer, err := r.queryEngine(ctx, round, input, inputForward)
		ch <- queryResult{answer: answer, err: err}
	}()
	return ch
}

// handleListCommand 处理 /list 命令，管理 Pause/Resume 生命周期。
func (r *Runner) handleListCommand() (int, bool) {
	// 运行选择器（独占终端输入，主 UI 不用 Bubble Tea 所以无需暂停）。
	selected, err := session.RunSessionPicker(r.sessions.List(), r.sessions.ActiveID())
	if err != nil {
		r.ui.OnError(fmt.Errorf("选择器错误: %v", err))
		return 0, true
	}
	if selected == "" {
		return 0, true
	}
	// 用户选中了一个会话，执行切换。
	if err := r.sessions.Switch(selected); err != nil {
		r.ui.OnError(fmt.Errorf("切换失败: %v", err))
		return 0, true
	}
	r.isTemporary = false
	r.switchSession()
	meta := r.sessions.FindMeta(r.sessions.ActiveID())
	displayName := r.sessions.ActiveID()
	if meta != nil {
		displayName = meta.Name
	}
	r.ui.OnMessage(fmt.Sprintf("✅ 已切换到会话: %s", displayName))
	r.printSessionHistory()
	return 1, true
}

// trimArgs 截断工具参数用于展示。
func trimArgs(args string) string {
	if len(args) > 60 {
		return args[:60] + "..."
	}
	return args
}

// toolCallSignature 生成工具调用的签名，用于检测重复调用。
//
// 签名格式：工具名1:参数1|工具名2:参数2|...
//
// 参数：
//   - calls: 工具调用列表
//
// 返回：
//   - string: 签名字符串，空列表返回空字符串
func toolCallSignature(calls []llm.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	var parts []string
	for _, tc := range calls {
		parts = append(parts, fmt.Sprintf("%s:%s", tc.Name, tc.Arguments))
	}
	return strings.Join(parts, "|")
}
