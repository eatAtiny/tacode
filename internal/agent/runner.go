package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"

	"agentic/internal/config"
	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/session"
	"agentic/internal/tool"
	"agentic/internal/ui"
	"agentic/internal/ui/bubble"
)

// ReAct 最大循环次数，防止无限循环。
// 可通过 config（MaxIterations）覆盖；无 config 时保持 10。
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
//	            │         ├─ prepareIfNeeded() ← 步骤 4c: 模型调用前运行 s08 压缩管线
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
	llm        *llm.OpenAIClient       // LLM 客户端
	history    *memory.HistoryStore    // L1 原始对话日志
	summary    *memory.SummaryStore    // L2 摘要
	memStore   *memory.MemoryStore     // L3 会话级结构化记忆（feedback 类，三级最内层）
	globalMem  *memory.MemoryStore     // L3 全局记忆（user 类，跨项目），nil = 未启用
	projectMem *memory.MemoryStore     // L3 项目级记忆（project/reference 类，跨会话），nil = 未启用
	events     *memory.EventStore      // 事件日志
	extractor  *memory.Extractor       // 记忆提取器
	retriever  *memory.Retriever       // 记忆检索器
	tools      *tool.Registry          // 工具注册表
	sessions   *session.SessionManager // 会话管理器
	ui         ui.UI                   // UI 接口
	chatUI     *bubble.BubbleUI        // 聊天 TUI 实现（非 nil = 聊天界面模式，tea 程序由 Run 启动）
	config     *config.Config          // 运行时配置（nil = 全部默认）

	isTemporary        bool   // 临时会话：启动时创建，有对话后才落盘
	tempID             string // 临时会话 ID
	pendingSessionName string // /new 指定的会话名，ensurePersisted 时使用

	messages []llm.ChatMessage // 跨轮累积的对话消息（不含 system/preamble，仅累积对话本身）

	memoryPreamble string     // 记忆 preamble 缓存（<system-reminder> 内容，会话内字节稳定，仅切换/首轮重建）
	compactor      *Compactor // s08 四步压缩管线（nil = 禁用）

	balanceFailCount atomic.Int32 // 连续余额查询失败次数（达到阈值时提示一次，防止静默失效）

	// pendingInputs 查询运行中排队的用户输入（查询结束后自动作为下一轮处理）。
	// 仅 Run() 主循环 goroutine 访问，无需锁。
	pendingInputs []string
	// permWaiting 是否有权限确认正在等待输入（queryEngine 设置，主循环读取）。
	// 用 atomic 跨 goroutine 同步：queryEngine 在 ConfirmPermission 前后翻转。
	permWaiting atomic.Bool
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
//     （聊天 TUI：BubbleUI 传入后由 Run() 启动 tea 程序，Runner 主循环不阻塞；
//     one-shot/子 agent：TextUI 无 Start，RunOnce 走原路径）
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
	r := &Runner{
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
	// 聊天 TUI 接线：UI 为 BubbleUI 时记录类型断言，
	// Run() 用它启动 tea 程序（TextUI 等 headless 实现此字段为 nil，不受影响）。
	if bui, ok := uiInstance.(*bubble.BubbleUI); ok {
		r.chatUI = bui
	}
	return r
}

// SetConfig 注入运行时配置。
// 配置为 nil 时全部使用代码默认值（行为与未配置时完全一致）。
// 通常在 NewRunner 之后、Run/RunOnce 之前调用。
func (r *Runner) SetConfig(cfg *config.Config) {
	r.config = cfg
}

// SetMemoryStores 注入全局与项目级记忆 store（三级记忆的外两层）。
//   - globalStore: 全局记忆（user 类，跨项目），extractMemory 写入 user/feedback
//   - projectStore: 项目级记忆（project/reference 类，跨会话），extractMemory 写入 project/reference
//
// 同时同步到 Retriever，使 BuildContext 合并这两层记忆。
func (r *Runner) SetMemoryStores(globalStore, projectStore *memory.MemoryStore) {
	r.globalMem = globalStore
	r.projectMem = projectStore
	if r.retriever != nil {
		r.retriever.SetGlobalMemory(globalStore)
		r.retriever.SetProjectMemory(projectStore)
	}
}

// SetCompactor 注入 s08 四步压缩管线。
// nil = 禁用压缩（保持旧行为）。在 Run/RunOnce 之前调用。
func (r *Runner) SetCompactor(c *Compactor) {
	r.compactor = c
}

