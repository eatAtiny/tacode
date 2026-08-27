package agent

import (
	"context"
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
// Runner 结构体和构造器
// ──────────────────────────────────────────────────────────

// Runner 负责驱动 Agent 的交互循环执行。
//
// 架构（两层查询）：
//   - QueryEngine 层（queryEngine 方法，query_engine.go）：负责上层协调
//   - queryLoop 层（queryLoop 函数，query_loop.go）：负责核心循环
//
// 每条用户输入的处理调用链（REPL 主循环见 repl.go，one-shot 入口见 oneshot.go）：
//
//	Runner.Run() / RunOnce()
//	  └─ runQueryAsync()（REPL）/ 直接调用（one-shot）
//	       └─ queryEngine()
//	            ├─ prompt.BuildReActSystemPrompt()  ← 全静态 system（工具描述 + 指南）
//	            ├─ Retriever.BuildContextFallback() ← 记忆 preamble（仅首轮/会话切换）
//	            ├─ prompt.BuildUserTask()           ← 末尾的用户任务消息（每轮唯一变化）
//	            ├─ compactor.Prepare()              ← 组装完成后运行 s08 压缩管线
//	            ├─ queryLoop()                      ← 内联 ReAct 循环（模型调用前再 Prepare）
//	            └─ 消费 event channel               ← 转发到 UI + EventStore，返回答案 + 消息数组
//	  └─ 查询成功：r.messages = 完整消息数组（跨轮累积）
//	  └─ extractMemory()   ← 后台提取 L3 记忆（L2 摘要仅在压缩时生成——当前未
//	                         持久化到 summaries.jsonl，对话细节由跨轮累积
//	                         messages + EventStore 承载）
//	  └─ ensurePersisted() ← 临时会话首次对话后落盘
//
// 职责：
//   - 读取用户输入（repl.go）
//   - 处理斜杠命令（/new, /list, /switch 等，session.go）
//   - 调用 QueryEngine 执行任务
//   - 保存记忆（L1 原始日志 + L3 结构化记忆；L2 摘要仅在压缩时生成——当前
//     未持久化到 summaries.jsonl，对话细节由跨轮累积 messages + EventStore 承载）
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
