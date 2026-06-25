package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/prompt"
	"agentic/internal/session"
	"agentic/internal/tool"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/chzyer/readline"
	openai "github.com/sashabaranov/go-openai"
)

// ReAct 最大循环次数，防止无限循环。
const maxIterations = 10

// TodoItem 表示规划中的一个待办步骤。
type TodoItem struct {
	ID      int    `json:"id"`
	Content string `json:"content"` // 步骤描述
	Status  string `json:"status"`  // "pending" | "done" | "failed"
}

// ──────────────────────────────────────────────────────────
// Lip Gloss 样式定义
// ──────────────────────────────────────────────────────────

var (
	// bannerStyle 用于顶部横幅，紫色圆角边框。
	bannerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("99")).
			Padding(0, 2).
			Align(lipgloss.Center).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("99"))

	// promptStyle 用于用户输入提示。
	promptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)

	// answerLabelStyle 用于 Agent 回答的标签行。
	answerLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)

	// thinkStyle 用于"思考中"等灰色提示。
	thinkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)

	// toolTitleStyle 用于工具调用的标题栏。
	toolTitleStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)

	// toolBoxStyle 用于工具调用的边框。
	toolBoxStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("11")).
			Padding(0, 1)

	// errorStyle 用于错误信息。
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)

	// successStyle 用于成功信息。
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	// mutedStyle 用于次要信息。
	mutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// reActBoxStyle 用于 ReAct 循环的提示框。
	reActBoxStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("11")).
			Padding(0, 1)

	// reActDoneBoxStyle 用于 ReAct 完成的提示框。
	reActDoneBoxStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10")).
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("10")).
				Padding(0, 1)

	// planBoxStyle 用于规划阶段的提示框。
	planBoxStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("13")).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("13")).
			Padding(0, 1)

	// todoPendingStyle 用于待执行的 todo 项。
	todoPendingStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// todoDoneStyle 用于已完成的 todo 项。
	todoDoneStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))

	// todoFailedStyle 用于失败的 todo 项。
	todoFailedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	// todoCurrentStyle 用于正在执行的 todo 项。
	todoCurrentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)

	// memoryBoxStyle 用于记忆信息的提示框。
	memoryBoxStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("13")).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("13")).
			Padding(0, 1)
)

// glamourRender 用于将 Markdown 渲染为漂亮的终端输出。
var glamourRender *glamour.TermRenderer

func init() {
	var err error
	glamourRender, err = glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	if err != nil {
		panic(fmt.Sprintf("init glamour renderer failed: %v", err))
	}
}

// Runner 负责驱动 Agent 的 ReAct 循环执行。
type Runner struct {
	llm        *llm.OpenAIClient
	history    *memory.HistoryStore
	summary    *memory.SummaryStore
	memStore   *memory.MemoryStore
	events     *memory.EventStore
	extractor  *memory.Extractor
	retriever  *memory.Retriever
	tools      *tool.Registry
	sessions   *session.SessionManager
	isTemporary       bool   // 临时会话：启动时创建，有对话后才落盘
	tempID            string // 临时会话 ID
	pendingSessionName string // /new 指定的会话名，ensurePersisted 时使用
}

// NewRunner 构造 Agent 执行器。
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

// Run 进入交互循环：读用户输入 -> ReAct 循环 -> 保存记忆。
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

		// 先规划 todo 列表，再逐步执行。
		answer, err := r.planAndExecute(ctx, round, input)
		if err != nil {
			return fmt.Errorf("react loop failed at round %d: %w", round, err)
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

// extractMemory 调用 LLM 提取摘要和结构化记忆。
func (r *Runner) extractMemory(ctx context.Context, round int, userInput, assistantOutput string) {
	result, err := r.extractor.Extract(ctx, userInput, assistantOutput)
	if err != nil {
		fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 记忆提取失败: %v", err)))
		return
	}

	// 保存 L2 摘要。
	if result.Summary != "" {
		if err := r.summary.Append(round, result.Summary); err != nil {
			fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 保存摘要失败: %v", err)))
		}
	}

	// 保存 L3 记忆。
	for _, action := range result.Memories {
		switch action.Action {
		case "create", "update":
			entry := memory.MemoryEntry{
				Name:        action.Name,
				Description: action.Description,
				Type:        action.Type,
				Importance:  action.Importance,
				Tags:        action.Tags,
				Content:     action.Content,
			}
			if err := r.memStore.SaveEntry(entry); err != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 保存记忆失败: %v", err)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("💾 记忆已保存: %s", action.Description)))
			}
		case "delete":
			if err := r.memStore.DeleteEntry(action.Name); err != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 删除记忆失败: %v", err)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("🗑️ 记忆已删除: %s", action.Name)))
			}
		}
	}
}

