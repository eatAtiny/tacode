package bubble

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"tacode/internal/memory"

	tea "github.com/charmbracelet/bubbletea"
)

// testStartOpts 测试用 tea 程序选项：无渲染（避免终端初始化）+ 空输入
// （非 TTY stdin 下 Program.Run 尝试打开 /dev/tty，测试环境不可用）。
func testStartOpts() []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithoutRenderer(),
		tea.WithInput(bytes.NewReader(nil)),
	}
}

// startTest 创建 BubbleUI 并以测试选项启动。
func startTest(t *testing.T) *BubbleUI {
	t.Helper()
	b := NewBubbleUI()
	if err := b.Start(testStartOpts()...); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestNewBubbleUI(t *testing.T) {
	b := NewBubbleUI()
	if b == nil {
		t.Fatal("NewBubbleUI returned nil")
	}
	if b.chat == nil {
		t.Fatal("chat should not be nil")
	}
	if b.submitCh == nil {
		t.Fatal("submitCh should not be nil")
	}
	// Start 前 program 应为 nil（Start 时才创建）。
	if b.program != nil {
		t.Fatal("program should be nil before Start")
	}
}

// ReadInputChan 应返回 chat 的提交 channel（textarea Enter → submitCh）。
func TestReadInputChan_BridgesSubmitCh(t *testing.T) {
	b := NewBubbleUI()
	ch := b.ReadInputChan()
	if ch == nil {
		t.Fatal("ReadInputChan returned nil")
	}
	// 向 chat 的 textarea 注入输入并提交，验证能从 ReadInputChan 读到。
	b.chat.textarea.SetValue("hello")
	b.chat.Update(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case got := <-ch:
		if got != "hello" {
			t.Errorf("ReadInputChan = %q, want hello", got)
		}
	default:
		t.Error("ReadInputChan 无消息（提交应桥接到 input channel）")
	}
}

// 事件方法投递消息：Start 后 send 应更新 ChatModel 的对话区。
// Close 同步等待 tea 事件循环退出（QuitMsg 前的消息已全部处理），
// 之后读对话区无数据竞争（-race 验证）。
func TestEvents_ReachChatModel(t *testing.T) {
	b := startTest(t)

	// 事件方法 → Program.Send → ChatModel.Update → lines 追加
	// （think 例外：进活区状态行不占转录，送达观测见 TestOnThink_SetsStatus）。
	b.OnThink(1)
	b.OnMessage("状态更新")
	b.OnFinal("最终回答", 100, 50, 150)

	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if len(b.chat.lines) < 3 {
		t.Fatalf("lines = %d, want >= 3（事件应送达 ChatModel）", len(b.chat.lines))
	}
	var joined string
	for _, l := range b.chat.lines {
		joined += l.text + "\n"
	}
	if !strings.Contains(joined, "状态更新") {
		t.Errorf("对话区应含消息行，实际:\n%s", joined)
	}
	if !strings.Contains(joined, "最终回答") {
		t.Errorf("对话区应含最终回答，实际:\n%s", joined)
	}
	if !strings.Contains(joined, "150 tokens") {
		t.Errorf("对话区应含 token 统计行，实际:\n%s", joined)
	}
}

// OnThink 送达 ChatModel：think 不再触碰 lines，送达只能从活区状态行观测。
// 不在 TestEvents_ReachChatModel 里轮询断言——事件循环 goroutine 写 status、
// 测试 goroutine 读无 happens-before 边，-race 实测报数据竞争；改为 Close
// （join 事件循环，Quit 前消息已全部处理）后读取，与本文件读 lines 模式一致。
func TestOnThink_SetsStatus(t *testing.T) {
	b := startTest(t)

	b.OnThink(1)

	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if b.chat.status == "" {
		t.Error("OnThink 后 status 应非空（think 送达只能从状态行观测）")
	}
}

func TestBubbleUIClose(t *testing.T) {
	b := NewBubbleUI()
	if err := b.Close(); err != nil {
		t.Fatalf("Close error (未 Start): %v", err)
	}
	// Start 后 Close 应正常退出 tea 程序。
	if err := b.Start(testStartOpts()...); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close error (已 Start): %v", err)
	}
}