// isChatUI 判断是否聊天 TUI 模式（BubbleUI 全 tea 界面）。
// 聊天界面下 tea 程序独占终端渲染，直接写 os.Stdout 会污染界面，相关输出一律跳过。
func (r *Runner) isChatUI() bool { return r.chatUI != nil }

// printPrompt 打印主屏输入提示符 "> "（追加式模型：输入框即流末尾的提示符）。
// 聊天 TUI 模式：提示符由 textarea 渲染，跳过（写 os.Stdout 会污染 alt screen）。
func (r *Runner) printPrompt() {
	if r.isChatUI() {
		return
	}
	fmt.Print("> ")
	os.Stdout.Sync()
}

// maxIter 返回 ReAct 最大循环次数。
// 优先使用 config 中的显式设置，否则回退到代码默认 10。
func (r *Runner) maxIter() int {
	if r.config != nil && r.config.MaxIterations != nil {
		return *r.config.MaxIterations
	}
	return maxIterations
}

// resultLimit 返回工具结果截断上限（字符数）。
// 优先使用 config 中的显式设置，否则回退到代码默认 8000。
func (r *Runner) resultLimit() int {
	if r.config != nil && r.config.ResultLimit != nil {
		return *r.config.ResultLimit
	}
	return defaultResultLimit
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
	// ── 启动聊天 TUI（tea 程序） ──
	// 聊天界面（BubbleUI）后台启动 tea.Program，Runner 主循环不阻塞：
	//   - 事件方法（OnThink/OnDelta/...）→ Program.Send → ChatModel 渲染对话区
	//   - 输入从 ChatModel 的 textarea 提交 channel（ReadInputChan）读取
	// 必须在 Welcome 之前启动：send 在 program 为 nil 时丢弃消息，未启动就
	// 调事件方法会丢欢迎信息。
	// one-shot / 子 agent 模式（TextUI 等）类型断言失败，走原路径不受影响。
	if bui, ok := r.ui.(*bubble.BubbleUI); ok {
		if err := bui.Start(); err != nil {
			r.ui.OnError(fmt.Errorf("聊天界面启动失败: %v", err))
			return err
		}
		defer bui.Close()
	}

	// ── 启动临时会话 ──
	// 临时会话不在 manifest 中，首次对话后通过 ensurePersisted 落盘。
	if err := r.initTempSession(); err != nil {
		return err
	}

	// 显示欢迎信息。
	r.ui.Welcome(r.llm.Model())

	// ── 启动状态初始化（footer 状态栏立即有数据） ──
	// 1. 上下文占用：初始为空（0 字符），显示 "上下文 0/50K (0%)"。
	// 2. 余额：自动查询一次（无需手动 /balance），成功显示在 footer；
	//    失败静默（启动不打扰，后续 /balance 可手动查询）。
	if r.compactor != nil {
		r.ui.UpdateContext(0, r.compactor.Limit())
	}
	go r.queryBalance()

	// ── 启动异步输入读取 ──
	// 追加式模型：统一走 UI 的 raw 输入通道（ReadInputChan），
	// 输入即流末尾的提示符行，无 tea 输入桥接。
	var inputCh <-chan string
	inputCh = r.ui.ReadInputChan()

	// ── 查询状态变量 ──
	var queryResultCh <-chan queryResult // 查询结果 channel（nil 表示无运行中的查询）
	var queryCancel context.CancelFunc   // 取消函数（用于 /stop）
	var queryRunning bool                // 是否有查询正在运行
	var round int                        // 当前轮次号
	var currentInput string              // 当前查询的用户输入，用于保存记忆
	var inputForward chan string         // 查询期间转发输入到此 channel（权限确认等）

	// 排队输入重放 channel：查询结束时把 pendingInputs 弹出一条投递到此，
	// 与 inputCh 走同一套分支 A 处理逻辑（空输入/exit/命令/查询）。
	pendingReplayCh := make(chan string, 4)

	// 显示初始提示符（立即 flush 确保在用户输入前显示）。
	r.printPrompt()

	for {
		select {
		// ──────────────────────────────────────────
		// 分支 A: 收到用户输入（主输入通道 或 排队重放）
		// ──────────────────────────────────────────
		case input, ok := <-inputCh:
			if !ok {
				// EOF，退出。
				if queryRunning {
					queryCancel()
				}
				return nil
			}
			// 排队输入重放与主输入走同一处理逻辑。
			if exit := r.handleInput(input, ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &r.pendingInputs); exit {
				return nil
			}

		// 分支 A2: 排队输入重放（查询结束后自动处理用户查询中输入的下一条）。
		case input := <-pendingReplayCh:
			if exit := r.handleInput(input, ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &r.pendingInputs); exit {
				return nil
			}

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
				// 跨轮累积：查询成功后保存本次完整消息数组。
				// 失败/取消时不更新（保留上一轮累积），避免部分轮次消息进入下轮。
				if result.messages != nil {
					r.messages = result.messages
				}

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
						if err := r.ensurePersisted(input); err != nil {
							r.ui.OnError(fmt.Errorf("保存会话失败: %v", err))
						}
					}
				}(round, currentInput, result.answer)
			}

			r.printPrompt()

			// 查询结束：自动处理查询期间排队的输入（Bug 2 修复——用户查询中
			// 输入的下一条消息在回答完成后自动作为下一轮输入发送）。
			// 只重放第一条，剩余继续排队（每轮查询结束处理一条，保持顺序）。
			if len(r.pendingInputs) > 0 {
				next := r.pendingInputs[0]
				r.pendingInputs = r.pendingInputs[1:]
				pendingReplayCh <- next
			}
		}
	}
}