// ensurePersisted 将临时会话持久化到 manifest。
// 首次对话完成后调用：创建正式会话，将临时文件移动到正式目录。
func (r *Runner) ensurePersisted() error {
	// 记住临时目录路径。
	tempDir := r.sessions.SessionDir(r.tempID)

	// 创建正式会话（会自动设为 Active）。
	sessionName := r.pendingSessionName
	if sessionName == "" {
		sessionName = "新会话"
	}
	r.pendingSessionName = ""
	realID, err := r.sessions.Create(sessionName)
	if err != nil {
		return err
	}
	realDir := r.sessions.SessionDir(realID)

	// 将临时目录下的文件移动到正式目录。
	// events.jsonl
	tempEvents := tempDir + "/events.jsonl"
	realEvents := realDir + "/events.jsonl"
	if _, err := os.Stat(tempEvents); err == nil {
		os.MkdirAll(realDir, 0o755)
		if err := os.Rename(tempEvents, realEvents); err != nil {
			if !os.IsNotExist(err) {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 临时事件文件迁移失败: %v", err)))
			}
		}
	}
	// history.jsonl
	tempHistory := tempDir + "/history.jsonl"
	realHistory := realDir + "/history.jsonl"
	if _, err := os.Stat(tempHistory); err == nil {
		os.MkdirAll(realDir, 0o755)
		if err := os.Rename(tempHistory, realHistory); err != nil {
			if !os.IsNotExist(err) {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 临时历史文件迁移失败: %v", err)))
			}
		}
	}
	// summaries.jsonl
	tempSummary := tempDir + "/summaries.jsonl"
	realSummary := realDir + "/summaries.jsonl"
	if _, err := os.Stat(tempSummary); err == nil {
		os.MkdirAll(realDir, 0o755)
		os.Rename(tempSummary, realSummary)
	}
	// memory/ 目录
	tempMemory := tempDir + "/memory"
	realMemory := realDir + "/memory"
	if _, err := os.Stat(tempMemory); err == nil {
		os.MkdirAll(realDir, 0o755)
		os.Rename(tempMemory, realMemory)
	}

	// 清理临时目录。
	os.RemoveAll(tempDir)

	// 更新所有 store 的路径。
	r.history.SetPath(realDir)
	r.summary.SetPath(realDir)
	r.memStore.SetPath(realDir)
	r.events.SetPath(realDir)
	r.isTemporary = false
	r.tempID = ""
	fmt.Printf("\n%s\n", mutedStyle.Render("💾 会话已保存"))
	return nil
}

// cleanOrphanTempDirs 清理不在 manifest 中的孤立会话目录。
// 上次异常退出时临时目录可能未被清理。
func (r *Runner) cleanOrphanTempDirs() {
	entries, err := os.ReadDir(r.sessions.Dir())
	if err != nil {
		return
	}
	known := make(map[string]bool)
	for _, s := range r.sessions.List() {
		known[s.ID] = true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !known[e.Name()] {
			os.RemoveAll(filepath.Join(r.sessions.Dir(), e.Name()))
		}
	}
}

// switchSession 切换会话时重新初始化所有 store 路径。
func (r *Runner) switchSession() {
	activeDir := r.sessions.ActiveSessionDir()
	os.MkdirAll(activeDir, 0o755)
	r.history.SetPath(activeDir)
	r.summary.SetPath(activeDir)
	r.memStore.SetPath(activeDir)
	r.events.SetPath(activeDir)
}

// printSessionHistory 读取并展示指定会话的历史记录。
func (r *Runner) printSessionHistory() {
	events, err := r.events.ReadAll()
	if err != nil || len(events) == 0 {
		fmt.Printf("\n%s\n", mutedStyle.Render("  (无历史记录)"))
		return
	}

	// 按轮次分组展示 user 和 assistant 事件。
	fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("  📜 共 %d 条事件:", len(events))))
	currentRound := 0
	for _, e := range events {
		switch e.Type {
		case memory.EventUser:
			if e.Round != currentRound {
				currentRound = e.Round
				fmt.Printf("  %s\n", mutedStyle.Render(fmt.Sprintf("Round %d:", e.Round)))
			}
			userLine := e.Content
			if len([]rune(userLine)) > 60 {
				userLine = string([]rune(userLine)[:60]) + "..."
			}
			fmt.Printf("    %s\n", promptStyle.Render("You> ")+userLine)
		case memory.EventAssistant:
			assistantLine := e.Content
			if idx := strings.IndexByte(assistantLine, '\n'); idx >= 0 {
				assistantLine = assistantLine[:idx]
			}
			if len([]rune(assistantLine)) > 80 {
				assistantLine = string([]rune(assistantLine)[:80]) + "..."
			}
			fmt.Printf("    %s\n", answerLabelStyle.Render("Agent> ")+assistantLine)
		case memory.EventToolUse:
			for _, tc := range e.ToolCalls {
				fmt.Printf("    %s\n", mutedStyle.Render(fmt.Sprintf("🔧 %s(%s)", tc.Name, trimArgs(tc.Arguments))))
			}
		}
	}
}

