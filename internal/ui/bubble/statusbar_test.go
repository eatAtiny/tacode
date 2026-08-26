package bubble

import (
	"strings"
	"testing"
)

// renderStatusBar 输出的状态栏文本应包含 session/model/token 信息。
func TestRenderStatusBar(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("deepseek-v4-flash")
	b.UpdateTokens(120, 45)

	line := b.statusBarText()
	for _, want := range []string{"session: demo", "deepseek-v4-flash", "↑ 120", "↓ 45"} {
		if !strings.Contains(line, want) {
			t.Errorf("statusBarText 缺少 %q，实际: %q", want, line)
		}
	}
}

// 余额设置后状态栏包含余额文本。
func TestRenderStatusBar_Balance(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("m")
	b.SetBalanceText("💰 ¥110.00")

	if !strings.Contains(b.statusBarText(), "💰 ¥110.00") {
		t.Errorf("statusBarText 应包含余额，实际: %q", b.statusBarText())
	}
}
