// internal/ui/components/input.go
package components

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// InputModel 输入栏组件。
type InputModel struct {
	textInput textinput.Model
	width     int
	focused   bool
}

// NewInputModel 创建输入栏组件。
func NewInputModel() InputModel {
	ti := textinput.New()
	ti.Placeholder = "输入任务开始对话..."
	ti.Focus()
	return InputModel{
		textInput: ti,
	}
}

func (m InputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m InputModel) Update(msg tea.Msg) (InputModel, tea.Cmd) {
	var cmd tea.Cmd
	m.textInput, cmd = m.textInput.Update(msg)
	return m, cmd
}

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

func (m *InputModel) SetValue(value string) {
	m.textInput.SetValue(value)
}

func (m *InputModel) Value() string {
	return m.textInput.Value()
}

func (m *InputModel) Focus() {
	m.textInput.Focus()
}

func (m *InputModel) Blur() {
	m.textInput.Blur()
}

func (m *InputModel) SetWidth(width int) {
	m.width = width
	m.textInput.Width = width - 4 // 减去前缀宽度
}
