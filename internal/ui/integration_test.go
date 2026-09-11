package ui

import (
	"testing"

	"tacode/internal/ui/bubble"
	"tacode/internal/ui/text"
)

// TestImplementsUIInterface 验证 BubbleUI 实现了 UI 接口。
func TestImplementsUIInterface(t *testing.T) {
	var _ UI = &bubble.BubbleUI{}
	var _ UI = bubble.NewBubbleUI()
}

// TestImplementsTextUIInterface 验证 TextUI 实现了 UI 接口。
func TestImplementsTextUIInterface(t *testing.T) {
	var _ UI = &text.TextUI{}
	var _ UI = text.NewTextUI()
}
