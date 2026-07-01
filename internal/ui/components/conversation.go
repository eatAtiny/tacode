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
	lines      []string
	width      int
	height     int
	scrollY    int // 当前滚动偏移（从顶部的行数）
	glamour    *glamour.TermRenderer
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

// IsEmpty 返回对话区是否为空。
func (m *ConversationModel) IsEmpty() bool {
	return len(m.lines) == 0
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
	m.scrollToBottom()
}

// AddDelta 添加增量文本（流式）。
func (m *ConversationModel) AddDelta(content string) {
	// 追加到最后一行，或新建一行
	if len(m.lines) > 0 {
		m.lines[len(m.lines)-1] += content
	} else {
		m.lines = append(m.lines, content)
	}
	m.scrollToBottom()
}

// AddContinue 添加继续推理指示。
func (m *ConversationModel) AddContinue(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render(fmt.Sprintf("🔄 Continuing... (iteration %d)", iteration)))
	m.scrollToBottom()
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
	m.scrollToBottom()
}

// AddError 添加错误信息。
func (m *ConversationModel) AddError(err error) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render(fmt.Sprintf("❌ Error: %v", err)))
	m.scrollToBottom()
}

// AddToolSummary 添加工具调用摘要行。
func (m *ConversationModel) AddToolSummary(count int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("🔧 %d tool calls                        [press Enter to view]", count)))
	m.scrollToBottom()
}

// AddMessage 添加一般性消息。
func (m *ConversationModel) AddMessage(msg string) {
	m.lines = append(m.lines, msg)
	m.scrollToBottom()
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
	m.scrollToBottom()
}

// ScrollUp 向上滚动。
func (m *ConversationModel) ScrollUp(n int) {
	m.scrollY -= n
	if m.scrollY < 0 {
		m.scrollY = 0
	}
}

// ScrollDown 向下滚动。
func (m *ConversationModel) ScrollDown(n int) {
	maxScroll := m.maxScrollY()
	m.scrollY += n
	if m.scrollY > maxScroll {
		m.scrollY = maxScroll
	}
}

// ScrollToTop 滚动到顶部。
func (m *ConversationModel) ScrollToTop() {
	m.scrollY = 0
}

// ScrollToBottom 滚动到底部。
func (m *ConversationModel) ScrollToBottom() {
	m.scrollY = m.maxScrollY()
}

// scrollToBottom 新内容到达时自动滚到底部。
func (m *ConversationModel) scrollToBottom() {
	m.scrollY = m.maxScrollY()
}

// maxScrollY 计算最大滚动偏移。
func (m *ConversationModel) maxScrollY() int {
	maxLines := m.ViewportHeight()
	totalLines := len(m.lines)
	if totalLines <= maxLines {
		return 0
	}
	return totalLines - maxLines
}

// ViewportHeight 可视区域能显示的行数。
func (m *ConversationModel) ViewportHeight() int {
	h := m.height - 2 // 留给状态栏和输入栏
	if h < 1 {
		h = 10
	}
	return h
}

func (m *ConversationModel) View() string {
	maxLines := m.ViewportHeight()

	if len(m.lines) == 0 {
		return ""
	}

	// 计算可视范围
	start := m.scrollY
	end := start + maxLines
	if start >= len(m.lines) {
		start = len(m.lines) - 1
		if start < 0 {
			start = 0
		}
	}
	if end > len(m.lines) {
		end = len(m.lines)
	}

	displayLines := m.lines[start:end]

	// 如果不在底部，显示滚动提示
	result := strings.Join(displayLines, "\n")
	if m.scrollY < m.maxScrollY() {
		result += "\n" + lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Italic(true).
			Render("── scroll: j/k or ↑/↓ ──")
	}

	return result
}