// handleSessionCommand 处理 / 开头的会话管理命令。
// 返回值：新的轮次号（切换会话时重置为 1），是否已处理。
func (r *Runner) handleSessionCommand(input string) (int, bool) {
	// 记录系统命令事件。
	r.events.Append(memory.Event{
		Type:    memory.EventSystem,
		Command: input,
		Content: input,
	})

	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/new":
		name := ""
		if len(parts) > 1 {
			name = strings.Join(parts[1:], " ")
		}
		if r.isTemporary {
			// 临时模式下 /new：清理可能已创建的临时目录，重置临时状态。
			// 不立即创建正式会话，延迟到首次对话后 ensurePersisted。
			os.RemoveAll(r.sessions.SessionDir(r.tempID))
			newTempID, err := session.GenerateID()
			if err != nil {
				fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("生成会话 ID 失败: %v", err)))
				return 0, true
			}
			r.tempID = newTempID
			tempDir := r.sessions.SessionDir(newTempID)
			r.history.SetPath(tempDir)
			r.summary.SetPath(tempDir)
			r.memStore.SetPath(tempDir)
			r.events.SetPath(tempDir)
			// 记住用户指定的会话名，ensurePersisted 时使用。
			r.pendingSessionName = name
			displayName := "新会话"
			if name != "" {
				displayName = name
			}
			fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已切换到新会话: %s（对话后自动保存）", displayName)))
			return 1, true
		}
		id, err := r.sessions.Create(name)
		if err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("创建会话失败: %v", err)))
			return 0, true
		}
		r.switchSession()
		meta := r.sessions.FindMeta(id)
		displayName := id
		if meta != nil {
			displayName = meta.Name
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已创建并切换到新会话: %s", displayName)))
		return 1, true

	case "/list":
		selected, err := session.RunSessionPicker(r.sessions.List(), r.sessions.ActiveID())
		if err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("选择器错误: %v", err)))
			return 0, true
		}
		if selected == "" {
			// 用户取消
			return 0, true
		}
		// 用户选中了一个会话，执行切换
		if err := r.sessions.Switch(selected); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("切换失败: %v", err)))
			return 0, true
		}
		r.isTemporary = false // 切换到已持久化会话
		r.switchSession()
		meta := r.sessions.FindMeta(r.sessions.ActiveID())
		displayName := r.sessions.ActiveID()
		if meta != nil {
			displayName = meta.Name
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已切换到会话: %s", displayName)))
		r.printSessionHistory()
		return 1, true

	case "/switch":
		if len(parts) < 2 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /switch <会话ID>"))
			return 0, true
		}
		id := parts[1]
		if err := r.sessions.Switch(id); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("切换失败: %v", err)))
			return 0, true
		}
		r.isTemporary = false // 切换到已持久化会话
		r.switchSession()
		meta := r.sessions.FindMeta(r.sessions.ActiveID())
		displayName := r.sessions.ActiveID()
		if meta != nil {
			displayName = meta.Name
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已切换到会话: %s", displayName)))
		r.printSessionHistory()
		return 1, true

	case "/delete":
		if len(parts) < 2 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /delete <会话ID>"))
			return 0, true
		}
		id := parts[1]
		if err := r.sessions.Delete(id); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("删除失败: %v", err)))
			return 0, true
		}
		fmt.Printf("\n%s\n", successStyle.Render("✅ 会话已删除"))
		return 0, true

	case "/rename":
		if len(parts) < 2 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /rename <新名称>"))
			return 0, true
		}
		name := strings.Join(parts[1:], " ")
		activeID := r.sessions.ActiveID()
		if err := r.sessions.Rename(activeID, name); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("重命名失败: %v", err)))
			return 0, true
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 会话已重命名为: %s", name)))
		return 0, true

	case "/current":
		if r.isTemporary {
			fmt.Printf("\n%s\n", mutedStyle.Render("当前会话: (临时会话，对话后自动保存)"))
		} else {
			activeID := r.sessions.ActiveID()
			meta := r.sessions.FindMeta(activeID)
			if meta != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("当前会话: %s (%s)", meta.Name, meta.ID)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("当前会话: %s", activeID)))
			}
		}
		return 0, true

	case "/compress":
		r.handleCompress()
		return 0, true

	case "/memory":
		r.handleMemoryCommand(parts)
		return 0, true

	default:
		return 0, false
	}
}

