// internal/ui/components/toolview.go
package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// toolEntry 单个工具调用条目。
type toolEntry struct {
	name     string
	args     string
	result   string
	isError  bool
	executed bool // 是否已执行完成
}

// ToolViewModel 工具执行弹窗组件。
type ToolViewModel struct {
	tools    []toolEntry
	cursor   int
	open     bool
	width    int
	height   int

	// 确认状态
	confirming   bool
	confirmTool  string
	confirmArgs  string
	confirmYes   bool // true = Yes 选中
}

// NewToolViewModel 创建工具弹窗组件。
func NewToolViewModel() *ToolViewModel {
	return &ToolViewModel{}
}

func (m *ToolViewModel) Init() tea.Cmd {
	return nil
}

func (m *ToolViewModel) Update(msg tea.Msg) (*ToolViewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirming {
			switch msg.String() {
			case "left", "right", "tab":
				m.ConfirmToggle()
			case "enter":
				m.ConfirmSelection()
			case "q", "esc":
				m.Close()
			}
		} else {
			switch msg.String() {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor < len(m.tools)-1 {
					m.cursor++
				}
			case "enter":
				// 展开详情（TODO: 弹出详情子弹窗）
			case "q", "esc":
				m.Close()
			}
		}
	}
	return m, nil
}

func (m *ToolViewModel) View() string {
	if !m.open {
		return ""
	}

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(0, 1)

	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	successStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	var lines []string
	lines = append(lines, titleStyle.Render("┌─ Tool Calls ─────────────────────────────────────────┐"))

	for i, tool := range m.tools {
		prefix := "  "
		if i == m.cursor {
			prefix = selectedStyle.Render("> ")
		}

		status := "⏳"
		statusStyle := mutedStyle
		if tool.executed {
			if tool.isError {
				status = "✗"
				statusStyle = errorStyle
			} else {
				status = "✓"
				statusStyle = successStyle
			}
		}

		// 截断 args
		args := tool.args
		if len([]rune(args)) > 30 {
			args = string([]rune(args)[:27]) + "..."
		}

		// 截断 result
		result := tool.result
		if len([]rune(result)) > 20 {
			result = string([]rune(result)[:17]) + "..."
		}

		line := fmt.Sprintf("%s🔧 %-20s %-30s %s %s",
			prefix,
			tool.name,
			args,
			statusStyle.Render(status),
			mutedStyle.Render(result),
		)
		lines = append(lines, line)
	}

	lines = append(lines, titleStyle.Render("└──────────────────────────────────────────────────────┘"))

	// 确认弹窗覆盖
	if m.confirming {
		confirmLines := m.renderConfirm()
		lines = append(lines, confirmLines...)
	}

	return borderStyle.Render(strings.Join(lines, "\n"))
}

func (m *ToolViewModel) renderConfirm() []string {
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	var lines []string
	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("┌─ Permission Required ────────────────────────────────┐"))
	lines = append(lines, fmt.Sprintf("  🔧 %s: %s", m.confirmTool, m.confirmArgs))
	lines = append(lines, mutedStyle.Render("  ⚠ 此操作需要确认"))

	yesStyle := mutedStyle
	noStyle := mutedStyle
	if m.confirmYes {
		yesStyle = selectedStyle
	} else {
		noStyle = selectedStyle
	}
	lines = append(lines, fmt.Sprintf("  %s    %s",
		yesStyle.Render("Yes, execute"),
		noStyle.Render("No, deny"),
	))
	lines = append(lines, titleStyle.Render("└──────────────────────────────────────────────────────┘"))

	return lines
}

// AddTool 添加工具调用。
func (m *ToolViewModel) AddTool(name, args string) {
	m.tools = append(m.tools, toolEntry{name: name, args: args})
}

// SetResult 设置工具执行结果。
func (m *ToolViewModel) SetResult(name, result string, isError bool) {
	for i := range m.tools {
		if m.tools[i].name == name && !m.tools[i].executed {
			m.tools[i].result = result
			m.tools[i].isError = isError
			m.tools[i].executed = true
			break
		}
	}
}

// Open 打开弹窗。
func (m *ToolViewModel) Open() {
	m.open = true
	m.cursor = 0
}

// Close 关闭弹窗。
func (m *ToolViewModel) Close() {
	m.open = false
	m.confirming = false
	m.tools = nil
}

// IsOpen 返回弹窗是否打开。
func (m *ToolViewModel) IsOpen() bool {
	return m.open
}

// SetConfirming 设置确认状态。
func (m *ToolViewModel) SetConfirming(tool, args string) {
	m.confirming = true
	m.confirmTool = tool
	m.confirmArgs = args
	m.confirmYes = true // 默认选中 Yes
}

// ConfirmToggle 切换 Yes/No 选择。
func (m *ToolViewModel) ConfirmToggle() {
	m.confirmYes = !m.confirmYes
}

// ConfirmSelection 返回当前选择并关闭确认。
func (m *ToolViewModel) ConfirmSelection() bool {
	m.confirming = false
	return m.confirmYes
}

func (m *ToolViewModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}
