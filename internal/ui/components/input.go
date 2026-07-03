// internal/ui/components/input.go
package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// InputModel 输入栏组件。
//
// 封装 Bubble Tea 的 textinput.Model，提供：
//   - 带样式的输入提示符（青色 "> " 前缀）
//   - 命令行提示文本（灰底显示可用命令）
//   - 分隔线
//
// 布局（从上到下）：
//   ─────────────────────────── 分隔线
//   > [用户输入区域]
//   /new /list /switch ...      命令提示
//
// 注意：当前主循环使用 bufio.Scanner 直接读输入，此组件预留给
// 未来切换到全 Bubble Tea 渲染管线时使用。
type InputModel struct {
	textInput textinput.Model // Bubble Tea 文本输入组件
	width     int             // 组件宽度（列数）
	focused   bool            // 是否聚焦
}

// NewInputModel 创建输入栏组件。默认 placeholder 为"输入任务开始对话..."，自动聚焦。
func NewInputModel() InputModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务开始对话..."
	ti.Focus()
	return InputModel{
		textInput: ti,
	}
}

// Init 返回初始命令（启动光标闪烁）。
func (m InputModel) Init() tea.Cmd {
	return textinput.Blink
}

// Update 处理 Bubble Tea 消息，更新内部 textinput 状态。
func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

// View 渲染输入栏：
//
//	───────────────────────────
//	> [textinput]
//	/new /list /switch ...
func (m InputModel) View() string {
	prefix := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true).Render("> ")
	input := m.textInput.View()

	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(
		"  /new /list /switch /delete /rename /memory /compress",
	)

	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", m.width)),
		prefix+input,
		hint,
	)
}

// SetValue 设置输入框的文本值。
func (m *InputModel) SetValue(value string) {
	m.textInput.SetValue(value)
}

// Value 返回输入框当前的文本值。
func (m *InputModel) Value() string {
	return m.textInput.Value()
}

// Focus 聚焦输入框（显示光标，接收输入）。
func (m *InputModel) Focus() {
	m.textInput.Focus()
}

// Blur 取消聚焦输入框。
func (m *InputModel) Blur() {
	m.textInput.Blur()
}

// SetWidth 设置组件宽度并调整内部 textinput 宽度（减去前缀宽度 4）。
func (m *InputModel) SetWidth(width int) {
	m.width = width
	m.textInput.Width = width - 4 // 减去 "> " 前缀宽度
}
