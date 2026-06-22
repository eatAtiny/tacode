package session

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────
// 交互式会话选择器（Bubble Tea）
// ──────────────────────────────────────────────────────────

var (
	// pickerActiveStyle 高亮当前光标所在的行。
	pickerActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("15")).
				Background(lipgloss.Color("57")).
				Bold(true).
				Padding(0, 1)

	// pickerInactiveStyle 普通行样式。
	pickerInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")).
				Padding(0, 1)

	// pickerCurrentStyle 标记当前活跃会话的行。
	pickerCurrentStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10")).
				Padding(0, 1)

	// pickerActiveCurrentStyle 同时是光标行又是活跃会话。
	pickerActiveCurrentStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("15")).
					Background(lipgloss.Color("57")).
					Bold(true).
					Padding(0, 1)

	// pickerHelpStyle 底部帮助提示。
	pickerHelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			MarginTop(1)

	// pickerTitleStyle 标题样式。
	pickerTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("99")).
				Bold(true).
				MarginBottom(1)

	// pickerMutedStyle 用于会话 ID 等次要信息。
	pickerMutedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// sessionPicker 是交互式会话选择器的 Bubble Tea Model。
type sessionPicker struct {
	sessions []SessionMeta
	cursor   int
	activeID string
	chosen   string // 用户选中的会话 ID；空串表示取消
}

// Init 启动时不执行任何命令。
func (m sessionPicker) Init() tea.Cmd {
	return nil
}

// Update 处理键盘事件。
func (m sessionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			m.chosen = ""
			return m, tea.Quit

		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}

		case "down", "j":
			if m.cursor < len(m.sessions)-1 {
				m.cursor++
			}

		case "enter":
			if len(m.sessions) > 0 {
				m.chosen = m.sessions[m.cursor].ID
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

// View 渲染选择器界面。
func (m sessionPicker) View() string {
	var b strings.Builder

	b.WriteString(pickerTitleStyle.Render("📋 会话列表"))
	b.WriteString("\n")

	for i, s := range m.sessions {
		// 构建行内容
		isActive := s.ID == m.activeID
		isCursor := i == m.cursor

		marker := "  "
		if isActive {
			marker = "▸ "
		}

		line := fmt.Sprintf("%s%s  %s", marker, s.Name, pickerMutedStyle.Render(s.ID))

		// 选中行高亮
		if isCursor {
			if isActive {
				b.WriteString(pickerActiveCurrentStyle.Render(line))
			} else {
				b.WriteString(pickerActiveStyle.Render(line))
			}
		} else if isActive {
			b.WriteString(pickerCurrentStyle.Render(line))
		} else {
			b.WriteString(pickerInactiveStyle.Render(line))
		}
		b.WriteString("\n")
	}

	b.WriteString(pickerHelpStyle.Render("↑↓ 移动 · Enter 切换 · Esc 取消"))

	return b.String()
}

// RunSessionPicker 启动交互式会话选择器，返回用户选中的会话 ID。
// 空串表示用户取消了选择。
func RunSessionPicker(sessions []SessionMeta, activeID string) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("没有可用的会话")
	}

	// 将光标初始位置设为当前活跃会话
	cursor := 0
	for i, s := range sessions {
		if s.ID == activeID {
			cursor = i
			break
		}
	}

	m := sessionPicker{
		sessions: sessions,
		cursor:   cursor,
		activeID: activeID,
	}

	p := tea.NewProgram(m)
	result, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("运行选择器失败: %w", err)
	}

	picker := result.(sessionPicker)
	return picker.chosen, nil
}