// handleInput 处理一条用户输入（主输入通道与排队重放共用）。
//
// 参数（指针，runInput 状态由 Run() 主循环持有，本方法修改后由主循环继续使用）：
//   - queryResultCh: 查询结果 channel（启动查询时写入，nil 表示无运行中的查询）
//   - queryCancel:   取消函数（/stop 用）
//   - queryRunning:  是否有查询正在运行
//   - round:         当前轮次号
//   - currentInput:  当前查询的用户输入（保存记忆用）
//   - inputForward:  权限确认转发 channel
//   - pendingInputs: 查询运行中排队的输入
//
// 返回 true 表示退出（exit 命令或 EOF）。
func (r *Runner) handleInput(
	input string,
	ctx context.Context,
	queryResultCh *<-chan queryResult,
	queryCancel *context.CancelFunc,
	queryRunning *bool,
	round *int,
	currentInput *string,
	inputForward *chan string,
	pendingInputs *[]string,
) bool {
	if input == "" {
		if !*queryRunning {
			r.printPrompt()
		}
		return false
	}

	// "exit" 退出程序。
	if strings.EqualFold(input, "exit") {
		if *queryRunning {
			(*queryCancel)()
		}
		return true
	}

	// ── 查询运行中 ──
	// 输入处理策略：
	//   - /stop        → 取消当前查询
	//   - 权限确认在等 → 转发给 query 侧（ConfirmPermission 阻塞读 inputForward）
	//   - 其他          → 排队（pendingInputs），查询结束后自动作为下一轮输入
	// 旧实现无条件 `inputForward <- input`：无权限确认时 channel 无人读，
	// 第 2 条输入即阻塞冻结主循环（Bug 2 根因）。
	if *queryRunning {
		if input == "/stop" {
			(*queryCancel)()
			*queryRunning = false
			*inputForward = nil
			r.ui.OnMessage("⏹️  已停止")
			*round++
			r.printPrompt()
		} else if r.permWaiting.Load() {
			// 权限确认：转发给 ConfirmPermission（阻塞读 inputForward）。
			// 非阻塞发送避免 channel 满时冻结主循环；缓冲 1 一般有空位。
			select {
			case *inputForward <- input:
			default:
				// 缓冲满（异常）：丢弃，避免阻塞。
			}
		} else {
			// 普通查询运行中：排队，查询结束后自动处理。
			*pendingInputs = append(*pendingInputs, input)
			r.ui.OnMessage("💬 输入已排队，当前查询结束后自动发送")
		}
		return false
	}

	// ── 空闲状态，处理输入 ──
	if strings.HasPrefix(input, "/") {
		// 处理会话管理命令。
		newRound, handled := r.handleSessionCommand(input)
		if handled {
			if newRound > 0 {
				*round = newRound - 1
			}
		} else {
			r.ui.OnError(fmt.Errorf("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory, /reload, /balance"))
		}
		*round++
		r.printPrompt()
		return false
	}

	// ── 普通输入，启动异步查询 ──
	*round++

	// 记录用户输入事件。
	r.events.Append(memory.Event{
		Type:    memory.EventUser,
		Round:   *round,
		Content: input,
	})

	// 创建可取消的 context（用于 /stop）。
	queryCtx, cancel := context.WithCancel(ctx)
	*queryCancel = cancel
	*queryRunning = true
	*currentInput = input
	*inputForward = make(chan string, 1)

	// 启动后台 goroutine 执行查询。
	// queryResultCh 收到结果后触发分支 B。
	*queryResultCh = r.runQueryAsync(queryCtx, *round, input, *inputForward)
	return false
}

