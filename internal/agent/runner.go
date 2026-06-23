package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
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
	llm         *llm.OpenAIClient
	memory      *memory.Store
	tools       *tool.Registry
	sessions    *session.SessionManager
	isTemporary bool   // 临时会话：启动时创建，有对话后才落盘
	tempID      string // 临时会话 ID
}

// NewRunner 构造 Agent 执行器。
func NewRunner(client *llm.OpenAIClient, store *memory.Store, tools *tool.Registry, sessions *session.SessionManager) *Runner {
	return &Runner{
		llm:      client,
		memory:   store,
		tools:    tools,
		sessions: sessions,
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

	// 启动时使用临时会话：生成临时 ID，记忆写入临时文件，不写 manifest。
	r.isTemporary = true
	tempID, err := session.GenerateID()
	if err != nil {
		return fmt.Errorf("generate temp session id failed: %w", err)
	}
	r.tempID = tempID
	r.memory.SetPath(r.sessions.TempPath(tempID))

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
			fmt.Printf("\n%s\n", errorStyle.Render("未知命令，可用: /new, /list, /switch, /delete, /rename, /current"))
			round-- // 不消耗轮次
			continue
		}

		// 先规划 todo 列表，再逐步执行。
		answer, err := r.planAndExecute(ctx, round, input)
		if err != nil {
			return fmt.Errorf("react loop failed at round %d: %w", round, err)
		}

		printAnswer(round, answer)

		// 落盘记忆。
		if err := r.memory.Append(round, input, answer); err != nil {
			return fmt.Errorf("save memory failed at round %d: %w", round, err)
		}

		// 首次对话后，将临时会话持久化到 manifest。
		if r.isTemporary {
			if err := r.ensurePersisted(); err != nil {
				fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("保存会话失败: %v", err)))
			}
		}
	}
}

// ensurePersisted 将临时会话持久化到 manifest。
// 首次对话完成后调用：创建正式会话，将临时文件重命名为正式文件。
func (r *Runner) ensurePersisted() error {
	// 记住临时文件路径，Create 会改变 Active。
	tempPath := r.sessions.TempPath(r.tempID)

	// 创建正式会话（会自动设为 Active）。
	realID, err := r.sessions.Create("新会话")
	if err != nil {
		return err
	}
	realPath := r.sessions.SessionPath(realID)

	// 将临时文件重命名为正式文件。
	if err := os.Rename(tempPath, realPath); err != nil {
		// 重命名失败（可能临时文件不存在），不影响会话创建。
		if !os.IsNotExist(err) {
			fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 临时文件迁移失败: %v", err)))
		}
	}

	r.memory.SetPath(realPath)
	r.isTemporary = false
	r.tempID = ""
	fmt.Printf("\n%s\n", mutedStyle.Render("💾 会话已保存"))
	return nil
}

// printSessionHistory 读取并展示指定会话的历史记录。
func (r *Runner) printSessionHistory() {
	records, err := r.memory.ReadHistory()
	if err != nil || len(records) == 0 {
		fmt.Printf("\n%s\n", mutedStyle.Render("  (无历史记录)"))
		return
	}

	fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("  📜 最近 %d 条记录:", len(records))))
	for _, rec := range records {
		userLine := rec.UserInput
		if len([]rune(userLine)) > 60 {
			userLine = string([]rune(userLine)[:60]) + "..."
		}
		assistantLine := rec.AssistantOutput
		// 去掉换行，取第一行
		if idx := strings.IndexByte(assistantLine, '\n'); idx >= 0 {
			assistantLine = assistantLine[:idx]
		}
		if len([]rune(assistantLine)) > 80 {
			assistantLine = string([]rune(assistantLine)[:80]) + "..."
		}
		fmt.Printf("  %s\n", mutedStyle.Render(fmt.Sprintf("Round %d:", rec.Round)))
		fmt.Printf("    %s\n", promptStyle.Render("You> ")+userLine)
		fmt.Printf("    %s\n", answerLabelStyle.Render("Agent> ")+assistantLine)
	}
}

