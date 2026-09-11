package bubble

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// 工具框线标题补线宽度口径回归：应使用显示宽（lipgloss.Width）而非字节数（len）。
// 中文/emoji 标题 "✅ 结果" 字节宽 10 vs 显示宽 7：字节口径补线少画 3 个 ─，
// 框线右端无法按显示宽对齐。
func TestToolResultBox_HeaderDashesUseDisplayWidth(t *testing.T) {
	result := "ok"
	out := toolResultBox("shell", result, false)

	// 标题行 = 2 空格缩进 + "┌─ ✅ 结果 " + 补线（已着色，用显示宽相减得出补线数）。
	header := strings.Split(out, "\n")[0]
	const title = "✅ 结果"
	prefix := "  ┌─ " + title + " "
	gotDashes := lipgloss.Width(header) - lipgloss.Width(prefix)

	// 短内容 → boxWidth 取 minWidth 60。
	width := boxWidth(result, 60)
	want := width - lipgloss.Width(title) + 2
	if gotDashes != want {
		t.Errorf("toolResultBox 标题行补线数 = %d, want %d（显示宽 %d，字节口径 len=%d 少画 %d 个 ─）",
			gotDashes, want, lipgloss.Width(title), len(title), len(title)-lipgloss.Width(title))
	}
}

// toolCallBox 同理：中文工具名 "结果" 字节宽 6 vs 显示宽 4，字节口径少画 2 个 ─。
func TestToolCallBox_HeaderDashesUseDisplayWidth(t *testing.T) {
	out := toolCallBox("结果", `{}`)

	// 标题行 = 2 空格缩进 + "┌─ 🔧 结果 " + 补线。
	header := strings.Split(out, "\n")[0]
	const name = "结果"
	prefix := "  ┌─ 🔧 " + name + " "
	gotDashes := lipgloss.Width(header) - lipgloss.Width(prefix)

	// 短内容 → boxWidth 取 minWidth 60。
	width := boxWidth(`{}`, 60)
	want := width - lipgloss.Width(name) - 6
	if gotDashes != want {
		t.Errorf("toolCallBox 标题行补线数 = %d, want %d（显示宽 %d，字节口径 len=%d 少画 %d 个 ─）",
			gotDashes, want, lipgloss.Width(name), len(name), len(name)-lipgloss.Width(name))
	}
}