// initTempSession 启动临时会话：分配 ID、将各 store 指向临时目录。
//
// 临时会话不在 manifest 中，首次对话后通过 ensurePersisted 落盘。
// Run() 和 RunOnce() 共用此初始化逻辑。
func (r *Runner) initTempSession() error {
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
	r.syncCompactorPaths(tempDir)
	r.cleanOrphanTempDirs()
	return nil
}

// RunOnce 执行一次查询并返回最终答案（one-shot / headless 模式）。
//
// 与 Run() 的区别：不进入 REPL 循环，同步执行单次查询后返回。
// 行为与 REPL 模式一致：复用 queryEngine（含记忆上下文构建、工具调用、
// 权限确认）、成功后保存 L1/L2/L3 记忆、临时会话落盘。
//
// 使用场景：
//   - CLI 一次性运行（go run . -one-shot "任务"）
//   - 脚本 / CI / 子 agent 场景（配合 TextUI）
//
// 权限：one-shot 场景配合 TextUI 使用，ConfirmPermission 默认放行。
// inputForward 传 nil 安全——TextUI.ConfirmPermission 不读该参数。
//
// 注意：每次调用会重新初始化临时会话（分配新 ID），不适合在同一个 Runner
// 实例上连续调用多次。计划用法：CLI one-shot 为独立进程；子 agent 场景
// 每个子 agent 使用独立的 Runner 实例。
//
// 返回：
//   - string: 最终回答文本（LLM 的完整回复）
//   - error: 查询失败或记忆保存失败
func (r *Runner) RunOnce(ctx context.Context, input string) (string, error) {
	// 空输入保护。
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("one-shot 输入为空")
	}

	// ── 启动临时会话（与 Run() 共用） ──
	if err := r.initTempSession(); err != nil {
		return "", err
	}

	// ── 记录用户输入事件（与 Run() 一致） ──
	r.events.Append(memory.Event{
		Type:    memory.EventUser,
		Round:   1,
		Content: input,
	})

	// ── 同步执行单次查询 ──
	// round=1，inputForward=nil（TextUI 权限默认放行，不读此参数）。
	answer, messages, err := r.queryEngine(ctx, 1, input, nil, nil)
	if err != nil {
		return "", fmt.Errorf("one-shot 查询失败: %w", err)
	}
	r.messages = messages

	// ── 记录助手回答事件（与 Run() 一致） ──
	r.events.Append(memory.Event{
		Type:    memory.EventAssistant,
		Round:   1,
		Content: answer,
		Model:   r.llm.Model(),
	})

	// ── 保存记忆（与 Run() 分支 B 一致） ──
	if err := r.history.Append(1, input, answer); err != nil {
		return "", fmt.Errorf("save history failed: %w", err)
	}
	r.extractMemory(ctx, 1, input, answer)
	if r.isTemporary {
		if err := r.ensurePersisted(input); err != nil {
			return "", fmt.Errorf("保存会话失败: %w", err)
		}
	}

	return answer, nil
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
	// 在调用 goroutine 前拷贝 slice header，避免与主循环后续写 r.messages 产生竞争。
	// queryEngine 只读此拷贝；主循环在收到结果（goroutine 完成）后才写回 r.messages。
	conversation := r.messages
	go func() {
		answer, messages, err := r.queryEngine(ctx, round, input, inputForward, conversation)
		ch <- queryResult{answer: answer, messages: messages, err: err}
	}()
	return ch
}

// handleListCommand 处理 /list 命令。
// 聊天 TUI（BubbleUI）下选择器融合进主渲染循环（不另起 tea 程序）；
// TextUI/headless 下用独立 tea 程序前台运行。
func (r *Runner) handleListCommand() (int, bool) {
	// 运行选择器（UI 接口统一入口，实现差异在各 UI）。
	selected, err := r.ui.RunSessionPicker(r.sessions.List(), r.sessions.ActiveID())
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