// handleCompress 手动触发摘要压缩。
func (r *Runner) handleCompress() {
	fmt.Printf("\n%s\n", memoryBoxStyle.Render("🗜️ 正在压缩摘要..."))

	// 使用一个简单的上下文。
	ctx := context.Background()
	err := r.retriever.CompressSummaries(ctx, r.llm)
	if err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("压缩失败: %v", err)))
		return
	}

	count, _ := r.summary.Count()
	fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 压缩完成，当前 %d 条摘要", count)))
}

// handleMemoryCommand 处理 /memory 子命令。
func (r *Runner) handleMemoryCommand(parts []string) {
	if len(parts) < 2 {
		// 默认列出所有记忆。
		r.listMemories()
		return
	}

	sub := strings.ToLower(parts[1])
	switch sub {
	case "list":
		r.listMemories()
	case "add":
		if len(parts) < 3 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /memory add <内容>"))
			return
		}
		content := strings.Join(parts[2:], " ")
		r.addMemory(content)
	case "rm", "delete":
		if len(parts) < 3 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /memory rm <name>"))
			return
		}
		name := parts[2]
		r.deleteMemory(name)
	default:
		fmt.Printf("\n%s\n", errorStyle.Render("用法: /memory [list|add|rm]"))
	}
}

// listMemories 列出所有记忆。
func (r *Runner) listMemories() {
	entries, err := r.memStore.ListEntries()
	if err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("读取记忆失败: %v", err)))
		return
	}
	if len(entries) == 0 {
		fmt.Printf("\n%s\n", mutedStyle.Render("  (暂无记忆)"))
		return
	}

	fmt.Printf("\n%s\n", memoryBoxStyle.Render(fmt.Sprintf("🧠 共 %d 条记忆:", len(entries))))
	for _, e := range entries {
		importanceIcon := strings.Repeat("⭐", e.Importance)
		fmt.Printf("  %s %s\n", mutedStyle.Render(fmt.Sprintf("[%s]", e.Type)), e.Description)
		fmt.Printf("    %s name=%s\n", mutedStyle.Render(importanceIcon), e.Name)
	}
}

// addMemory 手动添加一条记忆。
func (r *Runner) addMemory(content string) {
	// 生成一个简单的 name。
	name := fmt.Sprintf("manual-%s", strings.ReplaceAll(strings.ToLower(content[:min(20, len(content))]), " ", "-"))
	name = strings.TrimRight(name, "-")

	entry := memory.MemoryEntry{
		Name:        name,
		Description: content,
		Type:        "user",
		Importance:  3,
		Tags:        []string{"manual"},
		Content:     content,
	}
	if err := r.memStore.SaveEntry(entry); err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("保存记忆失败: %v", err)))
		return
	}
	fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 记忆已保存: %s", content)))
}

// deleteMemory 删除一条记忆。
func (r *Runner) deleteMemory(name string) {
	if err := r.memStore.DeleteEntry(name); err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("删除记忆失败: %v", err)))
		return
	}
	fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 记忆已删除: %s", name)))
}

