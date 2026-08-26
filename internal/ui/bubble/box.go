// 框线渲染辅助：阶段 1（ANSI 直写）与 tea 模式共用的文本生成函数。
// boxWidth 定义在 bubble.go（阶段 1 与 box.go 共用）。
package bubble

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolCallBox 生成工具调用框线文本（阶段 1 与 tea 模式共用）。
// 参数 JSON 格式化缩进显示，单行参数用紧凑格式。
//
// 结尾带 \n：tea 模式下 block 追加以 \n 结尾表示"闭合行"（见 tea_model.go 的
// teaAppendMsg 合并语义），配合 Update 里的换行断开逻辑让工具框线在流式行后另起。
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

// toolResultBox 生成工具执行结果框线文本（阶段 1 与 tea 模式共用）。
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

// finalAnswerText 渲染最终回答文本（tea 模式用）：Glamour Markdown 渲染，
// 失败回退纯文本，带 2 空格缩进，每行结尾带 \n（block 追加闭合语义）。
// 不直接使用 b.glamour 字段读——渲染器初始化后只读，无并发写，直接读安全。
func finalAnswerText(b *BubbleUI, answer string) string {
	if b.glamour != nil {
		rendered, err := b.glamour.Render(answer)
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
