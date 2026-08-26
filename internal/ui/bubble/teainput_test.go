package bubble

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// teaUI 模型的 View 应包含输入框（> 提示符 + 占位符）和状态栏。
func TestTeaUIView_ContainsInputAndStatus(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("deepseek-v4-flash")
	b.UpdateTokens(120, 45)

	m := b.teaModel()
	v := m.View()
	for _, want := range []string{">", "输入任务开始对话", "session: demo", "↑ 120"} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少 %q，实际:\n%s", want, v)
		}
	}
}

// teaUI 的 View 应包含分隔线（对话区与底部固定区之间的视觉分隔）。
func TestTeaUIView_ContainsSeparator(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60
	v := m.View()
	if !strings.Contains(v, strings.Repeat("─", 60)) {
		t.Errorf("View 应包含 60 列分隔线，实际:\n%s", v)
	}
}

// teaAppendMsg 追加语义（流式合并）：不以 \n 结尾的内容是流式增量，合并进当前行；
// 以 \n 结尾的内容是闭合块，若当前行未闭合（流式增量中）则先断开再另起新行。
func TestTeaUI_AppendConversation(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60

	// 闭合块（思考行）：新起一行。
	m2, cmd := m.Update(teaAppendMsg{content: "  ⏳ 思考中...\n"})
	if cmd != nil {
		t.Errorf("teaAppendMsg 不应产生命令，实际: %v", cmd)
	}
	m3, _ := m2.Update(teaAppendMsg{content: "你好"}) // 流式增量
	m4, _ := m3.Update(teaAppendMsg{content: "世界"}) // 流式增量 → 合并

	v := m4.View()
	if !strings.Contains(v, "你好世界") {
		t.Errorf("流式增量应合并为同一行，View 缺少 %q，实际:\n%s", "你好世界", v)
	}
	// 思考行与流式行应分行（闭合块尾部 \n 断开）。
	if strings.Contains(v, "思考中...你好") {
		t.Errorf("闭合块与流式行不应拼在同一行，实际:\n%s", v)
	}
}

// 流式行未闭合时收到闭合块（最终回答/工具框线）：块从新行开始，不与流式 token 拼接。
func TestTeaUI_AppendBlockBreaksStreamingLine(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60

	m2, _ := m.Update(teaAppendMsg{content: "你好"})            // 流式增量（未闭合）
	m3, _ := m2.Update(teaAppendMsg{content: "世界"})           // 流式增量（继续合并）
	m4, _ := m3.Update(teaAppendMsg{content: "  ✅ 思考完成\n"}) // 闭合块到达

	v := m4.View()
	if !strings.Contains(v, "你好世界\n") {
		t.Errorf("闭合块应断开流式行另起，View 实际:\n%s", v)
	}
	if strings.Contains(v, "你好世界  ✅ 思考完成") {
		t.Errorf("闭合块不应与流式行拼在同一行，实际:\n%s", v)
	}
	if !strings.Contains(v, "✅ 思考完成") {
		t.Errorf("闭合块内容应保留，实际:\n%s", v)
	}
}

// 闭合块后追加流式增量：新起一行（不再合并进上一个闭合块）。
func TestTeaUI_AppendDeltaAfterBlockStartsNewLine(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60

	m2, _ := m.Update(teaAppendMsg{content: "  ⏳ 思考中...\n"}) // 闭合块
	m3, _ := m2.Update(teaAppendMsg{content: "增量"})             // 流式增量 → 新行

	v := m3.View()
	if !strings.Contains(v, "思考中...\n增量") {
		t.Errorf("闭合块后的流式增量应新起一行，View 实际:\n%s", v)
	}
}

// 对话区 View 的输出结构：对话 entry 行 + 分隔线 + 状态栏 + 输入框。
func TestTeaUI_ViewStructure(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60
	m2, _ := m.Update(teaAppendMsg{content: "  ⏳ 思考中...\n"})
	m3, _ := m2.Update(teaAppendMsg{content: "回答内容\n"})

	v := m3.View()
	// 闭合块内容按行出现（\n join），分隔线在对话区之后。
	if !strings.Contains(v, "思考中...\n回答内容") {
		t.Errorf("对话区应分行累积，View 实际:\n%s", v)
	}
}

// teaBalanceMsg 消息应更新余额展示。
func TestTeaUI_BalanceMsg(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60
	m2, _ := m.Update(teaBalanceMsg{balance: "💰 ¥110.00"})

	v := m2.View()
	if !strings.Contains(v, "💰 ¥110.00") {
		t.Errorf("View 应包含余额，实际:\n%s", v)
	}
}

// 提交输入：Enter 后发出 UserInputMsg。
func TestTeaUI_SubmitInput(t *testing.T) {
	b := NewBubbleUI()
	m := b.teaModel()

	// 模拟输入 "hello" + Enter。
	_ = m
	// 说明：tea 模型 Update 处理 KeyMsg，需先注入文本再回车。
	// 具体消息流程在实现中定义，测试断言提交后产生输入 channel 消息。
	t.Skip("阶段2 骨架：输入提交的完整链路在 Task 6 接线后测试")
}

