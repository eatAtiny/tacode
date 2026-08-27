package bubble

import (
	"strings"
	"testing"
)

// ShowBalance 应把余额存到 ChatModel 的 footer 字段（chatBalanceMsg）。
// 后台 goroutine 调用（query_engine 每轮余额查询）→ Program.Send → footer 字段。
func TestShowBalance_ReachesChatModel(t *testing.T) {
	b := startTest(t)

	b.ShowBalance("💰 ¥110.00")

	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if b.chat.balance != "💰 ¥110.00" {
		t.Errorf("ChatModel.balance = %q, want 💰 ¥110.00", b.chat.balance)
	}
	if !strings.Contains(b.chat.renderFooter(), "¥110.00") {
		t.Errorf("footer 应含余额文本，实际: %q", b.chat.renderFooter())
	}
}
