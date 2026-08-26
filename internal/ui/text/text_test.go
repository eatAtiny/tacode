package text

import "testing"

// TestShowBalance_ForwardsEvent 验证 ShowBalance 通过 OnEvent 回调
// 转发 "balance" 事件，data 为原始 line 字符串。
func TestShowBalance_ForwardsEvent(t *testing.T) {
	got := map[string]any{}
	ui := NewTextUI()
	ui.OnEvent = func(name string, data any) {
		got["name"] = name
		got["data"] = data
	}

	ui.ShowBalance("💰 余额: ¥110.00")

	if got["name"] != "balance" {
		t.Errorf("event name = %v, want balance", got["name"])
	}
	if got["data"] != "💰 余额: ¥110.00" {
		t.Errorf("event data = %v, want balance line", got["data"])
	}
}

// TestShowBalance_NilOnEvent 验证 OnEvent 为 nil 时 ShowBalance 不 panic。
func TestShowBalance_NilOnEvent(t *testing.T) {
	ui := NewTextUI() // OnEvent 未设置，默认为 nil
	ui.ShowBalance("💰 余额: ¥110.00")
}
