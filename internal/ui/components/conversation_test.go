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
	c.SetSize(80, 10) // height=10, viewport=10-2=8

	for i := 0; i < 20; i++ {
		c.AddMessage("line")
	}

	view := c.View()
	lines := strings.Split(view, "\n")
	// Should show at most viewportHeight lines (plus maybe scroll indicator)
	if len(lines) > 9 { // 8 + scroll indicator
		t.Fatalf("view should respect height limit, got %d lines", len(lines))
	}
}