// Start 幂等：重复调用不创建新程序。
func TestStart_Idempotent(t *testing.T) {
	b := NewBubbleUI()
	if err := b.Start(testStartOpts()...); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	first := b.program
	if err := b.Start(); err != nil {
		t.Fatalf("重复 Start error: %v", err)
	}
	if b.program != first {
		t.Error("重复 Start 不应替换 tea.Program")
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
}

// ──────────────────────────────────────────────────────────
// 权限弹层（模态）：按键由弹层消费，决定经 permissionDone 回传
// ──────────────────────────────────────────────────────────

// newPermissionModel 构造处于权限弹层模态的 ChatModel。
// armed 为 true 时投递武装消息（模拟 permArmDelay 到期），走真实武装路径，
// 测试不依赖真实时钟。
func newPermissionModel(t *testing.T, tool, args, reason string, armed bool) *ChatModel {
	t.Helper()
	m := NewChatModel()
	_, _ = m.Update(chatPermissionMsg{tool: tool, args: args, reason: reason})
	if m.permLayer == nil {
		t.Fatal("chatPermissionMsg 应置起弹层")
	}
	if armed {
		_, _ = m.Update(chatPermissionArmedMsg{})
		if !m.permLayer.armed {
			t.Fatal("武装消息应置位 armed")
		}
	}
	return m
}

// readPermissionDecision 非阻塞读取弹层决定，返回 (有无决定, 是否批准)。
func readPermissionDecision(m *ChatModel) (got, approved bool) {
	select {
	case approved = <-m.permissionDone:
		return true, approved
	default:
		return false, false
	}
}

// 默认选中 YES，Enter 确认即批准；弹层随后清除。
func TestPermissionLayer_DefaultYesEnterApproves(t *testing.T) {
	m := newPermissionModel(t, "shell", `{"command":"rm -rf /"}`, "高风险操作", true)

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	got, approved := readPermissionDecision(m)
	if !got {
		t.Fatal("Enter 应产生决定")
	}
	if !approved {
		t.Error("默认选中 YES + Enter 应批准")
	}
	if m.permLayer != nil {
		t.Error("作出决定后弹层应清除")
	}
}

// 渲染：含工具信息与 YES/NO 两个选项。
func TestPermissionLayer_Render(t *testing.T) {
	m := &ChatModel{permLayer: &permissionLayer{
		tool: "shell", args: `{"command":"rm -rf /"}`, reason: "高风险操作",
	}}
	layer := m.renderPermissionLayer()
	for _, want := range []string{"权限确认", "shell", "rm -rf", "高风险操作", "YES", "NO"} {
		if !strings.Contains(layer, want) {
			t.Errorf("权限弹层应含 %q，实际:\n%s", want, layer)
		}
	}
}

// n 与 Esc 均直达拒绝。
func TestPermissionLayer_NAndEscReject(t *testing.T) {
	t.Run("n", func(t *testing.T) {
		m := newPermissionModel(t, "shell", "{}", "", true)
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
		got, approved := readPermissionDecision(m)
		if !got || approved {
			t.Errorf("n 应产生拒绝决定，got=%v approved=%v", got, approved)
		}
	})
	t.Run("esc", func(t *testing.T) {
		m := newPermissionModel(t, "shell", "{}", "", true)
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		got, approved := readPermissionDecision(m)
		if !got || approved {
			t.Errorf("Esc 应产生拒绝决定，got=%v approved=%v", got, approved)
		}
	})
}

// ↑↓ 切换选中项，Enter 确认当前项。
func TestPermissionLayer_ArrowSelectsThenEnter(t *testing.T) {
	t.Run("down-then-enter-rejects", func(t *testing.T) {
		m := newPermissionModel(t, "shell", "{}", "", true)
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		if m.permLayer.selected != permNo {
			t.Fatalf("↓ 后应选中 NO，got %d", m.permLayer.selected)
		}
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got, approved := readPermissionDecision(m)
		if !got || approved {
			t.Errorf("↓ + Enter 应拒绝，got=%v approved=%v", got, approved)
		}
	})
	t.Run("down-up-then-enter-approves", func(t *testing.T) {
		m := newPermissionModel(t, "shell", "{}", "", true)
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
		if m.permLayer.selected != permYes {
			t.Fatalf("↑ 后应选回 YES，got %d", m.permLayer.selected)
		}
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		got, approved := readPermissionDecision(m)
		if !got || !approved {
			t.Errorf("↑ + Enter 应批准，got=%v approved=%v", got, approved)
		}
	})
}

// 核心回归：弹层期间键入的回车绝不能被当成回答，也绝不能进入 submitCh。
//
// 这是本次改造要修的那个 bug——用户正在打消息时敲的回车，若被当作 y/N 答案，
// 会导致消息静默丢失 + 默认选中 YES 时静默批准一次危险操作。上屏闸未开
// （armed=false，等价于用户还看不见弹层）时，按键必须被完整丢弃。
func TestPermissionLayer_DisarmedDropsTypedEnter(t *testing.T) {
	m := newPermissionModel(t, "shell", `{"command":"rm -rf /"}`, "高风险操作", false)
	m.textarea.SetValue("帮我看看这个 bug")

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// 决定：绝不能产生（否则就是静默批准/拒绝）。
	if got, approved := readPermissionDecision(m); got {
		t.Fatalf("未武装时不得产生决定，却得到 approved=%v", approved)
	}
	// 提交：绝不能到达 Runner。
	select {
	case v := <-m.submitCh:
		t.Fatalf("弹层期间的按键不得进入 submitCh，却收到 %q", v)
	default:
	}
	// 弹层仍在等（用户仍可作出决定）。
	if m.permLayer == nil {
		t.Error("未作出决定时弹层不应清除")
	}
	if m.textarea.Value() != "帮我看看这个 bug" {
		t.Errorf("草稿应保留，实际 %q", m.textarea.Value())
	}

	// 武装后同一个回车才生效。
	_, _ = m.Update(chatPermissionArmedMsg{})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got, approved := readPermissionDecision(m)
	if !got || !approved {
		t.Errorf("武装后 Enter 应批准，got=%v approved=%v", got, approved)
	}
}

// ctrl+c 在弹层期间必须仍然是退出逃生口（不受武装闸限制）。
func TestPermissionLayer_CtrlCQuits(t *testing.T) {
	m := newPermissionModel(t, "shell", "{}", "", false)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c 应返回退出命令")
	}
	if got, _ := readPermissionDecision(m); got {
		t.Error("ctrl+c 不应产生权限决定")
	}
}

