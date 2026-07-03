// internal/ui/components/toolview.go
package components

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// toolEntry 记录单个工具调用的完整生命周期。
type toolEntry struct {
	name     string // 工具名称
	args     string // JSON 格式的调用参数
	result   string // 执行结果（执行完成后填充）
	isError  bool   // 执行是否出错
	executed bool   // 是否已执行完成（完成前显示 ⏳，完成后显示 ✓/✗）
}

// ToolViewModel 工具执行弹窗组件。
//
// 功能：
//   - 展示工具调用列表（名称、参数预览、执行状态）
//   - 光标导航（↑↓ 或 j/k 选择条目，Enter 展开详情）
//   - 权限确认弹窗（Yes/No 选择，←→ 切换，Enter 确认）
//   - 打开/关闭状态管理
//
// 弹窗状态机：
//
//	关闭 ──Open()──→ 打开（浏览模式）
//	打开 ──Close()─→ 关闭（清空工具列表）
//	打开 ──SetConfirming()─→ 确认模式
//	确认模式 ──ConfirmSelection()─→ 打开（浏览模式）
//	确认模式 ──Close()─→ 关闭
//
// 注意：当前主循环直接通过 BubbleUI.OnToolCall 绘制框线输出，不使用此组件。
// 此组件预留给未来切换到全 Bubble Tea 交互模式时使用。
type ToolViewModel struct {
	tools  []toolEntry // 工具调用列表
	cursor int         // 当前选中索引
	open   bool        // 弹窗是否打开
	width  int         // 弹窗宽度
	height int         // 弹窗高度

	// 确认状态
	confirming  bool   // 是否在权限确认模式
	confirmTool string // 正在确认的工具名称
	confirmArgs string // 正在确认的工具参数
	confirmYes  bool   // true = Yes 选中, false = No 选中
}

// NewToolViewModel 创建关闭状态的工具弹窗组件。
func NewToolViewModel() *ToolViewModel {
	return &ToolViewModel{}
}

// Init 返回 nil（无初始命令）。
func (m *ToolViewModel) Init() tea.Cmd {
	return nil
}

// Update 处理键盘事件。
//
// 浏览模式：
//   - ↑/k：上移光标
//   - ↓/j：下移光标
//   - Enter：展开详情（TODO）
//   - q/Esc：关闭弹窗
//
// 确认模式：
//   - ←/→/Tab：切换 Yes/No 选择
//   - Enter：确认选择
//   - q/Esc：关闭弹窗（视为拒绝）
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
				// 展开详情（TODO: 弹出详情子弹窗）。
			case "q", "esc":
				m.Close()
			}
		}
	}
	return m, nil
}

// View 渲染弹窗。
//
// 布局：
//
//	┌─ Tool Calls ─────────────────────────────────────────┐
//	> 🔧 shell              {"command":"ls"}       ✓ 成功输出
//	  🔧 file               {"action":"read"...}   ⏳ ...
//	└──────────────────────────────────────────────────────┘
//
// 确认模式下追加权限确认面板。
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

		// 状态图标：⏳ 等待中，✓ 成功，✗ 失败。
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

		// 截断过长的参数文本（>30 runes）。
		args := tool.args
		if len([]rune(args)) > 30 {
			args = string([]rune(args)[:27]) + "..."
		}

		// 截断过长的结果文本（>20 runes）。
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

	// 确认弹窗覆盖在工具列表下方。
	if m.confirming {
		confirmLines := m.renderConfirm()
		lines = append(lines, confirmLines...)
	}

	return borderStyle.Render(strings.Join(lines, "\n"))
}

// renderConfirm 渲染权限确认面板。
//
//	┌─ Permission Required ────────────────────────────────┐
//	  🔧 shell: {"command": "rm -rf /"}
//	  ⚠ 此操作需要确认
//	  > Yes, execute      No, deny
//	└──────────────────────────────────────────────────────┘
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

// AddTool 添加一个工具调用条目（状态为等待执行）。
func (m *ToolViewModel) AddTool(name, args string) {
	m.tools = append(m.tools, toolEntry{name: name, args: args})
}

// SetResult 设置工具执行结果。按名称匹配第一个未完成的条目。
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

// Open 打开弹窗，重置光标到第一个条目。
func (m *ToolViewModel) Open() {
	m.open = true
	m.cursor = 0
}

// Close 关闭弹窗，清空工具列表和确认状态。
func (m *ToolViewModel) Close() {
	m.open = false
	m.confirming = false
	m.tools = nil
}

// IsOpen 返回弹窗是否打开。
func (m *ToolViewModel) IsOpen() bool {
	return m.open
}

// SetConfirming 进入权限确认模式。默认选中 Yes。
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

// ConfirmSelection 确认当前选择，退出确认模式，返回用户选择。
// true 表示允许执行，false 表示拒绝。
func (m *ToolViewModel) ConfirmSelection() bool {
	m.confirming = false
	return m.confirmYes
}

// SetSize 设置弹窗尺寸。
func (m *ToolViewModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}
