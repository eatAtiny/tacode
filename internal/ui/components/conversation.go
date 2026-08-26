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
//   - 垂直滚动（↑/↓、PgUp/PgDn）
//   - Markdown 渲染最终回答（通过 Glamour）
//   - 自动滚到底部（新内容到达时，跟随模式开启时）
//   - 视口高度限制（高度即视口高度，由外部按布局分配）
//
// 流式输出机制：
//   - AddDelta 追加文本到当前最后一行（不换行）；若上一块已闭合则新起一行
//   - AddBlock 按行追加闭合内容块（最终回答/工具框线/思考行等）
//   - AddFinal 移除最后一行（流式文本），替换为渲染后的 Markdown
//
// 滚动跟随（follow）：
//   - 默认开启：任何 Add* 内容到达后自动滚到底部
//   - 用户上滚浏览历史时 SetFollow(false)：新内容追加但不再强制滚底
//   - 滚回底部或提交输入时 SetFollow(true)：恢复跟随并滚到底部
//
// 使用场景（阶段 2）：teaUI 组合布局中的对话区主组件（见 internal/ui/bubble/tea_model.go）。
type ConversationModel struct {
	lines     []string              // 所有行（包括已渲染的 Markdown 和分隔线）
	width     int                   // 组件宽度（列数）
	height    int                   // 组件高度（行数），即视口高度（由外部布局分配，不含状态栏/输入栏）
	scrollY   int                   // 当前滚动偏移（从顶部的行数）
	deltaOpen bool                  // 最后一行是否为未闭合的流式增量行（AddDelta 合并/断行依据）
	follow    bool                  // 是否跟随新内容自动滚到底部（用户上滚时关闭）
	glamour   *glamour.TermRenderer // Markdown 渲染器
}

// NewConversationModel 创建对话历史组件。初始化 Glamour 渲染器（自动样式，120 列换行）。
func NewConversationModel() *ConversationModel {
	g, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(120),
	)
	return &ConversationModel{
		glamour:   g,
		deltaOpen: false,
		follow:    true,
	}
}

// IsEmpty 返回对话区是否为空（无任何消息行）。
func (m *ConversationModel) IsEmpty() bool {
	return len(m.lines) == 0
}

// SetSize 设置组件的宽度和高度。高度即视口高度（不含状态栏/输入栏，
// 它们由外部组合布局分配，见 teaUI.View 的 convHeight 计算）。
//
// 尺寸变化后重算滚动偏移：追加时视口可能尚未设置（ViewportHeight 回退默认值），
// 此时 scrollToBottom 计算的 scrollY 可能超过新视口下的最大偏移，
// 若越界 clamp 到底部，保证滚动状态一致（IsAtBottom/ScrollDown 语义正确）。
func (m *ConversationModel) SetSize(width, height int) {
	m.width = width
	m.height = height
	if m.scrollY > m.maxScrollY() {
		m.scrollY = m.maxScrollY()
	}
}

// AddThink 添加思考指示器行（"⏳ Thinking..."，灰色斜体）。
func (m *ConversationModel) AddThink(iteration int) {
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render("⏳ Thinking..."))
}

// AddDelta 追加增量文本（流式输出）。
//
// 流式语义：
//   - 当前有未闭合的流式行（deltaOpen）→ 合并进最后一行（token 逐段累积）。
//   - 当前无未闭合流式行（空对话区或上一块已闭合）→ 新起一行。
//
// 追加后置 deltaOpen=true（标记最后一行仍处于流式未闭合状态），
// 后续 AddBlock 等块方法会闭合它，保证块与流式 token 不拼在同一行。
// 跟随模式（follow）下自动滚到底部。
func (m *ConversationModel) AddDelta(content string) {
	if m.deltaOpen && len(m.lines) > 0 {
		m.lines[len(m.lines)-1] += content
	} else {
		m.lines = append(m.lines, content)
	}
	m.deltaOpen = true
	m.scrollToBottom()
}

// AddBlock 添加闭合内容块（多行文本，如思考行/工具框线/最终回答/消息行）。
//
// 块内容按行拆分逐行追加（保留行内 ANSI 样式），块总从新行开始，
// 不会与之前的流式增量拼在同一行。追加后 deltaOpen=false（闭合流式状态），
// 之后的流式增量从新行开始。
// 跟随模式下自动滚到底部。
func (m *ConversationModel) AddBlock(content string) {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return
	}
	for _, line := range strings.Split(content, "\n") {
		m.addLine(line)
	}
}

// AddContinue 添加继续推理指示器行（"🔄 Continuing... (iteration N)"，灰色斜体）。
func (m *ConversationModel) AddContinue(iteration int) {
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Italic(true).
		Render(fmt.Sprintf("🔄 Continuing... (iteration %d)", iteration)))
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
	m.addLine(rendered)

	// 添加分隔线标记本轮结束。
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
}

// AddError 添加错误信息行（"❌ Error: ..."，红色加粗）。
func (m *ConversationModel) AddError(err error) {
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("9")).
		Bold(true).
		Render(fmt.Sprintf("❌ Error: %v", err)))
}

// AddToolSummary 添加工具调用摘要行（"🔧 N tool calls [press Enter to view]"）。
func (m *ConversationModel) AddToolSummary(count int) {
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("🔧 %d tool calls                        [press Enter to view]", count)))
}

// AddMessage 添加一般性消息行（无额外样式）。
func (m *ConversationModel) AddMessage(msg string) {
	m.addLine(msg)
}

// AddUserInput 添加用户输入行（"> " 前缀，青色加粗）+ 分隔线。
func (m *ConversationModel) AddUserInput(input string) {
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("14")).
		Bold(true).
		Render("> ") + input)
	m.addLine(lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", m.width)))
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

// IsAtBottom 是否已滚到最底部（scrollY 达最大偏移）。
// 内容不超过视口高度时恒为 true（无可滚动空间）。
func (m *ConversationModel) IsAtBottom() bool {
	return m.scrollY >= m.maxScrollY()
}

// SetFollow 设置是否跟随新内容自动滚到底部。默认开启。
//
// 滚动锁定语义：
//   - 用户上滚浏览历史时 SetFollow(false)：新内容追加但 scrollToBottom 不再强制滚底，
//     用户的阅读位置不被新内容打断。
//   - 滚回底部或提交新输入时 SetFollow(true)：立即滚到底部并恢复自动跟随。
func (m *ConversationModel) SetFollow(follow bool) {
	m.follow = follow
	if follow {
		m.scrollToBottom()
	}
}

// addLine 追加一行完整内容（块方法共用）并闭合流式状态。
// 行内容按原样入列（含 ANSI 样式），跟随模式下自动滚到底部。
func (m *ConversationModel) addLine(l string) {
	m.lines = append(m.lines, l)
	m.deltaOpen = false
	m.scrollToBottom()
}

// scrollToBottom 新内容到达时自动滚到底部（内部方法）。
// 跟随模式关闭（用户上滚浏览历史）时不强制滚底。
func (m *ConversationModel) scrollToBottom() {
	if !m.follow {
		return
	}
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

// ViewportHeight 返回可视区域能显示的行数。
// 高度即视口高度（由外部布局分配，见 SetSize）；未 SetSize（默认 0）时
// 回退 10 行，避免退化渲染。
func (m *ConversationModel) ViewportHeight() int {
	if m.height < 1 {
		return 10
	}
	return m.height
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
			Render("── scroll: ↑/↓ PgUp/PgDn ──")
	}

	return result
}
