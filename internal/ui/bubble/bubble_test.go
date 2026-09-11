package bubble

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

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

// 权限确认：提示进对话区 + 输入从 inputForward 读取（查询运行中链路）。
// 注：textarea 提交 → submitCh → Runner → inputForward 的转发由 Runner 主循环
// 负责（已有 TestReadInputChan_BridgesSubmitCh 覆盖桥接），此处直接写入
// inputForward 模拟已转发的确认输入；提示经 Program.Send 进对话区。
func TestConfirmPermission_InputForward(t *testing.T) {
	b := startTest(t)

	// 模拟查询运行中：Runner 主循环已把 textarea 提交转发到 inputForward。
	inputForward := make(chan string, 1)
	go func() {
		inputForward <- "y"
	}()

	approved, err := b.ConfirmPermission("shell", `{"command":"rm -rf /"}`, "高风险操作", inputForward)
	if err != nil {
		t.Fatalf("ConfirmPermission error: %v", err)
	}
	if !approved {
		t.Error("输入 y 应允许")
	}

	// 提示应显示为弹层（Close 同步等待事件循环处理完消息后读取，避免竞争）。
	if err := b.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	// 确认完成后弹层应清除；确认前应显示（此处已完成，验证流程无 panic）。
	// 弹层内容由 View 渲染，直接验证 renderPermissionLayer 输出。
	m := NewChatModel()
	m.permLayer = &permissionLayer{tool: "shell", args: `{"command":"rm -rf /"}`, reason: "高风险操作"}
	layer := m.renderPermissionLayer()
	for _, want := range []string{"权限确认", "shell", "rm -rf", "高风险操作", "y 允许"} {
		if !strings.Contains(layer, want) {
			t.Errorf("权限弹层应含 %q，实际:\n%s", want, layer)
		}
	}
}

// 权限确认：输入 n / 其他内容应拒绝。
func TestConfirmPermission_Reject(t *testing.T) {
	b := NewBubbleUI()
	if err := b.Start(testStartOpts()...); err != nil {
		t.Fatalf("Start error: %v", err)
	}
	defer b.Close()

	inputForward := make(chan string, 1)
	go func() {
		inputForward <- "n"
	}()

	approved, err := b.ConfirmPermission("shell", `{"command":"rm -rf /"}`, "", inputForward)
	if err != nil {
		t.Fatalf("ConfirmPermission error: %v", err)
	}
	if approved {
		t.Error("输入 n 应拒绝")
	}
}

// 权限确认：inputForward 为 nil 时回退 ReadInputChan（textarea 提交 channel）。
// 不 Start（无 tea 程序，send 丢弃消息，直接 Update ChatModel 无并发写者）。
func TestConfirmPermission_FallbackReadInputChan(t *testing.T) {
	b := NewBubbleUI()

	go func() {
		b.chat.textarea.SetValue("yes")
		b.chat.Update(tea.KeyMsg{Type: tea.KeyEnter})
	}()

	approved, err := b.ConfirmPermission("shell", `{"command":"ls"}`, "", nil)
	if err != nil {
		t.Fatalf("ConfirmPermission error: %v", err)
	}
	if !approved {
		t.Error("输入 yes 应允许")
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