// planAndExecute 实现 Plan & Execute 流程：
// 1. 规划阶段：LLM 生成 todo 列表
// 2. 执行阶段：逐条执行 todo，每步可调用工具
// 3. 汇总阶段：LLM 根据所有执行结果给出最终答案
func (r *Runner) planAndExecute(ctx context.Context, round int, userInput string) (string, error) {
	// 构建上下文（从三层记忆中检索）。
	contextDigest, err := r.retriever.BuildContext(userInput)
	if err != nil {
		// 降级：从事件日志生成简易摘要。
		contextDigest = r.events.Digest(10)
	}

	// 自动压缩检查：估算上下文 token 用量，接近阈值时压缩 L2。
	if contextDigest != "" {
		totalTokens := memory.EstimateTokens(contextDigest + userInput + r.tools.Descriptions())
		limit := r.llm.ContextLimit()
		compressed, compErr := r.retriever.CheckAndCompress(ctx, r.llm, limit, totalTokens)
		if compressed {
			if compErr != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 自动压缩失败: %v", compErr)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render("🗜️ 上下文接近上限，已自动压缩摘要"))
				// 压缩后重新构建上下文。
				if newCtx, err := r.retriever.BuildContext(userInput); err == nil {
					contextDigest = newCtx
				}
			}
		}
	}

	// ── 规划阶段 ──
	todos, err := r.planPhase(ctx, round, contextDigest, userInput)
	if err != nil {
		return "", fmt.Errorf("plan phase failed: %w", err)
	}
	printPlan(todos)

	// 记录规划事件。
	todoEvents := make([]memory.TodoEvent, len(todos))
	for i, t := range todos {
		todoEvents[i] = memory.TodoEvent{ID: t.ID, Content: t.Content, Status: t.Status}
	}
	r.events.Append(memory.Event{
		Type:   memory.EventPlan,
		Round:  round,
		Todos:  todoEvents,
	})

	// ── 执行阶段 ──
	answer, err := r.execPhase(ctx, round, contextDigest, userInput, todos)
	if err != nil {
		return "", fmt.Errorf("exec phase failed: %w", err)
	}

	return answer, nil
}

// planPhase 调用 LLM 生成结构化的 todo 列表。
func (r *Runner) planPhase(ctx context.Context, round int, contextDigest, userInput string) ([]TodoItem, error) {
	systemPrompt := prompt.BuildPlanPrompt(r.tools.Descriptions())
	userPrompt := prompt.BuildPlanUserPrompt(round, contextDigest, userInput)

	printPlanStart()

	result, err := r.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("plan llm call failed: %w", err)
	}

	// 解析 JSON 数组。
	var todos []TodoItem
	if err := json.Unmarshal([]byte(result), &todos); err != nil {
		// 解析失败，降级为单步 todo。
		todos = []TodoItem{{ID: 1, Content: userInput, Status: "pending"}}
		return todos, nil
	}

	// 确保所有 todo 的 Status 初始化为 pending。
	for i := range todos {
		todos[i].Status = "pending"
	}
	return todos, nil
}

// execPhase 逐条执行 todo 列表，完成后汇总最终答案。
func (r *Runner) execPhase(ctx context.Context, round int, contextDigest, userInput string, todos []TodoItem) (string, error) {
	tools := r.tools.FunctionDefinitions()
	var doneSummaries []string

	for i := range todos {
		todos[i].Status = "current"
		printTodoList(todos)

		// 如果步骤是"直接完成任务"类的简单指令，跳过工具调用，直接让 LLM 回答。
		if isDirectTask(todos[i].Content) {
			todos[i].Status = "done"
			doneSummaries = append(doneSummaries, fmt.Sprintf("步骤 %d 完成: 直接回答", todos[i].ID))
			continue
		}

		// 构建执行 prompt。
		todosText := formatTodoList(todos)
		currentStep := fmt.Sprintf("步骤 %d：%s", todos[i].ID, todos[i].Content)
		doneSummary := "(无)"
		if len(doneSummaries) > 0 {
			doneSummary = strings.Join(doneSummaries, "\n")
		}
		execSystemPrompt := prompt.BuildExecPrompt(todosText, currentStep, doneSummary)

		messages := []llm.ChatMessage{
			{Role: "system", Content: execSystemPrompt},
			{Role: "user", Content: fmt.Sprintf("请执行步骤 %d：%s", todos[i].ID, todos[i].Content)},
		}

		// 调用 LLM（带工具）。
		resp, err := r.llm.ChatWithTools(ctx, messages, tools)
		if err != nil {
			todos[i].Status = "failed"
			doneSummaries = append(doneSummaries, fmt.Sprintf("步骤 %d 失败: %v", todos[i].ID, err))
			continue
		}

		// 如果 LLM 需要调用工具，进入 ReAct 子循环。
		if !resp.Finish {
			printExecToolStart(i + 1)
			resp, err = r.execToolLoop(ctx, resp, tools, messages)
			if err != nil {
				todos[i].Status = "failed"
				doneSummaries = append(doneSummaries, fmt.Sprintf("步骤 %d 工具执行失败: %v", todos[i].ID, err))
				continue
			}
		}

		// 步骤完成。
		todos[i].Status = "done"
		summary := resp.Content
		if len(summary) > 200 {
			summary = summary[:200] + "..."
		}
		doneSummaries = append(doneSummaries, fmt.Sprintf("步骤 %d 完成: %s", todos[i].ID, summary))
	}

	// ── 汇总阶段 ──
	printTodoList(todos)
	return r.summarizePhase(ctx, round, contextDigest, userInput, todos, doneSummaries)
}

