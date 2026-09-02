// 欢迎界面渲染：Claude Code 风格的多行欢迎 banner。
// 包含 ASCII logo、欢迎语、版本/模型/目录信息、使用提示。
package bubble

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// asciiLogo TACODE 块状 ASCII art logo（figlet ansi_shadow）。
// 每行字符串宽度相近，左右留白对齐。
var asciiLogo = strings.Join([]string{
	`  ████████╗ █████╗  ██████╗ ██████╗ ██████╗ ███████╗`,
	`  ╚══██╔══╝██╔══██╗██╔════╝██╔═══██╗██╔══██╗██╔════╝`,
	`     ██║   ███████║██║     ██║   ██║██║  ██║█████╗`,
	`     ██║   ██╔══██║██║     ██║   ██║██║  ██║██╔══╝`,
	`     ██║   ██║  ██║╚██████╗╚██████╔╝██████╔╝███████╗`,
	`     ╚═╝   ╚═╝  ╚═╝ ╚═════╝ ╚═════╝ ╚═════╝ ╚══════╝`,
}, "\n")

// welcomeStyles 欢迎界面的样式。
var (
	// logoStyle logo 颜色（青紫色渐变效果用单一色）。
	logoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	// welcomeStyle 欢迎语：加粗青色。
	welcomeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
	// infoStyle 信息行：浅灰。
	infoStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	// hintStyle 使用提示：深灰。
	hintStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	// labelStyle 信息行标签：加粗。
	labelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
)

// infoLine 生成单条信息行（2 空格缩进 + emoji 标签 + 值，含行尾换行）。
func infoLine(emoji, value string) string {
	return infoStyle.Render("  "+labelStyle.Render(emoji)+" "+value) + "\n"
}

// welcomeBanner 生成欢迎界面多行文本。
// 结构：ASCII logo + 欢迎语 + 版本/模型/目录 + 使用提示。
// model 是 LLM 模型名，ver 是版本号，cwd 是当前工作目录。
func welcomeBanner(model, ver, cwd string) string {
	var sb strings.Builder

	// ASCII logo（青色）。
	sb.WriteString(logoStyle.Render(asciiLogo))
	sb.WriteString("\n\n")

	// 欢迎语。
	sb.WriteString(welcomeStyle.Render("  ✨ 欢迎使用 Tacode！"))
	sb.WriteString("\n\n")

	// 信息行：版本 / 模型 / 目录。
	if ver != "" {
		sb.WriteString(infoLine("📦", ver))
	}
	if model != "" {
		sb.WriteString(infoLine("🧠", model))
	}
	if cwd != "" {
		sb.WriteString(infoLine("📁", cwd))
	}

	// 使用提示。
	sb.WriteString("\n")
	sb.WriteString(hintStyle.Render("  输入消息开始对话 · /new 新建会话 · /balance 余额 · /list 切换 · exit 退出"))
	sb.WriteString("\n")
	sb.WriteString(hintStyle.Render("  " + strings.Repeat("─", 60)))

	return sb.String()
}
