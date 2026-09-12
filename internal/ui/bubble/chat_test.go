package bubble

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"tacode/internal/session"

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
	if !strings.Contains(v, "tacode") {
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

// WindowSizeMsg 更新布局（textarea 宽度；窗口尺寸字段已删，宽高只喂 textarea）。
func TestChatModel_WindowSize(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	// bubbles v1 的 SetWidth 含 prompt（"> " 宽 2），Width() 返回内容宽：80-2=78。
	if m.textarea.Width() != 78 {
		t.Errorf("textarea 宽度 = %d, want 78（窗口 80 - prompt 2）", m.textarea.Width())
	}
}

// 事件消息（final/error）追加对话行；think 进活区状态行（Task 5 起不占对话行）。
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
	m.Update(chatContextMsg{usedChars: 100, contextLimit: 50000})

	if len(m.lines) != 3 {
		t.Fatalf("lines = %d, want 3（final+token+error，think 进状态行不占对话行，余额不占对话行）", len(m.lines))
	}
	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
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
	if m.contextUsedChars != 100 || m.contextLimit != 50000 {
		t.Errorf("context = %d/%d, want 100/50000", m.contextUsedChars, m.contextLimit)
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
	if !strings.Contains(m.lines[0].text, "████████╗") {
		t.Errorf("欢迎行应含 logo，实际: %q", m.lines[0].text)
	}
}

// 启动时 UpdateContext(0, limit)：limit 已知即显示初始上下文（0/50.0k (0%)）。
// Bug 回归：旧条件 used>0 才显示，启动时 used=0 导致上下文要等一轮对话后才出现。
func TestChatModel_ContextStartupShown(t *testing.T) {
	m := NewChatModel()
	m.Update(chatContextMsg{usedChars: 0, contextLimit: 50_000})

	footer := m.renderFooter()
	if !strings.Contains(footer, "上下文 0/50.0k (0%)") {
		t.Errorf("footer 应显示初始上下文 0/50.0k (0%%)，实际:\n%s", footer)
	}
}

// limit=0（数据未就绪）时不显示上下文段。
func TestChatModel_ContextNoLimitHidden(t *testing.T) {
	m := NewChatModel()
	m.Update(chatContextMsg{usedChars: 0, contextLimit: 0})

	if strings.Contains(m.renderFooter(), "上下文") {
		t.Errorf("limit=0 时不应显示上下文段，实际:\n%s", m.renderFooter())
	}
}

// 历史加载（chatHistoryMsg）清空重建转录（旧对话不残留），顺序保留。
func TestChatModel_HistoryAppended(t *testing.T) {
	m := NewChatModel()
	// 先 seed 一行旧对话，验证历史加载是清空重建（旧的没了）。
	m.Update(chatMessageMsg{content: "旧对话"})
	var evts []string
	for i := 0; i < 30; i++ {
		evts = append(evts, fmt.Sprintf("历史第 %d 行", i))
	}
	m.Update(chatHistoryMsg{events: evts})
	if len(m.lines) != 30 {
		t.Fatalf("lines = %d, want 30（旧对话应被清空，只留历史）", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "历史第 0 行") {
		t.Errorf("首条应为第一行历史，实际: %q", m.lines[0].text)
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
	// "…第一段收尾" 已定稿为一条转录（think 进状态行，不占转录行）。
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（换行前定稿段）", len(m.lines))
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

	// think 进状态行不占转录行；final 定稿一条 + 无 token 行。
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（final 渲染全文，无 token 行）", len(m.lines))
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

// 最大迭代总结路径回归：generateFinalSummary 前会补发 Think（重置 streamed），
// final 的 glamour 重印分支因此可达，总结文本必须上屏（转录）。
func TestChatModel_MaxIterationSummaryDisplayed(t *testing.T) {
	m := NewChatModel()
	// 模拟事件序列：常规迭代（think→delta→toolcall→toolresult→continue）后，
	// 总结路径补发 think（当前 agent 行为）→ final 带总结内容。
	m.Update(chatThinkMsg{iteration: 10})
	m.Update(chatDeltaMsg{content: "常规迭代的输出\n"})
	m.Update(chatToolCallMsg{name: "shell", args: "{}"})
	m.Update(chatToolResultMsg{name: "shell", result: "ok", isError: false})
	m.Update(chatContinueMsg{iteration: 10})
	m.Update(chatThinkMsg{iteration: 10}) // generateFinalSummary 补发的 Think
	m.Update(chatFinalMsg{content: "这是最终总结答案", totalTokens: 50})

	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if !strings.Contains(all, "这是最终总结答案") {
		t.Errorf("总结路径 final 应经 glamour 重印上屏，实际转录:\n%s", all)
	}
}

// 工具调用前残余冲刷顺序：无尾换行的 delta 残余应在框线之前定稿（同一次 commit 保序）。
func TestChatModel_ToolCallFlushOrder(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "残余文本"})
	m.Update(chatToolCallMsg{name: "shell", args: "{}"})

	// 冲刷残余(1) + 框线(1) = 2（think 进状态行不占转录行）。
	if len(m.lines) != 2 {
		t.Fatalf("lines = %d, want 2（残余 + 框线）", len(m.lines))
	}
	// 残余在前、框线在后（同次 commit 保序）。
	if !strings.Contains(m.lines[0].text, "残余文本") {
		t.Errorf("首条应为冲刷的残余文本，实际: %q", m.lines[0].text)
	}
	if !strings.Contains(m.lines[1].text, "🔧") {
		t.Errorf("末条应为工具框线，实际: %q", m.lines[1].text)
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

// 状态行：查询中显示于活区，final/error/message 清空；think 不再进对话区。
func TestChatModel_StatusLine(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	if !strings.Contains(m.View(), "思考中") {
		t.Errorf("活区应显示思考状态，实际:\n%s", m.View())
	}
	if len(m.lines) != 0 {
		t.Errorf("think 不应再进对话区，实际 %d 行", len(m.lines))
	}

	m.Update(chatDeltaMsg{content: "流式文本"})
	if !strings.Contains(m.View(), "输出中") {
		t.Errorf("活区应显示输出状态，实际:\n%s", m.View())
	}

	m.Update(chatToolCallMsg{name: "shell", args: "{}"})
	if !strings.Contains(m.View(), "执行工具: shell") {
		t.Errorf("活区应显示工具执行状态，实际:\n%s", m.View())
	}

	m.Update(chatFinalMsg{content: "答", totalTokens: 0})
	if strings.Contains(m.View(), "执行工具") || strings.Contains(m.View(), "输出中") {
		t.Errorf("final 后状态行应清空，实际:\n%s", m.View())
	}
}

// 新一轮提交重置状态行。
func TestChatModel_StatusResetOnSubmit(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.textarea.SetValue("继续")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "思考中") {
		t.Errorf("提交后状态行应重置，实际:\n%s", m.View())
	}
}

// 权限确认期间状态行隐藏（弹层已展示工具信息，避免叠加误导），作出决定后恢复。
func TestChatModel_StatusHiddenDuringPermission(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatPermissionMsg{tool: "shell", args: `{"command":"rm -rf /"}`, reason: "高风险操作"})
	v := m.View()
	if strings.Contains(v, "思考中") {
		t.Errorf("权限确认期间状态行应隐藏，实际:\n%s", v)
	}
	if !strings.Contains(v, "权限确认") {
		t.Errorf("权限弹层应显示，实际:\n%s", v)
	}
	// 作出决定后弹层清除，状态行恢复（status 未被其他事件清空时）。
	m.Update(chatPermissionArmedMsg{})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.permLayer != nil {
		t.Fatal("作出决定后弹层应清除")
	}
	if !strings.Contains(m.View(), "思考中") {
		t.Errorf("确认完成后状态行应恢复，实际:\n%s", m.View())
	}
}

// 权限确认期间键入的回车不得进入 submitCh，也不得清空状态行——弹层是模态的，
// 权限答案不走文本输入流，用户此刻打的字一个字节都不该被当作回答消费掉。
func TestChatModel_PermissionSubmitBlocked(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatToolCallMsg{name: "shell", args: "{}"})
	m.Update(chatPermissionMsg{tool: "shell", args: "{}", reason: "高危"})
	if m.status == "" {
		t.Fatal("前置失败：工具调用后状态行应非空")
	}

	// 用户仍在往 textarea 里打字并敲了回车。此处未武装（等价于弹层尚未上屏），
	// 按键必须被完整丢弃——既不能提交，也不能被当成权限答案。
	m.textarea.SetValue("y")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case v := <-m.submitCh:
		t.Errorf("弹层期间的按键不得进入 submitCh，却收到 %q", v)
	default:
	}
	select {
	case approved := <-m.permissionDone:
		t.Errorf("未武装时不得产生权限决定，却得到 approved=%v", approved)
	default:
	}
	if m.status == "" {
		t.Error("被丢弃的按键不应清空状态行")
	}
}