// execToolLoop 在单个 todo 步骤内执行 ReAct 子循环（调用工具直到 LLM 给出文本回答）。
// initMessages 是进入子循环前的完整消息历史（system + user），确保 LLM 保留任务上下文。
func (r *Runner) execToolLoop(ctx context.Context, resp *llm.ChatResponse, tools []openai.Tool, initMessages []llm.ChatMessage) (*llm.ChatResponse, error) {
	messages := append([]llm.ChatMessage{}, initMessages...) // 复制初始消息，保留上下文
	seenToolCalls := make(map[string]bool)                    // 记录所有工具调用签名，检测重复

	for iter := 0; iter < maxIterations; iter++ {
		messages = append(messages, llm.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// 生成本次工具调用的签名（工具名+参数），用于重复检测。
		currentToolCall := toolCallSignature(resp.ToolCalls)
		isDuplicate := currentToolCall != "" && seenToolCalls[currentToolCall]
		if currentToolCall != "" {
			seenToolCalls[currentToolCall] = true
		}

		// 记录工具调用事件。
		toolCallEvents := make([]memory.ToolCallEvent, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			toolCallEvents[i] = memory.ToolCallEvent{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
		}
		r.events.Append(memory.Event{
			Type:      memory.EventToolUse,
			Round:     0, // 工具调用属于执行阶段，不标记轮次
			ToolCalls: toolCallEvents,
		})

		// 逐个执行工具调用。
		for i, tc := range resp.ToolCalls {
			printToolCall(iter+1, i+1, len(resp.ToolCalls), tc.Name, tc.Arguments)

			t := r.tools.Get(tc.Name)
			if t == nil {
				errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
				printToolResult(errMsg, true)
				// 记录工具错误结果。
				r.events.Append(memory.Event{
					Type:       memory.EventToolResult,
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					ToolResult: errMsg,
					IsError:    true,
				})
				messages = append(messages, llm.ChatMessage{
					Role:       "tool",
					Content:    errMsg,
					ToolCallID: tc.ID,
				})
				continue
			}

			result, execErr := t.Execute(tc.Arguments)
			if execErr != nil {
				printToolResult(fmt.Sprintf("%v", execErr), true)
				result = fmt.Sprintf("工具执行出错: %v\n请尝试其他方案，不要重复相同的命令。", execErr)
			} else {
				printToolResult(result, false)
			}

			// 记录工具执行结果。
			r.events.Append(memory.Event{
				Type:       memory.EventToolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolResult: result,
				IsError:    execErr != nil,
			})

			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// 重复调用检测：如果调用了相同的工具+参数，强制 LLM 总结。
		if isDuplicate {
			messages = append(messages, llm.ChatMessage{
				Role:    "user",
				Content: "你已经调用过相同的工具并获得了相同的结果。请根据已有信息直接给出最终回答，不要再调用任何工具。",
			})
		}

		printThink()
		resp, err := r.llm.ChatWithTools(ctx, messages, tools)
		if err != nil {
			return nil, fmt.Errorf("llm call failed: %w", err)
		}

		if resp.Finish {
			return resp, nil
		}

		printContinue()
	}

	// 达到最大迭代次数，让 LLM 做最终总结而不是直接报错。
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: "你已经尝试了多次工具调用。请根据已有信息直接给出回答，不要再调用工具。",
	})
	finalResp, err := r.llm.ChatWithTools(ctx, messages, tools)
	if err != nil {
		return nil, fmt.Errorf("tool loop reached max iterations (%d)", maxIterations)
	}
	return finalResp, nil
}

// trimArgs 截断工具参数用于展示。
func trimArgs(args string) string {
	if len(args) > 60 {
		return args[:60] + "..."
	}
	return args
}

// toolCallSignature 生成工具调用的签名，用于检测重复调用。
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

// summarizePhase 让 LLM 根据所有步骤的执行结果生成最终答案。
func (r *Runner) summarizePhase(ctx context.Context, round int, contextDigest, userInput string, todos []TodoItem, doneSummaries []string) (string, error) {
	todosText := formatTodoList(todos)
	summariesText := "(无)"
	if len(doneSummaries) > 0 {
		summariesText = strings.Join(doneSummaries, "\n")
	}

	systemPrompt := fmt.Sprintf(`你是一个任务执行的汇总者。用户的任务已经通过多步骤计划执行完毕。
请根据执行结果，给出简洁的最终回答。

## 执行计划
%s

## 各步骤执行结果
%s

## 要求
- 用中文回复
- 如果有步骤失败，说明失败原因和影响
- 给出完整的最终结果`, todosText, summariesText)

	userPrompt := fmt.Sprintf("轮次: %d\n用户任务: %s\n\n请根据以上执行结果给出最终回答。", round, userInput)

	result, err := r.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		// 汇总失败，降级为直接拼接结果。
		return fmt.Sprintf("任务执行完毕：\n\n%s\n\n%s", todosText, summariesText), nil
	}
	return result, nil
}

