package bubble

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// errSome 测试用的占位错误。
var errSome = errors.New("test error")

// ChatModel 的 View 包含输入框和 footer。
func TestChatModel_View(t *testing.T) {
	m := NewChatModel()
	v := m.View()
	if !strings.Contains(v, "输入消息") {
		t.Errorf("View 应包含输入框 placeholder，实际:\n%s", v)
	}
	if !strings.Contains(v, "agentic") {
		t.Errorf("View 应包含 footer，实际:\n%s", v)
	}
}

// 提交输入写入 submitCh，用户消息进入对话区。
func TestChatModel_Submit(t *testing.T) {
	m := NewChatModel()
	// 模拟输入 + Enter。
	m.textarea.SetValue("hello")
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	m.Update(msg)

	select {
	case got := <-m.SubmitCh():
		if got != "hello" {
			t.Errorf("submit = %q, want hello", got)
		}
	default:
		t.Error("submitCh 无消息")
	}

	// 用户消息已进对话区。
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（用户消息应进对话区）", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "你: hello") {
		t.Errorf("对话区首行应为用户消息，实际: %q", m.lines[0].text)
	}
}

// 空输入按 Enter 不应提交。
func TestChatModel_SubmitEmpty(t *testing.T) {
	m := NewChatModel()
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	m.Update(msg)

	select {
	case got := <-m.SubmitCh():
		t.Errorf("空输入不应提交，got %q", got)
	default:
		// 期望无消息。
	}
	if len(m.lines) != 0 {
		t.Errorf("空输入不应追加对话行，实际 %d 行", len(m.lines))
	}
}

// WindowSizeMsg 更新布局（viewport 高度 = 窗口 - textarea - footer）。
func TestChatModel_WindowSize(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.width != 80 || m.height != 24 {
		t.Errorf("size = %dx%d, want 80x24", m.width, m.height)
	}
	want := 24 - m.textarea.Height() - 2
	if m.viewport.Height != want {
		t.Errorf("viewport.Height = %d, want %d", m.viewport.Height, want)
	}
	if m.viewport.Width != 80 {
		t.Errorf("viewport.Width = %d, want 80", m.viewport.Width)
	}
}

// 事件消息（think/final/error/balance）追加对话行并刷新 viewport。
func TestChatModel_Events(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// think
	m.Update(chatThinkMsg{iteration: 1})
	// final（markdown 渲染）
	m.Update(chatFinalMsg{content: "**bold** answer"})
	// error
	m.Update(chatErrorMsg{err: errSome})
	// balance
	m.Update(chatBalanceMsg{balance: "💰 ¥110.00"})

	if len(m.lines) != 4 {
		t.Fatalf("lines = %d, want 4", len(m.lines))
	}
	content := m.viewport.View()
	if !strings.Contains(content, "思考中") {
		t.Errorf("viewport 应含思考行，实际:\n%s", content)
	}
	if !strings.Contains(content, "bold") {
		t.Errorf("viewport 应含渲染后的 final 文本，实际:\n%s", content)
	}
	if !strings.Contains(content, "Error") {
		t.Errorf("viewport 应含错误行，实际:\n%s", content)
	}
	if !strings.Contains(content, "¥110.00") {
		t.Errorf("viewport 应含余额行，实际:\n%s", content)
	}
}
