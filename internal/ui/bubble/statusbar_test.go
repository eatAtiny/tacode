package bubble

import (
	"strings"
	"testing"
)

// ShowBalance 应把余额行投递到对话区（chatBalanceMsg 渲染）。
// 后台 goroutine 调用（query_engine 每轮余额查询）→ Program.Send → 对话区。
func TestShowBalance_ReachesChatModel(t *testing.T) {
	b := startTest(t)

	b.ShowBalance("💰 ¥110.00")

	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if len(b.chat.lines) == 0 {
		t.Fatal("对话区无余额行")
	}
	if !strings.Contains(b.chat.lines[0].text, "¥110.00") {
		t.Errorf("对话区应含余额文本，实际: %q", b.chat.lines[0].text)
	}
}