// isDirectTask 判断步骤是否是"直接完成任务"类的简单指令（不需要工具调用）。
func isDirectTask(content string) bool {
	c := strings.TrimSpace(strings.ToLower(content))
	directPatterns := []string{
		"直接完成任务",
		"直接回答",
		"直接回复",
		"直接输出",
	}
	for _, p := range directPatterns {
		if strings.Contains(c, p) {
			return true
		}
	}
	return false
}

// formatTodoList 将 todo 列表格式化为可读文本。
func formatTodoList(todos []TodoItem) string {
	var lines []string
	for _, t := range todos {
		var icon string
		switch t.Status {
		case "done":
			icon = "✅"
		case "failed":
			icon = "❌"
		case "current":
			icon = "👉"
		default:
			icon = "⬜"
		}
		lines = append(lines, fmt.Sprintf("%s %d. %s", icon, t.ID, t.Content))
	}
	return strings.Join(lines, "\n")
}

// reactLoop 保留作为降级方案，当规划阶段完全失败时使用。
func (r *Runner) reactLoop(ctx context.Context, round int, userInput string) (string, error) {
	systemPrompt := prompt.BuildReActSystemPrompt(r.tools.Descriptions())
	contextDigest, err := r.retriever.BuildContext(userInput)
	if err != nil {
		contextDigest = r.history.Digest(10)
	}
	userPrompt := prompt.BuildReActUserPrompt(round, contextDigest, userInput)

	messages := []llm.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	tools := r.tools.FunctionDefinitions()

	resp, err := r.llm.ChatWithTools(ctx, messages, tools)
	if err != nil {
		return "", fmt.Errorf("llm call failed: %w", err)
	}

	if resp.Finish {
		return resp.Content, nil
	}

	printReActStart()
	for iter := 0; iter < maxIterations; iter++ {
		messages = append(messages, llm.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// 记录工具调用事件。
		toolCallEvents := make([]memory.ToolCallEvent, len(resp.ToolCalls))
		for i, tc := range resp.ToolCalls {
			toolCallEvents[i] = memory.ToolCallEvent{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
		}
		r.events.Append(memory.Event{
			Type:      memory.EventToolUse,
			Round:     round,
			ToolCalls: toolCallEvents,
		})

		for i, tc := range resp.ToolCalls {
			printToolCall(iter+1, i+1, len(resp.ToolCalls), tc.Name, tc.Arguments)

			t := r.tools.Get(tc.Name)
			if t == nil {
				errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
				printToolResult(errMsg, true)
				r.events.Append(memory.Event{
					Type:       memory.EventToolResult,
					ToolCallID: tc.ID,
					ToolName:   tc.Name,
					ToolResult: errMsg,
					IsError:    true,
				})
				messages = append(messages, llm.ChatMessage{
					Role:       "tool",
					Content:    errMsg,
					ToolCallID: tc.ID,
				})
				continue
			}

			result, execErr := t.Execute(tc.Arguments)
			if execErr != nil {
				printToolResult(fmt.Sprintf("%v", execErr), true)
			} else {
				printToolResult(result, false)
			}

			if execErr != nil {
				result = fmt.Sprintf("工具执行出错: %v", execErr)
			}

			r.events.Append(memory.Event{
				Type:       memory.EventToolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				ToolResult: result,
				IsError:    execErr != nil,
			})

			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		printThink()
		resp, err = r.llm.ChatWithTools(ctx, messages, tools)
		if err != nil {
			return "", fmt.Errorf("llm call failed: %w", err)
		}

		if resp.Finish {
			printReActEnd(iter + 1)
			return resp.Content, nil
		}

		printContinue()
	}

	return "", fmt.Errorf("reached max iterations (%d) without final answer", maxIterations)
}

// ──────────────────────────────────────────────────────────
// 终端美化输出函数（Lip Gloss + Glamour）
// ──────────────────────────────────────────────────────────

func printBanner(sessions *session.SessionManager) {
	fmt.Println()
	content := lipgloss.JoinVertical(lipgloss.Center,
		"🤖  Agentic AI Assistant",
		"",
		mutedStyle.Render("输入任务开始对话，输入 exit 退出"),
		mutedStyle.Render("会话命令: /new /list /switch /delete /rename /current"),
		mutedStyle.Render("记忆命令: /compress /memory [list|add|rm]"),
	)
	fmt.Println(bannerStyle.Render(content))
	fmt.Println()
}

func printSessionHint() {
	fmt.Printf("%s\n", mutedStyle.Render("💡 当前为新会话，对话后自动保存"))
}

func printAnswer(round int, answer string) {
	label := answerLabelStyle.Render(fmt.Sprintf("[Round %d] Agent>", round))
	fmt.Printf("\n%s\n", label)

	// 用 Glamour 渲染 Markdown 内容。
	rendered, err := glamourRender.Render(answer)
	if err != nil {
		// 渲染失败时回退到纯文本。
		for _, line := range strings.Split(answer, "\n") {
			fmt.Printf("  %s\n", line)
		}
	} else {
		fmt.Print(rendered)
	}
	fmt.Println()
}

func printReActStart() {
	fmt.Println()
	content := "🔍 需要调用工具，进入推理循环..."
	fmt.Println(reActBoxStyle.Render(content))
}

// ──────────────────────────────────────────────────────────
// Plan & Execute UI 函数
// ──────────────────────────────────────────────────────────

func printPlanStart() {
	fmt.Println()
	content := "📋 正在规划任务步骤..."
	fmt.Println(planBoxStyle.Render(content))
}

func printPlan(todos []TodoItem) {
	fmt.Println()
	header := planBoxStyle.Render("📋 执行计划")
	fmt.Println(header)
	for _, t := range todos {
		printTodoItem(t, false)
	}
	fmt.Println()
}

func printTodoItem(t TodoItem, showStatus bool) {
	var icon string
	var style lipgloss.Style
	switch t.Status {
	case "done":
		icon = "✅"
		style = todoDoneStyle
	case "failed":
		icon = "❌"
		style = todoFailedStyle
	case "current":
		icon = "👉"
		style = todoCurrentStyle
	default:
		icon = "⬜"
		style = todoPendingStyle
	}
	fmt.Printf("  %s\n", style.Render(fmt.Sprintf("%s %d. %s", icon, t.ID, t.Content)))
}

func printTodoList(todos []TodoItem) {
	fmt.Println()
	header := todoCurrentStyle.Render("📋 当前进度")
	fmt.Println(header)
	for _, t := range todos {
		printTodoItem(t, true)
	}
}

func printExecToolStart(step int) {
	fmt.Println()
	content := fmt.Sprintf("🔧 步骤 %d 需要调用工具，进入执行循环...", step)
	fmt.Println(reActBoxStyle.Render(content))
}

func printReActEnd(steps int) {
	fmt.Println()
	content := successStyle.Render(fmt.Sprintf("✅ 推理完成，共 %d 步", steps))
	fmt.Println(reActDoneBoxStyle.Render(content))
}

func printThink() {
	fmt.Printf("\n  %s\n", thinkStyle.Render("💭 思考中..."))
}

func printContinue() {
	fmt.Printf("\n  %s\n", thinkStyle.Render("🔄 继续推理..."))
}

func printToolCall(step, index, total int, name, args string) {
	// 标题行
	header := toolTitleStyle.Render(fmt.Sprintf("🔧 %s", name))
	subtitle := mutedStyle.Render(fmt.Sprintf("步骤 %d · 工具调用 (%d/%d)", step, index, total))

	var body string
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		if formatted, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			body = string(formatted)
		}
	}
	if body == "" {
		body = args
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		subtitle,
		header,
		mutedStyle.Render(body),
	)
	fmt.Printf("\n%s\n", toolBoxStyle.Render(content))
}

func printToolResult(result string, isError bool) {
	lines := strings.Split(result, "\n")
	maxLines := 15
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("... (共 %d 行，已截断)", len(strings.Split(result, "\n")))))
	}

	var label string
	if isError {
		label = errorStyle.Render("❌ 错误:")
	} else {
		label = successStyle.Render("✅ 结果:")
	}

	body := strings.Join(lines, "\n")
	content := lipgloss.JoinVertical(lipgloss.Left, label, mutedStyle.Render(body))
	fmt.Printf("\n%s\n", toolBoxStyle.Render(content))
}
