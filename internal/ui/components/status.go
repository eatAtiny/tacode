// internal/ui/components/status.go
package components

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StatusModel 状态栏组件。
//
// 显示在终端底部，包含：
//   - 项目名称（"agentic"）
//   - 当前会话名（截断到 20 字符）
//   - 模型名称
//   - 本轮 token 用量（输入 ↑ 和输出 ↓）
//
// 样式：浅灰文字 + 深灰背景，分隔符使用竖线。
//
// Token 显示格式：
//   - <1000: 直接显示数字
//   - >=1000: 显示为 "1.2k" 格式
//
// 注意：当前主循环不渲染此组件，预留给未来的全 Bubble Tea 模式。
type StatusModel struct {
	session      string // 会话显示名称
	model        string // LLM 模型名称
	inputTokens  int    // 本轮输入 token 数
	outputTokens int    // 本轮输出 token 数
	width        int    // 状态栏宽度（列数）
}

// NewStatusModel 创建状态栏组件。初始 session 为 "new"，model 为 "unknown"。
func NewStatusModel() StatusModel {
	return StatusModel{
		session: "new",
		model:   "unknown",
	}
}

// Init 返回 nil（状态栏无初始命令）。
func (m StatusModel) Init() tea.Cmd {
	return nil
}

// Update 状态栏不响应消息，原样返回。
func (m StatusModel) Update(msg tea.Msg) (StatusModel, tea.Cmd) {
	return m, nil
}

// View 渲染状态栏。
// 格式：agentic │ session: <name> │ <model> │ ↑<N> ↓<N>
// 宽度不足时右侧用空格填充。
func (m StatusModel) View() string {
	statusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("238")).
		Padding(0, 1)

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// 截断过长的 session 名称（超过 20 字符时截断并加 "..."）。
	session := m.session
	if len(session) > 20 {
		session = session[:17] + "..."
	}

	// 格式化 token 显示。
	tokenText := fmt.Sprintf("↑ %s ↓ %s",
		formatTokenCount(m.inputTokens),
		formatTokenCount(m.outputTokens),
	)

	left := fmt.Sprintf("agentic%s%s%s%s%s",
		sepStyle.Render(" │ "),
		"session: "+session,
		sepStyle.Render(" │ "),
		m.model,
		sepStyle.Render(" │ "),
	)

	bar := statusStyle.Render(left + tokenText)

	// 填充到目标宽度（右侧补空格）。
	barWidth := lipgloss.Width(bar)
	if barWidth < m.width {
		bar += statusStyle.Render(fmt.Sprintf("%*s", m.width-barWidth, ""))
	}

	return bar
}

// SetSession 设置会话显示名称。
func (m *StatusModel) SetSession(name string) {
	m.session = name
}

// SetModel 设置模型名称。
func (m *StatusModel) SetModel(model string) {
	m.model = model
}

// SetTokens 设置本轮 token 用量（输入和输出）。
func (m *StatusModel) SetTokens(input, output int) {
	m.inputTokens = input
	m.outputTokens = output
}

// SetWidth 设置状态栏宽度。
func (m *StatusModel) SetWidth(width int) {
	m.width = width
}

// formatTokenCount 格式化 token 数量。
// 小于 1000 直接显示数字，大于等于 1000 显示为 "1.2k" 格式。
func formatTokenCount(count int) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	return fmt.Sprintf("%.1fk", float64(count)/1000)
}
