package bubble

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// errSome 测试用的占位错误。
var errSome = errors.New("test error")

// sleepMs 测试辅助：短暂休眠（轮询等待 tea 事件循环消费消息）。
func sleepMs(ms int) {
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

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
	if !strings.Contains(m.lines[0].text, "> hello") {
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

// 双轮提交：第一轮提交 + 事件流后，第二轮仍能提交（Bug 2 复现）。
func TestChatModel_SubmitTwice(t *testing.T) {
	m := NewChatModel()

	// 第一轮：输入 + Enter。
	m.textarea.SetValue("第一轮")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-m.SubmitCh():
		if got != "第一轮" {
			t.Errorf("第一轮 submit = %q, want 第一轮", got)
		}
	default:
		t.Fatal("第一轮 submitCh 无消息")
	}

	// 模拟第一轮事件流（think → delta → final）。
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "增量"})
	m.Update(chatFinalMsg{content: "回答", totalTokens: 100})

	// 第二轮：输入 + Enter。
	m.textarea.SetValue("第二轮")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-m.SubmitCh():
		if got != "第二轮" {
			t.Errorf("第二轮 submit = %q, want 第二轮", got)
		}
	default:
		t.Fatal("第二轮 submitCh 无消息（Bug 2：第二轮输入无反应）")
	}

	// 第二轮用户消息应进对话区。
	if len(m.lines) < 3 {
		t.Fatalf("lines = %d, want >= 3（两轮用户消息 + 事件行）", len(m.lines))
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
	// final（markdown 渲染 + token 行）
	m.Update(chatFinalMsg{content: "**bold** answer", inputTokens: 100, outputTokens: 50, totalTokens: 150})
	// error
	m.Update(chatErrorMsg{err: errSome})
	// balance
	m.Update(chatBalanceMsg{balance: "💰 ¥110.00"})

	if len(m.lines) != 5 {
		t.Fatalf("lines = %d, want 5（think+final+token+error+balance）", len(m.lines))
	}
	content := m.viewport.View()
	if !strings.Contains(content, "思考中") {
		t.Errorf("viewport 应含思考行，实际:\n%s", content)
	}
	if !strings.Contains(content, "bold") {
		t.Errorf("viewport 应含渲染后的 final 文本，实际:\n%s", content)
	}
	if !strings.Contains(content, "150 tokens") {
		t.Errorf("viewport 应含 token 统计行，实际:\n%s", content)
	}
	if !strings.Contains(content, "Error") {
		t.Errorf("viewport 应含错误行，实际:\n%s", content)
	}
	if !strings.Contains(content, "¥110.00") {
		t.Errorf("viewport 应含余额行，实际:\n%s", content)
	}
}

// totalTokens 为 0（API 未返回 usage）时不应展示 token 统计行。
func TestChatModel_FinalNoTokens(t *testing.T) {
	m := NewChatModel()
	m.Update(chatFinalMsg{content: "ok", totalTokens: 0})

	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（无 token 行）", len(m.lines))
	}
	if strings.Contains(m.lines[0].text, "tokens") {
		t.Errorf("totalTokens=0 不应渲染 token 行，实际: %q", m.lines[0].text)
	}
}

// 流式 delta 合并到最后一行（不逐条 append）。
func TestChatModel_StreamingMerge(t *testing.T) {
	m := NewChatModel()
	m.Update(chatDeltaMsg{content: "你好"})
	m.Update(chatDeltaMsg{content: "，世界"})
	m.Update(chatDeltaMsg{content: "！"})

	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（delta 应合并到同一行）", len(m.lines))
	}
	if m.lines[0].text != "你好，世界！" {
		t.Errorf("流式合并文本 = %q, want 你好，世界！", m.lines[0].text)
	}
	// streaming 标记保持 true，View 末尾应带 ▌ 光标标记。
	if !m.lines[0].streaming {
		t.Error("流式行 streaming 标记应为 true")
	}
	if v := m.View(); !strings.Contains(v, "▌") {
		t.Errorf("流式行应带 ▌ 光标标记，实际:\n%s", v)
	}

	// final 用渲染后的最终回答原地替换流式行（增量与最终回答同源，避免双份显示），
	// streaming 标记关闭。
	m.Update(chatFinalMsg{content: "回答", totalTokens: 0})
	if m.lines[0].streaming {
		t.Error("final 后流式行 streaming 标记应关闭")
	}
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（final 应替换流式行而非追加）", len(m.lines))
	}
	// 流式文本已被最终回答替换（不再双份显示）。
	if !strings.Contains(m.lines[0].text, "回答") {
		t.Errorf("流式行应被最终回答替换，实际: %q", m.lines[0].text)
	}
}

// 工具调用/结果用 box.go 框线渲染。
func TestChatModel_ToolMessages(t *testing.T) {
	m := NewChatModel()
	m.Update(chatToolCallMsg{name: "shell", args: `{"command":"ls -la"}`})
	m.Update(chatToolResultMsg{name: "shell", result: "total 8", isError: false})

	if len(m.lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "🔧") {
		t.Errorf("工具调用行应含框线标题，实际: %q", m.lines[0].text)
	}
	if !strings.Contains(m.lines[1].text, "✅ 结果") {
		t.Errorf("工具结果行应含成功标题，实际: %q", m.lines[1].text)
	}
}

// Update 返回值满足 tea.Model 接口（签名约束）。
func TestChatModel_ImplementsTeaModel(t *testing.T) {
	var _ tea.Model = NewChatModel()
}
