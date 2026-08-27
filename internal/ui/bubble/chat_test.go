package bubble

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agentic/internal/session"

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

// 事件消息（think/final/error）追加对话行并刷新 viewport。
// 余额（chatBalanceMsg）进 footer 字段而非对话行（新版行为）。
func TestChatModel_Events(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	// think
	m.Update(chatThinkMsg{iteration: 1})
	// final（markdown 渲染 + token 行）
	m.Update(chatFinalMsg{content: "**bold** answer", inputTokens: 100, outputTokens: 50, totalTokens: 150})
	// error
	m.Update(chatErrorMsg{err: errSome})
	// balance（进 footer 字段）
	m.Update(chatBalanceMsg{balance: "💰 ¥110.00"})
	// context（进 footer 字段）
	m.Update(chatContextMsg{usedTokens: 100, contextLimit: 50000})

	if len(m.lines) != 4 {
		t.Fatalf("lines = %d, want 4（think+final+token+error，余额不再占对话行）", len(m.lines))
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
	// 余额进 footer。
	if m.balance != "💰 ¥110.00" {
		t.Errorf("balance 字段 = %q, want 💰 ¥110.00", m.balance)
	}
	if !strings.Contains(m.renderFooter(), "¥110.00") {
		t.Errorf("footer 应含余额，实际:\n%s", m.renderFooter())
	}
	// 上下文占用进 footer（已用/总/百分比）。
	if m.contextUsedTokens != 100 || m.contextLimit != 50000 {
		t.Errorf("context = %d/%d, want 100/50000", m.contextUsedTokens, m.contextLimit)
	}
	footer := m.renderFooter()
	if !strings.Contains(footer, "100") || !strings.Contains(footer, "50.0k") || !strings.Contains(footer, "0%") {
		t.Errorf("footer 应含上下文占用（100/50.0k/0%%），实际:\n%s", footer)
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

// 欢迎界面（chatWelcomeMsg）追加后应滚到顶部（logo 可见），而非滚到底部。
// Bug 回归：启动时 viewport 高度未校准（默认 10 行），GotoBottom 会把
// 超出的顶部 logo 滚出视口（「要上滑才能看见」根因）。
func TestChatModel_WelcomeGotoTop(t *testing.T) {
	m := NewChatModel()
	// 模拟启动时序：先收 Welcome（viewport 高度还是默认 10），后收 WindowSizeMsg。
	m.Update(chatWelcomeMsg{content: welcomeBanner("deepseek-v4-flash", "v0.1", "/tmp")})

	if m.viewport.YOffset != 0 {
		t.Errorf("Welcome 后 viewport.YOffset = %d, want 0（应滚到顶部）", m.viewport.YOffset)
	}
	content := m.viewport.View()
	if !strings.Contains(content, "agentic") {
		t.Errorf("viewport 顶部应含 logo（agentic），实际:\n%s", content)
	}
}

// 启动时 UpdateContext(0, limit)：limit 已知即显示初始上下文（0/50.0k (0%)）。
// Bug 回归：旧条件 used>0 才显示，启动时 used=0 导致上下文要等一轮对话后才出现。
func TestChatModel_ContextStartupShown(t *testing.T) {
	m := NewChatModel()
	m.Update(chatContextMsg{usedTokens: 0, contextLimit: 50_000})

	footer := m.renderFooter()
	if !strings.Contains(footer, "上下文 0/50.0k (0%)") {
		t.Errorf("footer 应显示初始上下文 0/50.0k (0%%)，实际:\n%s", footer)
	}
}

// limit=0（数据未就绪）时不显示上下文段。
func TestChatModel_ContextNoLimitHidden(t *testing.T) {
	m := NewChatModel()
	m.Update(chatContextMsg{usedTokens: 0, contextLimit: 0})

	if strings.Contains(m.renderFooter(), "上下文") {
		t.Errorf("limit=0 时不应显示上下文段，实际:\n%s", m.renderFooter())
	}
}

// 历史加载（chatHistoryMsg）后应滚到底部（展示最近一轮结果），而非顶部。
func TestChatModel_HistoryGotoBottom(t *testing.T) {
	m := NewChatModel()
	// 构造足够多的历史行（超过默认 viewport 高度 10），验证滚动到底部。
	var evts []chatHistoryEvent
	for i := 0; i < 30; i++ {
		evts = append(evts, chatHistoryEvent{text: fmt.Sprintf("历史第 %d 行", i)})
	}
	m.Update(chatHistoryMsg{events: evts})

	// 滚到底部：YOffset 应接近最大值（最后一行可见），而非 0（顶部）。
	if m.viewport.YOffset == 0 {
		t.Error("历史加载后应滚到底部（YOffset > 0），而非顶部")
	}
	content := m.viewport.View()
	if !strings.Contains(content, "历史第 29 行") {
		t.Errorf("底部应显示最后一行历史，实际:\n%s", content)
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

// 会话选择器融合：chatPickerMsg 进入选择模式，按键移动光标，Enter 选中。
func TestChatModel_PickerIntegration(t *testing.T) {
	m := NewChatModel()
	m.pickerDone = make(chan string, 1) // 测试直接构造，确保 channel 就绪

	sessions := []session.SessionMeta{
		{ID: "a", Name: "会话A"},
		{ID: "b", Name: "会话B"},
		{ID: "c", Name: "会话C"},
	}

	// 进入选择模式。
	m.Update(chatPickerMsg{sessions: sessions, activeID: "a"})
	if !m.picking {
		t.Fatal("chatPickerMsg 后应进入选择模式（picking=true）")
	}
	if m.picker == nil {
		t.Fatal("picker 应为非 nil")
	}
	// View 渲染选择器。
	if !strings.Contains(m.View(), "会话A") {
		t.Errorf("选择模式 View 应含会话列表，实际:\n%s", m.View())
	}

	// 按下 ↓ 移动光标（应选中 会话B）。
	upd, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_ = upd
	if m.picker.Chosen() != "" {
		t.Fatal("未按 Enter 不应有选择结果")
	}

	// 按 Enter 选中。
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.picking {
		t.Fatal("Enter 后应退出选择模式")
	}
	select {
	case got := <-m.pickerDone:
		if got == "" {
			t.Error("选中结果不应为空")
		}
	default:
		t.Error("pickerDone 应收到选择结果")
	}
}

// 会话选择器取消（Esc）：pickerDone 收到空串。
func TestChatModel_PickerCancel(t *testing.T) {
	m := NewChatModel()
	m.pickerDone = make(chan string, 1)

	m.Update(chatPickerMsg{sessions: []session.SessionMeta{{ID: "a", Name: "会话A"}}, activeID: "a"})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if m.picking {
		t.Fatal("Esc 后应退出选择模式")
	}
	select {
	case got := <-m.pickerDone:
		if got != "" {
			t.Errorf("取消选择应返回空串，got %q", got)
		}
	default:
		t.Error("pickerDone 应收到取消结果")
	}
}
