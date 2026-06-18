package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/prompt"
	"agentic/internal/tool"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/chzyer/readline"
)

// ReAct 最大循环次数，防止无限循环。
const maxIterations = 10

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
	llm    *llm.OpenAIClient
	memory *memory.Store
	tools  *tool.Registry
}

// NewRunner 构造 Agent 执行器。
func NewRunner(client *llm.OpenAIClient, store *memory.Store, tools *tool.Registry) *Runner {
	return &Runner{
		llm:    client,
		memory: store,
		tools:  tools,
	}
}

// newReadline 创建一个 readline 实例，用于逐行读取输入。
func newReadline(prompt string) (*readline.Instance, error) {
	return readline.NewEx(&readline.Config{
		Prompt: prompt,
	})
}

// Run 进入交互循环：读用户输入 -> ReAct 循环 -> 保存记忆。
// 输入 exit 可退出。
func (r *Runner) Run(ctx context.Context) error {
	printBanner()
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

		// 先调 LLM 判断是否需要工具：不需要则直接回答，需要则进入 ReAct 循环。
		answer, err := r.reactLoop(ctx, round, input)
		if err != nil {
			return fmt.Errorf("react loop failed at round %d: %w", round, err)
		}

		printAnswer(round, answer)

		// 落盘记忆。
		if err := r.memory.Append(round, input, answer); err != nil {
			return fmt.Errorf("save memory failed at round %d: %w", round, err)
		}
	}
}

// reactLoop 先调一次 LLM 判断意图：
// - 如果 LLM 直接返回文本（不需要工具），直接返回
// - 如果 LLM 请求调用工具，进入 ReAct 循环
func (r *Runner) reactLoop(ctx context.Context, round int, userInput string) (string, error) {
	systemPrompt := prompt.BuildReActSystemPrompt(r.tools.Descriptions())
	digest := r.memory.Digest(20)
	userPrompt := prompt.BuildReActUserPrompt(round, digest, userInput)

	messages := []llm.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	tools := r.tools.FunctionDefinitions()

	// 第一次调用：判断是否需要工具。
	resp, err := r.llm.ChatWithTools(ctx, messages, tools)
	if err != nil {
		return "", fmt.Errorf("llm call failed: %w", err)
	}

	// LLM 直接回答，不需要工具。
	if resp.Finish {
		return resp.Content, nil
	}

	// 需要工具，进入 ReAct 循环。
	printReActStart()
	for iter := 0; iter < maxIterations; iter++ {
		// 将 assistant 的 tool_calls 消息加入历史。
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

		// 工具执行完毕，继续调用 LLM。
		printThink()
		resp, err = r.llm.ChatWithTools(ctx, messages, tools)
		if err != nil {
			return "", fmt.Errorf("llm call failed: %w", err)
		}

		// LLM 给出最终答案。
		if resp.Finish {
			printReActEnd(iter + 1)
			return resp.Content, nil
		}

		// 还需要继续调用工具。
		printContinue()
	}

	return "", fmt.Errorf("reached max iterations (%d) without final answer", maxIterations)
}

// ──────────────────────────────────────────────────────────
// 终端美化输出函数（Lip Gloss + Glamour）
// ──────────────────────────────────────────────────────────

func printBanner() {
	fmt.Println()
	content := lipgloss.JoinVertical(lipgloss.Center,
		"🤖  Agentic AI Assistant",
		"",
		mutedStyle.Render("输入任务开始对话，输入 exit 退出"),
	)
	fmt.Println(bannerStyle.Render(content))
	fmt.Println()
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
