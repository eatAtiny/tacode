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

// ShowBalance 更新状态栏余额段（不单独打印）。
func TestShowBalance_UpdatesStatusBar(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("m")

	b.ShowBalance("💰 ¥110.00")
	if b.balanceText != "💰 ¥110.00" {
		t.Errorf("balanceText = %q, want 已设置", b.balanceText)
	}
	if !strings.Contains(b.statusBarText(), "💰 ¥110.00") {
		t.Errorf("状态栏应含余额，实际: %q", b.statusBarText())
	}
}

// UpdateTokens 后状态栏 token 段更新。
func TestStatusBar_UpdateTokens(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("m")

	b.UpdateTokens(120, 45)
	if !strings.Contains(b.statusBarText(), "↑ 120") || !strings.Contains(b.statusBarText(), "↓ 45") {
		t.Errorf("状态栏应含累计 token，实际: %q", b.statusBarText())
	}
}

// 状态栏生命周期：render 后 shown，clear 后 unshown。
// 说明：本测试在改造 A（删 tea 主渲染）前依赖 renderStatusBar/clearStatusBar
// 的 ANSI 输出（写 os.Stdout），且改造 B（状态栏简化）将重做状态栏实现，
// 故删除——保留 statusBarText 文本内容的断言（改造 B 后语义仍有效）。

// 多行余额文本被 clamp 成单行。
func TestStatusBar_MultiLineBalance(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("m")
	b.SetBalanceText("💰 ¥110.00\n💰 $5.00")

	if strings.Contains(b.statusBarText(), "\n") {
		t.Errorf("状态栏不应含换行，实际: %q", b.statusBarText())
	}
}
