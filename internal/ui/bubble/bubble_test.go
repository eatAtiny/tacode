package bubble

import (
	"testing"
)

func TestNewBubbleUI(t *testing.T) {
	b := NewBubbleUI()
	if b == nil {
		t.Fatal("NewBubbleUI returned nil")
	}
	if b.conversation == nil {
		t.Fatal("conversation should not be nil")
	}
}

func TestBubbleUIClose(t *testing.T) {
	b := NewBubbleUI()
	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
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
