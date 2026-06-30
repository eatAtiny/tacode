package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────
// TUI 样式定义
// ──────────────────────────────────────────────────────────

var (
	// Status bar
	StatusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Background(lipgloss.Color("238")).
			Padding(0, 1)

	StatusSeparatorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	// User input prefix
	UserPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	// Thinking indicator
	ThinkStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Italic(true)

	// Tool call
	ToolPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11")).
			Bold(true)

	ToolSummaryStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("245"))

	ToolSuccessStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10"))

	ToolErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	// Separator line
	SeparatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	// Error message
	ErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9")).
			Bold(true)

	// Success message
	SuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10"))

	// Muted/secondary text
	MutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245"))

	// Prompt style
	PromptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")).
			Bold(true)

	// Answer label
	AnswerLabelStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10")).
				Bold(true)
)

// Separator 返回一条横线。
func Separator(width int) string {
	if width <= 0 {
		width = 60
	}
	return SeparatorStyle.Render(strings.Repeat("─", width))
}
