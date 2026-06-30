package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"agentic/internal/session"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

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

// ──────────────────────────────────────────────────────────
// 终端美化输出函数（Lip Gloss + Glamour）
// ──────────────────────────────────────────────────────────

// printBanner 打印启动横幅，显示可用命令。
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

// printSessionHint 打印会话提示信息。
func printSessionHint() {
	fmt.Printf("%s\n", mutedStyle.Render("💡 当前为新会话，对话后自动保存"))
}

// printAnswer 打印 Agent 的回答，使用 Glamour 渲染 Markdown。
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

// printReActStart 打印推理循环开始提示。
func printReActStart() {
	fmt.Println()
	content := "🔍 需要调用工具，进入推理循环..."
	fmt.Println(reActBoxStyle.Render(content))
}

// printReActEnd 打印推理循环结束提示。
func printReActEnd(steps int) {
	fmt.Println()
	content := successStyle.Render(fmt.Sprintf("✅ 推理完成，共 %d 步", steps))
	fmt.Println(reActDoneBoxStyle.Render(content))
}

// printThink 打印思考中提示。
func printThink() {
	fmt.Printf("\n  %s\n", thinkStyle.Render("💭 思考中..."))
}

// printContinue 打印继续推理提示。
func printContinue() {
	fmt.Printf("\n  %s\n", thinkStyle.Render("🔄 继续推理..."))
}

// printDelta 打印增量文本（流式输出）。
// 直接输出到终端，实现真正的流式显示。
func printDelta(content string) {
	fmt.Print(content)
}

// printToolCall 打印工具调用信息，包含标题、参数。
func printToolCall(step, index, total int, name, args string) {
	// 标题行
	header := toolTitleStyle.Render(fmt.Sprintf("🔧 %s", name))
	subtitle := mutedStyle.Render(fmt.Sprintf("步骤 %d · 工具调用 (%d/%d)", step, index, total))

	// 格式化参数 JSON。
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

// printToolResult 打印工具执行结果，支持截断长文本。
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
