package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/prompt"
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
// 架构（两层查询）：
//   - QueryEngine 层（queryEngine 方法）：负责上层协调
//   - queryLoop 层（queryLoop 函数）：负责核心循环
//
// 每条用户输入的处理调用链：
//
//	Runner.Run()                          ← REPL 主循环，select 监听输入/结果
//	  └─ runQueryAsync()                  ← 启动后台 goroutine
//	       └─ queryEngine()                ← 上层协调（QueryEngine 层）
//	            ├─ Retriever.BuildContext() ← 步骤 1: 构建三层记忆上下文
//	            ├─ CheckAndCompress()       ← 步骤 2: 自动压缩检查
//	            ├─ BuildReActPrompt()       ← 步骤 3: 构建 System/User Prompt
//	            ├─ queryLoop()              ← 步骤 4: 启动核心循环，返回 event channel
//	            │    └─ runLoop()           ←   步骤 4a-4f 的 while 循环
//	            │         ├─ callLLMStream() ←   步骤 4a: 调用 LLM 流式接口
//	            │         ├─ executeToolCalls() ← 步骤 4b: 执行工具调用
//	            │         ├─ checkAndCompressContext() ← 步骤 4c: 检查并压缩上下文
//	            │         ├─ detectDuplicateAndWarn() ← 步骤 4d: 检测重复调用
//	            │         └─ generateFinalSummary()   ← 步骤 4e: 超限时生成总结
//	            └─ 消费 event channel       ← 步骤 5: 转发事件到 UI + EventStore
//	  └─ 返回 result channel               ← 步骤 6: Runner 收到最终结果
//	  └─ extractMemory()                   ← 步骤 7: 后台提取 L2 摘要 + L3 记忆
//	  └─ ensurePersisted()                 ← 步骤 8: 临时会话首次对话后落盘
//
// 职责：
//   - 读取用户输入
//   - 处理会话管理命令（/new, /list, /switch 等）
//   - 调用 QueryEngine 执行任务
//   - 保存记忆（三层：L1 原始日志 + L2 摘要 + L3 结构化记忆）
type Runner struct {
	llm       *llm.OpenAIClient       // LLM 客户端
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

	sessionStartTime time.Time // 会话开始时间（环境元数据注入，会话内不变）

	promptBuilder *prompt.Builder // 提示词构建器（预构建 system prompt，封装消息组装）
}

