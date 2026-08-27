package bubble

import (
	"testing"
)

// TestShowBalance_StoresText 验证 ShowBalance 直接打印余额行（追加式）并缓存文本。
// 说明：改造 B（状态栏简化）删除常驻状态栏后，ShowBalance 不再走状态栏更新，
// 而是直接追加打印一行。这里不捕获 stdout（ANSI 输出与测试隔离），
// 仅验证缓存行为 + 打印本身不 panic（fmt.Println 并发安全）。
func TestShowBalance_StoresText(t *testing.T) {
	b := NewBubbleUI()

	b.ShowBalance("💰 ¥110.00")
	if b.balanceText != "💰 ¥110.00" {
		t.Errorf("balanceText = %q, want 已设置", b.balanceText)
	}
}

// balance() 应返回最近一次 ShowBalance 缓存的文本。
func TestBalance_Getter(t *testing.T) {
	b := NewBubbleUI()
	if got := b.balance(); got != "" {
		t.Fatalf("初始 balance() = %q, want 空串", got)
	}

	b.setBalance("💰 ¥110.00")
	if got := b.balance(); got != "💰 ¥110.00" {
		t.Fatalf("balance() = %q, want '💰 ¥110.00'", got)
	}
}
