package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/session"
	"agentic/internal/tool"

	"github.com/chzyer/readline"
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
	}
}

// newReadline 创建一个 readline 实例，用于逐行读取输入。
func newReadline(prompt string) (*readline.Instance, error) {
	return readline.NewEx(&readline.Config{
		Prompt: prompt,
	})
}

// Run 进入交互循环：读用户输入 -> QueryEngine -> 保存记忆。
//
// 流程：
//  1. 打印启动横幅和会话提示
//  2. 初始化临时会话
//  3. 进入主循环：
//     - 读取用户输入
//     - 处理会话管理命令（/ 开头）
//     - 调用 QueryEngine 执行任务
//     - 保存记忆（三层）
//     - 首次对话后持久化临时会话
//
// 输入 exit 可退出，/ 开头为会话管理命令。
// 启动时创建临时会话，只有真正对话后才落盘到 manifest。
func (r *Runner) Run(ctx context.Context) error {
	printBanner(r.sessions)
	printSessionHint()

	// 启动时使用临时会话：生成临时 ID，只设置路径，不创建目录。
	// 目录在首次对话写入时惰性创建，无对话则不留痕迹。
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

	// 清理上次异常退出留下的孤立临时目录。
	r.cleanOrphanTempDirs()

	rl, err := newReadline("")
	if err != nil {
		return fmt.Errorf("init readline failed: %w", err)
	}
	defer rl.Close()

	for round := 1; ; round++ {
		rl.SetPrompt(promptStyle.Render(fmt.Sprintf("[Round %d] You> ", round)))
		input, err := rl.Readline()
		if err == readline.ErrInterrupt || err == io.EOF {
			fmt.Printf("\n%s\n", mutedStyle.Render("Agent stopped by user."))
			return nil
		}
		if err != nil {
			return fmt.Errorf("read input failed: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}
		if strings.EqualFold(input, "exit") {
			fmt.Printf("\n%s\n", mutedStyle.Render("Agent stopped by user."))
			return nil
		}

		// 处理会话管理斜杠命令。
		if strings.HasPrefix(input, "/") {
			newRound, handled := r.handleSessionCommand(input)
			if handled {
				if newRound > 0 {
					round = newRound - 1 // -1 因为 for 循环末尾会 ++
				}
				continue
			}
			// 不是已知命令，提示用户
			fmt.Printf("\n%s\n", errorStyle.Render("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory"))
			round-- // 不消耗轮次
			continue
		}

		// 记录用户输入事件。
		r.events.Append(memory.Event{
			Type:    memory.EventUser,
			Round:   round,
			Content: input,
		})

		// 执行 QueryEngine：构建上下文 → 调用 queryLoop → 记录事件 → 返回结果。
		answer, err := r.queryEngine(ctx, round, input)
		if err != nil {
			return fmt.Errorf("query engine failed at round %d: %w", round, err)
		}

		printAnswer(round, answer)

		// 记录模型回答事件。
		r.events.Append(memory.Event{
			Type:    memory.EventAssistant,
			Round:   round,
			Content: answer,
			Model:   r.llm.Model(),
		})

		// ── 保存记忆（三层） ──
		// L1: 原始对话日志（兼容保留）。
		if err := r.history.Append(round, input, answer); err != nil {
			return fmt.Errorf("save history failed at round %d: %w", round, err)
		}

		// L2 + L3: LLM 提取摘要和记忆。
		r.extractMemory(ctx, round, input, answer)

		// 首次对话后，将临时会话持久化到 manifest。
		if r.isTemporary {
			if err := r.ensurePersisted(); err != nil {
				fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("保存会话失败: %v", err)))
			}
		}
	}
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
