// internal/ui/components/status.go
package components

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// StatusModel 状态栏组件。
type StatusModel struct {
	session      string
	model        string
	inputTokens  int
	outputTokens int
	width        int
}

// NewStatusModel 创建状态栏组件。
func NewStatusModel() StatusModel {
	return StatusModel{
		session: "new",
		model:   "unknown",
	}
}

func (m StatusModel) Init() tea.Cmd {
	return nil
}

func (m StatusModel) Update(msg tea.Msg) (StatusModel, tea.Cmd) {
	return m, nil
}

func (m StatusModel) View() string {
	statusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Background(lipgloss.Color("238")).
		Padding(0, 1)

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// 截断 session 名称
	session := m.session
	if len(session) > 20 {
		session = session[:17] + "..."
	}

	// token 显示
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

	// 填充到宽度
	barWidth := lipgloss.Width(bar)
	if barWidth < m.width {
		bar += statusStyle.Render(fmt.Sprintf("%*s", m.width-barWidth, ""))
	}

	return bar
}

func (m *StatusModel) SetSession(name string) {
	m.session = name
}

func (m *StatusModel) SetModel(model string) {
	m.model = model
}

func (m *StatusModel) SetTokens(input, output int) {
	m.inputTokens = input
	m.outputTokens = output
}

func (m *StatusModel) SetWidth(width int) {
	m.width = width
}

// formatTokenCount 格式化 token 数量（1234 → 1.2k）。
func formatTokenCount(count int) string {
	if count < 1000 {
		return fmt.Sprintf("%d", count)
	}
	return fmt.Sprintf("%.1fk", float64(count)/1000)
}
