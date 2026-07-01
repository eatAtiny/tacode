package ui

import (
	"testing"
)

func TestNewBubbleUI(t *testing.T) {
	b := NewBubbleUI()
	if b == nil {
		t.Fatal("NewBubbleUI returned nil")
	}
	if b.scanner == nil {
		t.Fatal("scanner should not be nil")
	}
}

func TestBubbleUIClose(t *testing.T) {
	b := NewBubbleUI()
	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

func TestBubbleUISetters(t *testing.T) {
	b := NewBubbleUI()

	b.SetSessionName("test")
	if b.sessionName != "test" {
		t.Fatalf("sessionName should be 'test', got %q", b.sessionName)
	}

	b.SetModel("gpt-4")
	if b.model != "gpt-4" {
		t.Fatalf("model should be 'gpt-4', got %q", b.model)
	}

	b.UpdateTokens(100, 50)
	if b.inputTokens != 100 || b.outputTokens != 50 {
		t.Fatalf("tokens should be 100/50, got %d/%d", b.inputTokens, b.outputTokens)
	}

	b.ResetTokens()
	if b.inputTokens != 0 || b.outputTokens != 0 {
		t.Fatalf("tokens should be 0/0 after reset, got %d/%d", b.inputTokens, b.outputTokens)
	}
}

func TestTrimArgs(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"hello world", 5, "hello..."},
		{"你好世界测试", 3, "你好世..."},
	}
	for _, tt := range tests {
		got := trimArgs(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("trimArgs(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}
