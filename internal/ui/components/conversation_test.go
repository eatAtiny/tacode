package components

import (
	"strings"
	"testing"
)

func TestConversationEmpty(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	if !c.IsEmpty() {
		t.Fatal("new conversation should be empty")
	}

	view := c.View()
	if view != "" {
		t.Fatalf("empty view should be '', got %q", view)
	}
}

func TestConversationAddUserInput(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	c.AddUserInput("hello world")

	if c.IsEmpty() {
		t.Fatal("should not be empty after AddUserInput")
	}

	view := c.View()
	if !strings.Contains(view, "hello world") {
		t.Fatalf("view should contain 'hello world', got %q", view)
	}
	if !strings.Contains(view, "> ") {
		t.Fatalf("view should contain '> ' prefix, got %q", view)
	}
}

func TestConversationAddFinal(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	// AddFinal without prior delta should work
	c.AddFinal("this is the answer")

	view := c.View()
	if !strings.Contains(view, "this is the answer") {
		t.Fatalf("view should contain answer, got %q", view)
	}
}

func TestConversationDeltaThenFinal(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	// Simulate streaming: delta chunks then final
	c.AddDelta("hel")
	c.AddDelta("lo ")
	c.AddDelta("world")

	// Should have accumulated text
	view := c.View()
	if !strings.Contains(view, "hello world") {
		t.Fatalf("view should contain accumulated delta, got %q", view)
	}

	// AddFinal replaces delta with rendered answer
	c.AddFinal("hello world (final)")

	view = c.View()
	if !strings.Contains(view, "hello world (final)") {
		t.Fatalf("view should contain final answer, got %q", view)
	}
}

func TestConversationMultiRound(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	// Round 1
	c.AddUserInput("question 1")
	c.AddFinal("answer 1")

	// Round 2
	c.AddUserInput("question 2")
	c.AddFinal("answer 2")

	view := c.View()
	if !strings.Contains(view, "question 1") {
		t.Fatal("view should contain question 1")
	}
	if !strings.Contains(view, "answer 1") {
		t.Fatal("view should contain answer 1")
	}
	if !strings.Contains(view, "question 2") {
		t.Fatal("view should contain question 2")
	}
	if !strings.Contains(view, "answer 2") {
		t.Fatal("view should contain answer 2")
	}
}

func TestConversationScroll(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 5) // small height to force scrolling

	// Add many lines
	for i := 0; i < 20; i++ {
		c.AddMessage("line " + string(rune('A'+i)))
	}

	// Should auto-scroll to bottom
	view := c.View()
	if !strings.Contains(view, "line T") {
		t.Fatal("should auto-scroll to show last lines")
	}

	// Scroll up
	c.ScrollUp(10)
	view = c.View()
	if strings.Contains(view, "line T") {
		t.Fatal("after scroll up, last lines should not be visible")
	}

	// Scroll to top
	c.ScrollToTop()
	view = c.View()
	if !strings.Contains(view, "line A") {
		t.Fatal("after scroll to top, first lines should be visible")
	}

	// Scroll to bottom
	c.ScrollToBottom()
	view = c.View()
	if !strings.Contains(view, "line T") {
		t.Fatal("after scroll to bottom, last lines should be visible")
	}
}

func TestConversationAddMessage(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	c.AddMessage("hello")
	c.AddMessage("world")

	view := c.View()
	if !strings.Contains(view, "hello") || !strings.Contains(view, "world") {
		t.Fatalf("view should contain both messages, got %q", view)
	}
}

func TestConversationViewHeight(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 10) // 高度即视口高度：10 行

	for i := 0; i < 20; i++ {
		c.AddMessage("line")
	}

	view := c.View()
	lines := strings.Split(view, "\n")
	// 最多显示视口高度行（10）+ 可能存在的滚动提示行（1）。
	if len(lines) > 11 {
		t.Fatalf("view should respect height limit, got %d lines", len(lines))
	}
}

// 滚动跟随：默认跟随（新内容自动滚底）；SetFollow(false) 后新内容不强制滚底。
func TestConversationFollowLock(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 5)

	for i := 0; i < 20; i++ {
		c.AddMessage("line")
	}
	if !c.IsAtBottom() {
		t.Error("默认应跟随到底部")
	}

	// 用户上滚 → 锁定跟随：新内容追加但不强制滚底。
	c.ScrollUp(5)
	c.SetFollow(false)
	before := c.View()
	c.AddMessage("new content")
	if c.IsAtBottom() {
		t.Error("锁定跟随后新内容不应自动滚到底部")
	}
	after := c.View()
	if strings.Contains(after, "new content") {
		t.Error("锁定跟随后 View 不应显示新内容（滚动位置未变）")
	}
	if after != before {
		t.Error("锁定跟随后新内容到达不应改变 View 输出")
	}

	// 滚回底部 → 解锁跟随：恢复自动滚底。
	c.ScrollToBottom()
	c.SetFollow(true)
	if !c.IsAtBottom() {
		t.Error("解锁跟随后应滚到底部")
	}
	c.AddMessage("final line")
	if !strings.Contains(c.View(), "final line") {
		t.Error("恢复跟随后新内容应自动滚到底部显示")
	}
}

// IsAtBottom 语义：内容不超过视口高度时恒为底部；超过后随滚动位置变化。
func TestConversationIsAtBottom(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 5)

	// 内容不足视口：无可滚动空间，恒为底部。
	c.AddMessage("a")
	if !c.IsAtBottom() {
		t.Error("内容不足视口时应视为底部")
	}

	// 内容超过视口：滚动到顶后不在底部。
	for i := 0; i < 20; i++ {
		c.AddMessage("line")
	}
	if !c.IsAtBottom() {
		t.Error("新内容到达后应自动滚到底部")
	}
	c.ScrollToTop()
	if c.IsAtBottom() {
		t.Error("滚到顶部后不应视为底部")
	}
	c.ScrollToBottom()
	if !c.IsAtBottom() {
		t.Error("滚到底部后应视为底部")
	}
}

// AddBlock 按行追加闭合块：多行内容逐行入列，且不拼接进之前的流式行。
func TestConversationAddBlock(t *testing.T) {
	c := NewConversationModel()
	c.SetSize(80, 24)

	// 流式增量后闭合块：块从新行开始，不与流式 token 拼在同一行。
	c.AddDelta("思考中")
	c.AddDelta("...")
	c.AddBlock("  ✅ 思考完成\n  回答第一行\n  回答第二行\n")

	view := c.View()
	if !strings.Contains(view, "思考中...") {
		t.Errorf("流式增量应保留，实际:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "思考中...") && strings.Contains(line, "✅") {
			t.Errorf("闭合块不应与流式行拼在同一行，实际:\n%s", view)
		}
	}
	if !strings.Contains(view, "回答第一行") || !strings.Contains(view, "回答第二行") {
		t.Errorf("闭合块各应按行保留，实际:\n%s", view)
	}

	// 块闭合后流式增量从新行开始。
	c.AddDelta("新增量")
	view = c.View()
	found := false
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "新增量") && strings.Contains(line, "回答第二行") {
			found = true
		}
	}
	if found {
		t.Errorf("块闭合后的流式增量应新起一行，实际:\n%s", view)
	}
}