// 错误中断冲刷：流式残余随错误行定稿，不泄漏到下一轮（不重复注入助手前缀）。
func TestChatModel_ErrorFlushesStream(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "流式中断的部分"})
	m.Update(chatErrorMsg{err: errSome})

	if m.streamBuf != "" {
		t.Errorf("错误后 streamBuf 应清空，实际: %q", m.streamBuf)
	}
	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if !strings.Contains(all, "流式中断的部分") {
		t.Errorf("残余应随错误行定稿，实际:\n%s", all)
	}

	// 下一轮首个 delta 只注入一次助手前缀。
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "新轮\n"})
	// 区分度断言用末行单行计数：无冲刷的旧 bug 下泄漏残余与新轮 delta 会
	// 合并成同一行（行内两个前缀），全量计数仍是 2、无区分度；单行计数
	// 正常=1（残余已随错误行单独定稿）而 bug=2（合并进一行），才能区分。
	last := m.lines[len(m.lines)-1]
	if got := strings.Count(last.text, "助手"); got != 1 {
		t.Errorf("新轮末行应恰含 1 个助手前缀，实际 %d 个: %q", got, last.text)
	}
	// 补充：全量恰好 2 次（每轮一次）。
	all2 := ""
	for _, l := range m.lines {
		all2 += l.text + "\n"
	}
	if got := strings.Count(all2, "助手"); got != 2 {
		t.Errorf("助手前缀应恰好 2 次（每轮一次），实际 %d 次:\n%s", got, all2)
	}
}

// 多行粘贴回归：bracketed paste（v1.3.10 默认开启）以 KeyMsg{Paste:true}
// 到达，textarea 多行插入，换行不触发 Enter 提交。
func TestChatModel_PasteMultiline(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("第一行\n第二行"), Paste: true})

	if !strings.Contains(m.textarea.Value(), "\n") {
		t.Errorf("多行粘贴应保留换行，实际: %q", m.textarea.Value())
	}
	select {
	case got := <-m.SubmitCh():
		t.Errorf("粘贴不应触发提交，got %q", got)
	default:
	}
}
