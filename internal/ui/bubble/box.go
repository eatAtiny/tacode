// 框线渲染辅助：chat.go（ChatModel）的工具消息渲染共用文本生成函数。
// 框线统一在此生成，chat.go 不再内联绘制（消除双份逻辑）。
package bubble

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
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
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(styleToolPrefix.Render("┌─ 🔧 " + name))
	sb.WriteString(" ")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", max(0, width-len(name)-6))))
	sb.WriteString("\n")
	for _, line := range strings.Split(displayArgs, "\n") {
		sb.WriteString("  ")
		sb.WriteString(styleToolPrefix.Render("│"))
		sb.WriteString(" ")
		sb.WriteString(styleMuted.Render(line))
		sb.WriteString("\n")
	}
	sb.WriteString("  ")
	sb.WriteString(styleToolPrefix.Render("└" + strings.Repeat("─", width+1)))
	sb.WriteString("\n")
	return sb.String()
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
	var sb strings.Builder
	sb.WriteString("  ")
	sb.WriteString(titleStyle.Render("┌─ " + titleText))
	sb.WriteString(" ")
	sb.WriteString(styleMuted.Render(strings.Repeat("─", max(0, width-len(titleText)+2))))
	sb.WriteString("\n")
	for _, line := range lines {
		sb.WriteString("  ")
		sb.WriteString(titleStyle.Render("│"))
		sb.WriteString(" ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if totalLines > 15 {
		sb.WriteString("  ")
		sb.WriteString(titleStyle.Render("│"))
		sb.WriteString(" ")
		sb.WriteString(styleMuted.Render(fmt.Sprintf("... (共 %d 行，已截断)", totalLines)))
		sb.WriteString("\n")
	}
	sb.WriteString("  ")
	sb.WriteString(titleStyle.Render("└" + strings.Repeat("─", width+1)))
	sb.WriteString("\n")
	return sb.String()
}

// finalAnswerText 渲染最终回答文本（chat.go chatFinalMsg 使用）：Glamour Markdown
// 渲染，失败回退纯文本，每行带 2 空格缩进。
// 渲染器由 ChatModel 持有（m.renderer，NewChatModel 初始化后只读，无并发写）。
func finalAnswerText(m *ChatModel, answer string) string {
	renderer := (*glamour.TermRenderer)(nil)
	if m != nil {
		renderer = m.renderer
	}
	if renderer != nil {
		rendered, err := renderer.Render(answer)
		if err == nil {
			var sb strings.Builder
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				sb.WriteString("  ")
				sb.WriteString(line)
				sb.WriteString("\n")
			}
			return sb.String()
		}
	}
	var sb strings.Builder
	for _, line := range strings.Split(answer, "\n") {
		sb.WriteString("  ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}