// Message 类型实现：直接驱动 ChatModel（同一 tea 事件循环 goroutine）。
var _ tea.Model = (*ChatModel)(nil)

// BubbleUI.ConfirmPermission 与弹层的完整往返：发 chatPermissionMsg → 用户
// 按键经 Program.Send 投递 → 弹层作出决定 → permissionDone → 方法返回。
// 这条链路验证 ConfirmPermission 确实接在 permissionDone 上（而非旧输入流）。
//
// 用真实时钟等待 permArmDelay（3× 冗余），不注入武装消息——本测试的目的
// 正是覆盖「武装消息由 tea.Tick 自己送达」这条生产路径。
func TestConfirmPermission_ModalRoundTrip(t *testing.T) {
	b := startTest(t)

	resCh := make(chan bool, 1)
	errCh := make(chan error, 1)
	go func() {
		approved, err := b.ConfirmPermission("shell", `{"command":"ls"}`, "需确认")
		if err != nil {
			errCh <- err
			return
		}
		resCh <- approved
	}()

	// 等 tea.Tick 武装弹层后再投按键（先投会被武装闸丢弃）。
	time.Sleep(3 * permArmDelay)
	b.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	select {
	case approved := <-resCh:
		if !approved {
			t.Error("按 y 应批准")
		}
	case err := <-errCh:
		t.Fatalf("ConfirmPermission error: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ConfirmPermission 未返回")
	}
}

// tea 未启动时 ConfirmPermission 必须拒绝而非默认放行——拿不到用户同意
// 就不能执行高危操作。旧实现此路径返回 EOF 错误，调用方按拒绝处理。
func TestConfirmPermission_NotStartedRejects(t *testing.T) {
	b := NewBubbleUI() // 未 Start：program 为 nil

	approved, err := b.ConfirmPermission("shell", `{"command":"rm -rf /"}`, "")
	if err == nil {
		t.Error("未启动 tea 时应返回错误")
	}
	if approved {
		t.Error("未启动 tea 时不得默认放行")
	}
}

// ShowHistory 把历史事件渲染为结构化对话行（用户/助手/工具框线），非原始 JSON。
// 助手回答完整显示；工具结果用 toolResultBox（15 行截断，工具结果不该全量）。
func TestShowHistory_RendersStructured(t *testing.T) {
	b := startTest(t)

	// 工具结果构造 20 行（超过 toolResultBox 的 15 行截断阈值）。
	longResult := ""
	for i := 0; i < 20; i++ {
		longResult += fmt.Sprintf("第 %d 行内容\n", i)
	}
	events := []memory.Event{
		{Type: memory.EventUser, Content: "你好"},
		{Type: memory.EventAssistant, Content: "第一行\n第二行\n第三行"},
		{Type: memory.EventToolUse, ToolCalls: []memory.ToolCallEvent{{ID: "1", Name: "file", Arguments: `{"action":"read"}`}}},
		{Type: memory.EventToolResult, ToolName: "file", ToolResult: longResult},
	}
	b.ShowHistory(events)

	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	joined := ""
	for _, l := range b.chat.lines {
		joined += l.text + "\n"
	}
	// 助手回答完整显示（多行，非仅首行）。
	for _, want := range []string{"你好", "第一行", "第二行", "file"} {
		if !strings.Contains(joined, want) {
			t.Errorf("历史对话区应完整含 %q，实际:\n%s", want, joined)
		}
	}
	// 工具结果截断：前 15 行在内，第 19 行被截断，显示截断标记。
	if !strings.Contains(joined, "第 0 行内容") {
		t.Errorf("工具结果前 15 行应显示，实际:\n%s", joined)
	}
	if strings.Contains(joined, "第 19 行内容") {
		t.Errorf("工具结果第 19 行应被截断（不该显示），实际:\n%s", joined)
	}
	if !strings.Contains(joined, "已截断") {
		t.Errorf("工具结果应显示截断标记，实际:\n%s", joined)
	}
}
