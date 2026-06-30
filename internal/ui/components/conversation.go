// internal/ui/components/conversation.go
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ConversationModel 对话历史组件。
type ConversationModel struct {
	lines   []string
	width   int
	height  int
	glamour *glamour.TermRenderer
}

// NewConversationModel 创建对话历史组件。
func NewConversationModel() *ConversationModel {
	g, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	return &ConversationModel{
		glamour: g,
	}
}

func (m *ConversationModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// AddThink 添加思考指示器。
func (m *ConversationModel) AddThink(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render("⏳ Thinking..."))
}

// AddDelta 添加增量文本（流式）。
func (m *ConversationModel) AddDelta(content string) {
	// 追加到最后一行，或新建一行
	if len(m.lines) > 0 {
		m.lines[len(m.lines)-1] += content
	} else {
		m.lines = append(m.lines, content)
	}
}

// AddContinue 添加继续推理指示。
func (m *ConversationModel) AddContinue(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render(fmt.Sprintf("🔄 Continuing... (iteration %d)", iteration)))
}

// AddFinal 添加最终回答（Markdown 渲染）。
func (m *ConversationModel) AddFinal(answer string) {
	// 清除之前的流式文本（最后一行是 delta 累积）
	if len(m.lines) > 0 {
		m.lines = m.lines[:len(m.lines)-1]
	}

	// 用 glamour 渲染
	rendered, err := m.glamour.Render(answer)
	if err != nil {
		rendered = answer
	}
	m.lines = append(m.lines, rendered)

	// 添加分隔线
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
}

// AddError 添加错误信息。
func (m *ConversationModel) AddError(err error) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render(fmt.Sprintf("❌ Error: %v", err)))
}

// AddToolSummary 添加工具调用摘要行。
func (m *ConversationModel) AddToolSummary(count int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("🔧 %d tool calls                        [press Enter to view]", count)))
}

// AddUserInput 添加用户输入。
func (m *ConversationModel) AddUserInput(input string) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("14")).
		Bold(true).
		Render("> ")+input)
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
}

func (m *ConversationModel) View() string {
	// 只显示最后 N 行，超出高度时截断
	maxLines := m.height - 2
	if maxLines < 1 {
		maxLines = 10
	}

	displayLines := m.lines
	if len(displayLines) > maxLines {
		displayLines = displayLines[len(displayLines)-maxLines:]
	}

	return strings.Join(displayLines, "\n")
}
