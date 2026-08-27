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

// WindowSizeMsg 更新布局（textarea 宽度；viewport 已删除）。
func TestChatModel_WindowSize(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.width != 80 || m.height != 24 {
		t.Errorf("size = %dx%d, want 80x24", m.width, m.height)
	}
	// bubbles v1 的 SetWidth 含 prompt（"> " 宽 2），Width() 返回内容宽：80-2=78。
	if m.textarea.Width() != 78 {
		t.Errorf("textarea 宽度 = %d, want 78（窗口 80 - prompt 2）", m.textarea.Width())
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
	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if !strings.Contains(all, "思考中") {
		t.Errorf("转录应含思考行，实际:\n%s", all)
	}
	if !strings.Contains(all, "bold") {
		t.Errorf("转录应含渲染后的 final 文本，实际:\n%s", all)
	}
	if !strings.Contains(all, "150 tokens") {
		t.Errorf("转录应含 token 统计行，实际:\n%s", all)
	}
	if !strings.Contains(all, "Error") {
		t.Errorf("转录应含错误行，实际:\n%s", all)
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
// 断言末条转录内容而非总行数（think 行在 Task 5 才移除，行数会变）。
func TestChatModel_FinalNoTokens(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatFinalMsg{content: "ok", totalTokens: 0})

	last := m.lines[len(m.lines)-1]
	if strings.Contains(last.text, "tokens") {
		t.Errorf("totalTokens=0 不应渲染 token 行，实际: %q", last.text)
	}
	if !strings.Contains(last.text, "ok") {
		t.Errorf("final 行应含回答内容，实际: %q", last.text)
	}
}

// 欢迎界面（chatWelcomeMsg）记入转录（首条），无滚动语义。
func TestChatModel_WelcomePrinted(t *testing.T) {
	m := NewChatModel()
	m.Update(chatWelcomeMsg{content: welcomeBanner("deepseek-v4-flash", "v0.1", "/tmp")})
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "agentic") {
		t.Errorf("欢迎行应含 logo，实际: %q", m.lines[0].text)
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

// 历史加载（chatHistoryMsg）清空重建转录，顺序保留。
func TestChatModel_HistoryAppended(t *testing.T) {
	m := NewChatModel()
	var evts []chatHistoryEvent
	for i := 0; i < 30; i++ {
		evts = append(evts, chatHistoryEvent{text: fmt.Sprintf("历史第 %d 行", i)})
	}
	m.Update(chatHistoryMsg{events: evts})
	if len(m.lines) != 30 {
		t.Fatalf("lines = %d, want 30", len(m.lines))
	}
	if !strings.Contains(m.lines[29].text, "历史第 29 行") {
		t.Errorf("末条应为最后一行历史，实际: %q", m.lines[29].text)
	}
}

// 流式增量遇换行定稿：完整段落进 m.lines，残余留 streamBuf。
func TestChatModel_StreamingFlushOnNewline(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "第一段"})
	m.Update(chatDeltaMsg{content: "收尾\n第二段开头"})
	// "…第一段收尾" 已定稿为一条转录（think 行仍在转录且占 1 行，Task 5 移除）。
	if len(m.lines) != 2 {
		t.Fatalf("lines = %d, want 2（思考行 + 换行前定稿段）", len(m.lines))
	}
	seg := m.lines[len(m.lines)-1]
	if !strings.Contains(seg.text, "第一段收尾") {
		t.Errorf("定稿段应含完整段落，实际: %q", seg.text)
	}
	if !strings.Contains(seg.text, "助手") {
		t.Errorf("首个流式段应带助手前缀，实际: %q", seg.text)
	}
	// 残余未定稿。
	if m.streamBuf != "第二段开头" {
		t.Errorf("streamBuf = %q, want 第二段开头", m.streamBuf)
	}

	// final 冲刷残余 + 补 token 行。
	m.Update(chatFinalMsg{content: "第二段开头", totalTokens: 100})
	if m.streamBuf != "" {
		t.Errorf("final 后 streamBuf 应清空，实际: %q", m.streamBuf)
	}
	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if !strings.Contains(all, "第二段开头") || !strings.Contains(all, "100 tokens") {
		t.Errorf("final 应冲刷残余并补统计行，实际:\n%s", all)
	}
}

// final 不重印：有流式内容时 final 不再打印全文（避免双份显示）。
func TestChatModel_FinalNoReprint(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "答案内容完毕\n"})
	m.Update(chatFinalMsg{content: "答案内容完毕", totalTokens: 0})

	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if got := strings.Count(all, "答案内容完毕"); got != 1 {
		t.Errorf("答案应只出现 1 次（流式已定稿，final 不重印），实际 %d 次:\n%s", got, all)
	}
}

// 本轮无流式内容（模型直接答）时 final 打印 glamour 渲染全文。
func TestChatModel_FinalWithoutStreamPrintsRendered(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatFinalMsg{content: "**加粗**回答", totalTokens: 0})

	// think 行仍在转录且占 1 行（Task 5 移除），final 定稿一条 + 无 token 行。
	if len(m.lines) != 2 {
		t.Fatalf("lines = %d, want 2（思考行 + final 渲染全文，无 token 行）", len(m.lines))
	}
	last := m.lines[len(m.lines)-1]
	if !strings.Contains(last.text, "加粗") || !strings.Contains(last.text, "助手") {
		t.Errorf("final 应打印渲染全文 + 助手前缀，实际: %q", last.text)
	}
}

// commit 契约：无参返回 nil；逐段按序记入转录（含空段，保留段落间空行语义）。
func TestChatModel_CommitContract(t *testing.T) {
	m := NewChatModel()
	if cmd := m.commit(); cmd != nil {
		t.Error("无段 commit 应返回 nil")
	}
	_ = m.commit("a", "", "b")
	if len(m.lines) != 3 {
		t.Fatalf("lines = %d, want 3（空段照记）", len(m.lines))
	}
	if m.lines[0].text != "a" || m.lines[1].text != "" || m.lines[2].text != "b" {
		t.Errorf("转录应按序含 a/\"\"/b，实际: %+v", m.lines)
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

// 活区 View 不含对话内容（对话经 tea.Println 定稿进 scrollback，View 只有活区）。
func TestChatModel_ViewExcludesConversation(t *testing.T) {
	m := NewChatModel()
	m.Update(chatFinalMsg{content: "答案正文内容", totalTokens: 0})
	if v := m.View(); strings.Contains(v, "答案正文内容") {
		t.Errorf("View 不应包含对话内容（活区只有输入框/footer），实际:\n%s", v)
	}
	// 定稿内容记入转录 m.lines。
	if len(m.lines) != 1 || !strings.Contains(m.lines[0].text, "答案正文内容") {
		t.Errorf("final 内容应记入 m.lines，实际: %+v", m.lines)
	}
}
