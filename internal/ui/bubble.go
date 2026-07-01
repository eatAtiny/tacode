package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"agentic/internal/ui/components"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────
// BubbleUI — 混合模式 UI
//
// 主循环：bufio.Scanner 读输入 + fmt.Println 输出（Claude Code 风格）
// 会话选择器：Bubble Tea 交互式组件
// ──────────────────────────────────────────────────────────

type BubbleUI struct {
	scanner *bufio.Scanner

	// 子组件（用于会话选择器等 Bubble Tea 交互场景）
	conversation *components.ConversationModel
	input        components.InputModel
	status       components.StatusModel
	toolView     *components.ToolViewModel

	// 元数据
	sessionName string
	model       string

	// token 统计（本轮）
	inputTokens  int
	outputTokens int

	// 流式状态
	hasDelta bool // 本轮是否收到过 delta
}

// NewBubbleUI 创建 BubbleUI 实例。
func NewBubbleUI(_ ...tea.ProgramOption) *BubbleUI {
	return &BubbleUI{
		scanner:      bufio.NewScanner(os.Stdin),
		conversation: components.NewConversationModel(),
		input:        components.NewInputModel(),
		status:       components.NewStatusModel(),
		toolView:     components.NewToolViewModel(),
	}
}

// Close 关闭 UI，释放资源。
func (b *BubbleUI) Close() error {
	return nil
}

// ──────────────────────────────────────────────────────────
// 样式定义
// ──────────────────────────────────────────────────────────

var (
	styleUserPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleThink      = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	styleError      = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleMuted      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleSuccess    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSeparator  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleToolPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
)

// ──────────────────────────────────────────────────────────
// UI 接口实现
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) ReadInput() (string, error) {
	fmt.Print(styleUserPrefix.Render("> "))
	if !b.scanner.Scan() {
		return "", fmt.Errorf("EOF")
	}
	input := strings.TrimSpace(b.scanner.Text())
	return input, nil
}

func (b *BubbleUI) OnThink(_ int) {
	fmt.Println(styleThink.Render("⏳ Thinking..."))
}

func (b *BubbleUI) OnDelta(content string) {
	b.hasDelta = true
	fmt.Print(content)
}

func (b *BubbleUI) OnToolCall(name, args string) {
	fmt.Printf("\n%s %s\n", styleToolPrefix.Render("🔧 "+name), styleMuted.Render(trimArgs(args, 80)))
}

func (b *BubbleUI) OnToolResult(name, result string, isError bool) {
	lines := strings.Split(result, "\n")
	if len(lines) > 15 {
		lines = lines[:15]
		lines = append(lines, styleMuted.Render(fmt.Sprintf("... (共 %d 行，已截断)", len(strings.Split(result, "\n")))))
	}
	if isError {
		fmt.Printf("  %s\n", styleError.Render("❌ 错误:"))
	} else {
		fmt.Printf("  %s\n", styleSuccess.Render("✅ 结果:"))
	}
	for _, line := range lines {
		fmt.Printf("  %s\n", line)
	}
}

func (b *BubbleUI) OnContinue(iteration int) {
	b.hasDelta = false
	fmt.Println(styleThink.Render(fmt.Sprintf("🔄 Continuing... (iteration %d)", iteration)))
}

func (b *BubbleUI) OnFinal(answer string) {
	if !b.hasDelta {
		fmt.Println(answer)
	}
	b.hasDelta = false
	fmt.Println()
	fmt.Println(styleSeparator.Render(strings.Repeat("─", 60)))
}

func (b *BubbleUI) OnError(err error) {
	fmt.Println(styleError.Render(fmt.Sprintf("❌ Error: %v", err)))
}

func (b *BubbleUI) OnMessage(msg string) {
	fmt.Println(msg)
}

func (b *BubbleUI) ConfirmPermission(tool, args string) (bool, error) {
	fmt.Printf("\n%s %s\n", styleThink.Render("⚠️  权限确认:"), tool)
	fmt.Printf("  参数: %s\n", styleMuted.Render(args))
	fmt.Print(styleUserPrefix.Render("  允许执行? [y/N] "))
	if !b.scanner.Scan() {
		return false, fmt.Errorf("EOF")
	}
	answer := strings.ToLower(strings.TrimSpace(b.scanner.Text()))
	return answer == "y" || answer == "yes", nil
}

// ──────────────────────────────────────────────────────────
// 元数据设置
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) SetSessionName(name string) {
	b.sessionName = name
}

func (b *BubbleUI) SetModel(model string) {
	b.model = model
}

func (b *BubbleUI) UpdateTokens(input, output int) {
	b.inputTokens += input
	b.outputTokens += output
}

func (b *BubbleUI) ResetTokens() {
	b.inputTokens = 0
	b.outputTokens = 0
}

// 辅助函数
func trimArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return args
}
