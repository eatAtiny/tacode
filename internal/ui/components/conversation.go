// Package components 提供可复用的 Bubble Tea UI 组件。
//
// 这些组件设计用于 Bubble Tea 全屏交互场景（如会话选择器、工具详情弹窗），
// 当前主循环未使用 Bubble Tea 渲染管线，组件主要作为预留和 /list 等交互场景使用。
//
// 组件列表：
//   - ConversationModel：可滚动的对话历史视图
//   - InputModel：文本输入栏
//   - StatusModel：状态栏（会话、模型、token）
//   - ToolViewModel：工具调用弹窗（含权限确认）
package components

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ConversationModel 对话历史组件。
//
// 维护一个行列表（lines），支持：
//   - 添加各类消息（用户输入、思考状态、流式增量、最终回答、错误等）
//   - 垂直滚动（j/k 或 ↑/↓）
//   - Markdown 渲染最终回答（通过 Glamour）
//   - 自动滚到底部（新内容到达时）
//   - 视口高度限制（height - 2，预留给状态栏和输入栏）
//
// 流式输出机制：
//   - AddDelta 追加文本到当前最后一行（不换行）
//   - AddFinal 移除最后一行（流式文本），替换为渲染后的 Markdown
//
// 注意：当前主循环使用 BubbleUI 的直接输出（fmt.Println），而非此组件。
// 此组件预留给未来切换到全 Bubble Tea 渲染管线的场景。
type ConversationModel struct {
	lines   []string            // 所有行（包括已渲染的 Markdown 和分隔线）
	width   int                 // 组件宽度（列数）
	height  int                 // 组件高度（行数），包含状态栏和输入栏
	scrollY int                 // 当前滚动偏移（从顶部的行数）
	glamour *glamour.TermRenderer // Markdown 渲染器
}

// NewConversationModel 创建对话历史组件。初始化 Glamour 渲染器（自动样式，120 列换行）。
func NewConversationModel() *ConversationModel {
	g, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	return &ConversationModel{
		glamour: g,
	}
}

// IsEmpty 返回对话区是否为空（无任何消息行）。
func (m *ConversationModel) IsEmpty() bool {
	return len(m.lines) == 0
}

// SetSize 设置组件的宽度和高度。视口高度 = height - 2。
func (m *ConversationModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

// AddThink 添加思考指示器行（"⏳ Thinking..."，灰色斜体）。
// 调用后自动滚到底部。
func (m *ConversationModel) AddThink(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render("⏳ Thinking..."))
	m.scrollToBottom()
}

// AddDelta 追加增量文本到当前行末尾（流式输出）。
// 如果当前无行则创建新行。调用后自动滚到底部。
// 注意：AddFinal 会移除这行并替换为渲染后的 Markdown。
func (m *ConversationModel) AddDelta(content string) {
	// 追加到最后一行，或新建一行。
	if len(m.lines) > 0 {
		m.lines[len(m.lines)-1] += content
	} else {
		m.lines = append(m.lines, content)
	}
	m.scrollToBottom()
}

// AddContinue 添加继续推理指示器行（"🔄 Continuing... (iteration N)"，灰色斜体）。
func (m *ConversationModel) AddContinue(iteration int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render(fmt.Sprintf("🔄 Continuing... (iteration %d)", iteration)))
	m.scrollToBottom()
}

// AddFinal 添加最终回答。
// 先移除最后一行（流式 delta 累积行），然后用 Glamour 渲染 answer
// 并添加分隔线。渲染失败时回退为纯文本。
func (m *ConversationModel) AddFinal(answer string) {
	// 清除之前的流式文本（最后一行是 delta 累积）。
	if len(m.lines) > 0 {
		m.lines = m.lines[:len(m.lines)-1]
	}

	// 用 Glamour 渲染 Markdown。
	rendered, err := m.glamour.Render(answer)
	if err != nil {
		rendered = answer
	}
	m.lines = append(m.lines, rendered)

	// 添加分隔线标记本轮结束。
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
	m.scrollToBottom()
}

// AddError 添加错误信息行（"❌ Error: ..."，红色加粗）。
func (m *ConversationModel) AddError(err error) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render(fmt.Sprintf("❌ Error: %v", err)))
	m.scrollToBottom()
}

// AddToolSummary 添加工具调用摘要行（"🔧 N tool calls [press Enter to view]"）。
func (m *ConversationModel) AddToolSummary(count int) {
	m.lines = append(m.lines, lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("🔧 %d tool calls                        [press Enter to view]", count)))
	m.scrollToBottom()
}

// AddMessage 添加一般性消息行（无额外样式）。
func (m *ConversationModel) AddMessage(msg string) {
	m.lines = append(m.lines, msg)
	m.scrollToBottom()
}

// AddUserInput 添加用户输入行（"> " 前缀，青色加粗）+ 分隔线。
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

// ScrollUp 向上滚动 n 行。不会滚过顶部（scrollY >= 0）。
func (m *ConversationModel) ScrollUp(n int) {
	m.scrollY -= n
	if m.scrollY < 0 {
		m.scrollY = 0
	}
}

// ScrollDown 向下滚动 n 行。不会滚过底部。
func (m *ConversationModel) ScrollDown(n int) {
	maxScroll := m.maxScrollY()
	m.scrollY += n
	if m.scrollY > maxScroll {
		m.scrollY = maxScroll
	}
}

// ScrollToTop 滚动到最顶部。
func (m *ConversationModel) ScrollToTop() {
	m.scrollY = 0
}

// ScrollToBottom 滚动到最底部。
func (m *ConversationModel) ScrollToBottom() {
	m.scrollY = m.maxScrollY()
}

// scrollToBottom 新内容到达时自动滚到底部（内部方法）。
func (m *ConversationModel) scrollToBottom() {
	m.scrollY = m.maxScrollY()
}

// maxScrollY 计算最大滚动偏移量。如果内容行数不超过视口高度，返回 0。
func (m *ConversationModel) maxScrollY() int {
	maxLines := m.ViewportHeight()
	totalLines := len(m.lines)
	if totalLines <= maxLines {
		return 0
	}
	return totalLines - maxLines
}

// ViewportHeight 返回可视区域能显示的行数。计算方式：height - 2（预留给状态栏和输入栏）。
// 最小保证 10 行。
func (m *ConversationModel) ViewportHeight() int {
	h := m.height - 2
	if h < 1 {
		h = 10
	}
	return h
}

// View 返回当前可视区域的渲染文本。
// 根据 scrollY 和 ViewportHeight 截取 lines，如果不在底部则显示滚动提示。
func (m *ConversationModel) View() string {
	maxLines := m.ViewportHeight()

	if len(m.lines) == 0 {
		return ""
	}

	// 计算可视范围。
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

	// 如果不在底部，显示滚动提示。
	result := strings.Join(displayLines, "\n")
	if m.scrollY < m.maxScrollY() {
		result += "\n" + lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Italic(true).
			Render("── scroll: j/k or ↑/↓ ──")
	}

	return result
}
