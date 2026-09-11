// 框线渲染辅助：chat.go（ChatModel）的工具消息渲染共用文本生成函数。
// 框线统一在此生成，chat.go 不再内联绘制（消除双份逻辑）。
package bubble

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// boxWidth 计算框线宽度：取内容最长行的字符宽度，限制在 [minWidth, 80] 范围。
// toolCallBox/toolResultBox 共用。
func boxWidth(content string, minWidth int) int {
	maxLen := minWidth
	for _, line := range strings.Split(content, "\n") {
		w := len([]rune(line))
		if w > maxLen {
			maxLen = w
		}
	}
	if maxLen > 80 {
		maxLen = 80
	}
	return maxLen
}

// drawBox 绘制工具框线骨架：标题行（┌─ title + 补位虚线）、正文行（│ 前缀）、
// 底边（└ + 虚线），每行 2 空格缩进、行尾带 \n（结尾带 \n 契约由调用方依赖）。
// titleStyle 用于角标/竖线/底边；bodyLines 为已着色的正文行（原样写入）；
// headerDashes 是标题行补位虚线数——调用方按各自字节口径计算
// （宽度口径统一 len→lipgloss.Width 是后续 fix 任务，此处保持逐字节不变）。
func drawBox(titleStyle lipgloss.Style, title string, headerDashes int, bodyLines []string, width int) string {
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(titleStyle.Render("┌─ " + title))
	sb.WriteString(" ")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", max(0, headerDashes))))
	sb.WriteString("\n")
	for _, line := range bodyLines {
		sb.WriteString("  ")
		sb.WriteString(titleStyle.Render("│"))
		sb.WriteString(" ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	sb.WriteString("  ")
	sb.WriteString(titleStyle.Render("└" + strings.Repeat("─", width+1)))
	sb.WriteString("\n")
	return sb.String()
}

// toolCallBox 生成工具调用框线文本（chat.go chatToolCallMsg 使用）。
// 参数 JSON 格式化缩进显示，单行参数用紧凑格式。
//
// 结尾带 \n：框线文本作为 chatLine 渲染，尾换行保证框线闭合后另起一行。
func toolCallBox(name, args string) string {
	displayArgs := args
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		if formatted, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			displayArgs = string(formatted)
		}
	}
	width := boxWidth(displayArgs, 60)
	var body []string
	for _, line := range strings.Split(displayArgs, "\n") {
		body = append(body, styleMuted.Render(line))
	}
	return drawBox(styleToolPrefix, "🔧 "+name, width-lipgloss.Width(name)-6, body, width)
}

// toolResultBox 生成工具执行结果框线文本（chat.go chatToolResultMsg 使用）。
// 超过 15 行的输出会被截断。成功标题绿色 "✅ 结果"，失败红色 "❌ 错误"。
// 结尾带 \n：语义同 toolCallBox。
func toolResultBox(name, result string, isError bool) string {
	lines := strings.Split(result, "\n")
	totalLines := len(lines)
	if totalLines > 15 {
		lines = lines[:15]
	}
	titleStyle := styleSuccess
	titleText := "✅ 结果"
	if isError {
		titleStyle = styleError
		titleText = "❌ 错误"
	}
	width := boxWidth(strings.Join(lines, "\n"), 60)
	if totalLines > 15 {
		lines = append(lines, styleMuted.Render(fmt.Sprintf("... (共 %d 行，已截断)", totalLines)))
	}
	return drawBox(titleStyle, titleText, width-lipgloss.Width(titleText)+2, lines, width)
}

// indentLines 给 s 的每一行加 2 空格前缀，行尾补 \n。
func indentLines(s string) string {
	var sb strings.Builder
	for _, line := range strings.Split(s, "\n") {
		sb.WriteString("  ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// finalAnswerText 渲染最终回答文本（chat.go chatFinalMsg 使用）：Glamour Markdown
// 渲染，失败回退纯文本，每行带 2 空格缩进。
// 渲染器由 ChatModel 持有（m.renderer，NewChatModel 初始化后只读，无并发写）。
func finalAnswerText(m *ChatModel, answer string) string {
	if m.renderer != nil {
		if rendered, err := m.renderer.Render(answer); err == nil {
			return indentLines(strings.TrimRight(rendered, "\n"))
		}
	}
	return indentLines(answer)
}
