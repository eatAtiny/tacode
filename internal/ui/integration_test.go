package ui

import (
	"testing"
)

// TestImplementsUIInterface 验证 BubbleUI 实现了 UI 接口。
func TestImplementsUIInterface(t *testing.T) {
	var _ UI = &BubbleUI{}
	var _ UI = NewBubbleUI()
}

// TestImplementsTextUIInterface 验证 TextUI 实现了 UI 接口。
func TestImplementsTextUIInterface(t *testing.T) {
	var _ UI = &TextUI{}
	var _ UI = NewTextUI()
}