// NewRunner 构造 Agent 执行器。
//
// 参数（按初始化顺序）：
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
// 主循环流程（channel select 模式）：
//
//	循环开始
//	  ├─ case input, ok := <-inputCh         ← 收到用户输入
//	  │    ├─ "exit"  → return（退出）
//	  │    ├─ "/stop" → queryCancel()（中断正在运行的查询）
//	  │    ├─ "/xxx"  → handleSessionCommand()（会话管理命令）
//	  │    └─ 其他     → runQueryAsync()（启动异步查询）
//	  │                   ├─ 记录 EventUser
//	  │                   ├─ 重置 token 计数
//	  │                   ├─ 创建 queryCtx + cancel
//	  │                   └─ 启动 goroutine 调用 queryEngine()
//	  │
//	  └─ case result, ok := <-queryResultCh  ← 收到查询结果
//	       ├─ 记录 EventAssistant
//	       ├─ go history.Append()             ← 后台保存 L1
//	       ├─ go extractMemory()              ← 后台提取 L2 + L3
//	       └─ go ensurePersisted()            ← 首次对话后落盘临时会话
//
// 启动时创建临时会话，只有真正对话后才落盘到 manifest。
func (r *Runner) Run(ctx context.Context) error {
	// ── 启动临时会话 ──
	// 临时会话不在 manifest 中，首次对话后通过 ensurePersisted 落盘。
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

	// 记录会话开始时间（用于环境元数据注入，会话内不变）。
	r.sessionStartTime = time.Now()

	// 预构建提示词 Builder（system prompt 构造一次，整个会话复用）。
	var guides []prompt.ToolGuide
	for _, name := range r.tools.Names() {
		t := r.tools.Get(name)
		guides = append(guides, prompt.ToolGuide{
			Name:  t.Name(),
			Guide: t.PromptGuide(),
		})
	}
	r.promptBuilder = prompt.NewBuilder(r.tools.Descriptions(), guides)

	// 设置 UI 初始状态。
	r.ui.SetSessionName("new")
	r.ui.SetModel(r.llm.Model())

	// 显示欢迎信息。
	r.ui.Welcome(r.llm.Model())

	// ── 启动异步输入读取 ──
	// inputCh 是后台 goroutine 持续读取用户输入的 channel。
	inputCh := r.ui.ReadInputChan()

	// ── 查询状态变量 ──
	var queryResultCh <-chan queryResult // 查询结果 channel（nil 表示无运行中的查询）
	var queryCancel context.CancelFunc   // 取消函数（用于 /stop）
	var queryRunning bool                // 是否有查询正在运行
	var round int                        // 当前轮次号
	var currentInput string              // 当前查询的用户输入，用于保存记忆
	var inputForward chan string         // 查询期间转发输入到此 channel（权限确认等）

	// 显示初始提示符（立即 flush 确保在用户输入前显示）。
	fmt.Print("> ")
	os.Stdout.Sync()

	for {
		select {
		// ──────────────────────────────────────────
		// 分支 A: 收到用户输入
		// ──────────────────────────────────────────
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
					os.Stdout.Sync()
				}
				continue
			}

			// "exit" 退出程序。
			if strings.EqualFold(input, "exit") {
				if queryRunning {
					queryCancel()
				}
				return nil
			}

			// ── 子分支 A1: 查询运行中 ──
			// 输入转发给查询侧（/stop 优先）。
			if queryRunning {
				if input == "/stop" {
					queryCancel()
					queryRunning = false
					inputForward = nil
					r.ui.OnMessage("⏹️  已停止")
					round++
					fmt.Print("> ")
					os.Stdout.Sync()
				} else if inputForward != nil {
					// 转发给 query 侧（权限确认等场景）。
					inputForward <- input
				}
				continue
			}

			// ── 子分支 A2: 空闲状态，处理输入 ──
			if strings.HasPrefix(input, "/") {
				// 处理会话管理命令。
				newRound, handled := r.handleSessionCommand(input)
				if handled {
					if newRound > 0 {
						round = newRound - 1
					}
				} else {
					r.ui.OnError(fmt.Errorf("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /context, /compress, /memory"))
				}
				round++
				fmt.Print("> ")
				os.Stdout.Sync()
				continue
			}

			// ── 子分支 A3: 普通输入，启动异步查询 ──
			round++
			r.ui.ResetTokens()

			// 记录用户输入事件。
			r.events.Append(memory.Event{
				Type:    memory.EventUser,
				Round:   round,
				Content: input,
			})

			// 创建可取消的 context（用于 /stop）。
			queryCtx, cancel := context.WithCancel(ctx)
			queryCancel = cancel
			queryRunning = true
			currentInput = input
			inputForward = make(chan string, 1)

			// 启动后台 goroutine 执行查询。
			// queryResultCh 收到结果后触发下面的分支 B。
			queryResultCh = r.runQueryAsync(queryCtx, round, input, inputForward)

		// ──────────────────────────────────────────
		// 分支 B: 收到查询结果
		// ──────────────────────────────────────────
		case result, ok := <-queryResultCh:
			if !ok {
				queryResultCh = nil
				continue
			}

			// 清理查询状态。
			queryRunning = false
			queryResultCh = nil
			inputForward = nil

			if result.err != nil {
				r.ui.OnError(result.err)
			} else {
				// 记录助手回答事件。
				r.events.Append(memory.Event{
					Type:    memory.EventAssistant,
					Round:   round,
					Content: result.answer,
					Model:   r.llm.Model(),
				})

				// ── 后台保存记忆 ──
				// 不阻塞主循环，在独立 goroutine 中执行：
				//   1. 保存 L1 原始对话记录
				//   2. 提取 L2 摘要 + L3 记忆
				//   3. 首次对话后临时会话落盘
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
			os.Stdout.Sync()
		}
	}
}

// runQueryAsync 在后台 goroutine 中执行查询，返回结果 channel。
//
// 这是 Runner.Run() → queryEngine() 的桥梁：
//   - 创建带缓冲的 channel（容量 1）
//   - 启动 goroutine 调用 queryEngine()
//   - 立即返回 channel（非阻塞）
//   - goroutine 完成后写入结果并关闭 channel
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

// singleCallSignature 生成单个工具调用的签名，用于检测重复调用。
//
// 签名格式：工具名:参数JSON
// 与之前的 batch 签名不同，逐条检测能发现单个 toolCall 级别的重复。
//
// 返回空字符串表示空的 tool call。
func singleCallSignature(tc llm.ToolCall) string {
	if tc.Name == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", tc.Name, tc.Arguments)
}