// submitInput 写入 tea 专用 channel（不经过 raw 路径的 inputChan），
// 且永不 close（无 send-vs-close 竞态、无 panic）。
func TestTeaUI_SubmitInputBridge(t *testing.T) {
	b := NewBubbleUI()

	// 写入 3 条（缓冲 1，超出丢弃），模拟 Runner 未及时消费时不阻塞。
	b.submitInput("hello")
	b.submitInput("second")
	b.submitInput("third")

	// 第一条应可读出（未被丢弃）。
	ch := b.ReadTeaInputChan()
	select {
	case got := <-ch:
		if got != "hello" {
			t.Errorf("tea 输入 = %q, want %q", got, "hello")
		}
	default:
		t.Error("tea 输入 channel 应含第一条提交")
	}

	// raw 路径的 inputChan 不应被 tea 输入启动/写入。
	if b.inputChan != nil {
		t.Errorf("submitInput 不应初始化 raw inputChan，实际非 nil")
	}
}

// Start 后 tea 程序启动，事件投递不阻塞（tea 未完全就绪时 Send 也安全）。
// 测试环境非 TTY：Start 用 WithInput(nil)+WithoutRenderer()，tea.Run 不依赖
// /dev/tty 与渲染器，事件循环正常运转；后续 Quit 优雅退出。
func TestTeaUI_StartAndSend(t *testing.T) {
	b := NewBubbleUI()
	stop, err := b.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer stop()

	// 事件投递（tea 未完全就绪时 Send 也安全——Program.Send 在 ctx 活跃前
	// 写入消息 channel，未阻塞）。
	b.sendToTea(teaAppendMsg{content: "hello"})
	b.ShowBalance("💰 ¥1.00")

	// 双 Start 幂等：再次 Start 返回空 cancel，不重复启动 tea 程序。
	stop2, err2 := b.Start()
	if err2 != nil {
		t.Fatalf("第二次 Start failed: %v", err2)
	}
	stop2()

	// 重复 cancel 安全：stop 已调用后再次调用不 panic。
	stop()
}

// Start 后 IsTeaMode 为 true；cancel 后恢复 false（阶段 1 ANSI 路径恢复）。
func TestTeaUI_StartThenStopMode(t *testing.T) {
	b := NewBubbleUI()
	if b.IsTeaMode() {
		t.Error("未 Start 时不应处于 tea 模式")
	}

	stop, err := b.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if !b.IsTeaMode() {
		t.Error("Start 后应处于 tea 模式")
	}

	// tea 模式下阶段 1 的 ANSI 状态栏方法退位（不输出、不改状态位）。
	b.renderStatusBar()
	if b.statusBarShown {
		t.Error("tea 模式下 renderStatusBar 不应置 statusBarShown")
	}
	b.clearStatusBar()

	// 事件方法在 tea 模式下不 panic（sendToTea 走 Program.Send）。
	b.OnThink(1)
	b.OnDelta("增量")
	b.OnToolCall("shell", `{"command":"ls"}`)
	b.OnToolResult("shell", "file.txt\n", false)
	b.OnContinue(2)
	b.OnFinal("最终回答", 10, 5, 15)
	b.OnMessage("一般消息")
	b.OnError(fmt.Errorf("测试错误")) // 验证错误事件不 panic

	stop()
	if b.IsTeaMode() {
		t.Error("cancel 后不应处于 tea 模式")
	}
}

// tea 模式与阶段 1 模式对同一事件方法的输出一致性：
// OnDelta 在两种模式下都追加内容到对话区（tea 模式经 sendToTea）。
func TestTeaUI_EventForwarding(t *testing.T) {
	b := NewBubbleUI()
	stop, err := b.Start()
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer stop()

	// 事件方法：tea 模式走 sendToTea，不 panic 即通过（内部不可直接断言对话区，
	// 因为 tea 模型在独立 goroutine 中消费消息）。
	b.OnDelta("你好")
	b.OnFinal("回答", 1, 2, 3)
}

// teaUI 的 Update 返回 tea.Model（接口签名），动态类型保持 *teaUI。
func TestTeaUI_ValueReceiver(t *testing.T) {
	b := NewBubbleUI()
	m := b.teaModel()

	// Update 返回 tea.Model 接口，动态类型应为 *teaUI（指针方法集实现接口）。
	m2, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	next, ok := m2.(*teaUI)
	if !ok {
		t.Fatalf("Update 返回的动态类型应为 *teaUI，实际 %T", m2)
	}
	if next.b != b {
		t.Error("Update 返回的模型应仍持有同一 BubbleUI 引用")
	}
	if next.width != 100 || next.height != 30 {
		t.Errorf("WindowSizeMsg 后 width/height = %d/%d, want 100/30", next.width, next.height)
	}
}

// 输入按键经 Update 后应保留在 textinput 中（值类型组件返回值必须写回）。
func TestTeaUI_TypingPersists(t *testing.T) {
	b := NewBubbleUI()
	m := b.teaModel()
	m.width = 80

	// 依次注入 'h' 'i' 两个字符按键，逐个写回模型。
	// 注：textinput 对字符按键会返回光标闪烁命令（textinput.Blink），属正常行为，不在此断言。
	for _, r := range []rune("hi") {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(*teaUI)
	}

	if m.input.Value() != "hi" {
		t.Errorf("textinput 值 = %q, want %q", m.input.Value(), "hi")
	}

	// 回车提交后清空输入框。
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(*teaUI)
	if m.input.Value() != "" {
		t.Errorf("Enter 后 textinput 应清空，实际 %q", m.input.Value())
	}
}