// handleSessionCommand 处理 / 开头的会话管理命令。
// 返回值：新的轮次号（切换会话时重置为 1），是否已处理。
func (r *Runner) handleSessionCommand(input string) (int, bool) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/new":
		name := ""
		if len(parts) > 1 {
			name = strings.Join(parts[1:], " ")
		}
		// 如果当前是临时会话，先清理临时文件。
		if r.isTemporary {
			os.Remove(r.sessions.TempPath(r.tempID))
			r.isTemporary = false
			r.tempID = ""
		}
		id, err := r.sessions.Create(name)
		if err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("创建会话失败: %v", err)))
			return 0, true
		}
		r.memory.SetPath(r.sessions.ActivePath())
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
		r.memory.SetPath(r.sessions.ActivePath())
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
		r.memory.SetPath(r.sessions.ActivePath())
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

	default:
		return 0, false
	}
}

// planAndExecute 实现 Plan & Execute 流程：
// 1. 规划阶段：LLM 生成 todo 列表
// 2. 执行阶段：逐条执行 todo，每步可调用工具
// 3. 汇总阶段：LLM 根据所有执行结果给出最终答案
func (r *Runner) planAndExecute(ctx context.Context, round int, userInput string) (string, error) {
	digest := r.memory.Digest(20)

	// ── 规划阶段 ──
	todos, err := r.planPhase(ctx, round, digest, userInput)
	if err != nil {
		return "", fmt.Errorf("plan phase failed: %w", err)
	}
	printPlan(todos)

	// ── 执行阶段 ──
	answer, err := r.execPhase(ctx, round, digest, userInput, todos)
	if err != nil {
		return "", fmt.Errorf("exec phase failed: %w", err)
	}

	return answer, nil
}

// planPhase 调用 LLM 生成结构化的 todo 列表。
func (r *Runner) planPhase(ctx context.Context, round int, digest, userInput string) ([]TodoItem, error) {
	systemPrompt := prompt.BuildPlanPrompt(r.tools.Descriptions())
	userPrompt := prompt.BuildPlanUserPrompt(round, digest, userInput)

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
func (r *Runner) execPhase(ctx context.Context, round int, digest, userInput string, todos []TodoItem) (string, error) {
	tools := r.tools.FunctionDefinitions()
	var doneSummaries []string

	for i := range todos {
		todos[i].Status = "current"
		printTodoList(todos)

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
			resp, err = r.execToolLoop(ctx, resp, tools)
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
	return r.summarizePhase(ctx, round, digest, userInput, todos, doneSummaries)
}

// execToolLoop 在单个 todo 步骤内执行 ReAct 子循环（调用工具直到 LLM 给出文本回答）。
func (r *Runner) execToolLoop(ctx context.Context, resp *llm.ChatResponse, tools []openai.Tool) (*llm.ChatResponse, error) {
	messages := []llm.ChatMessage{} // 子循环独立的消息历史

	for iter := 0; iter < maxIterations; iter++ {
		messages = append(messages, llm.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// 逐个执行工具调用。
		for i, tc := range resp.ToolCalls {
			printToolCall(iter+1, i+1, len(resp.ToolCalls), tc.Name, tc.Arguments)

			t := r.tools.Get(tc.Name)
			if t == nil {
				errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
				printToolResult(errMsg, true)
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
				result = fmt.Sprintf("工具执行出错: %v", execErr)
			} else {
				printToolResult(result, false)
			}

			messages = append(messages, llm.ChatMessage{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
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

	return nil, fmt.Errorf("tool loop reached max iterations (%d)", maxIterations)
}

// summarizePhase 让 LLM 根据所有步骤的执行结果生成最终答案。
func (r *Runner) summarizePhase(ctx context.Context, round int, digest, userInput string, todos []TodoItem, doneSummaries []string) (string, error) {
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
	digest := r.memory.Digest(20)
	userPrompt := prompt.BuildReActUserPrompt(round, digest, userInput)

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

		for i, tc := range resp.ToolCalls {
			printToolCall(iter+1, i+1, len(resp.ToolCalls), tc.Name, tc.Arguments)

			t := r.tools.Get(tc.Name)
			if t == nil {
				errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
				printToolResult(errMsg, true)
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
