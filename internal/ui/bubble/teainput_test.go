package bubble

import (
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

// teaAppendMsg 消息应追加到对话区（Update 返回的模型携带累积状态）。
func TestTeaUI_AppendConversation(t *testing.T) {
	b := NewBubbleUI()

	m := b.teaModel()
	m.width = 60
	m2, cmd := m.Update(teaAppendMsg{content: "第一行"})
	if cmd != nil {
		t.Errorf("teaAppendMsg 不应产生命令，实际: %v", cmd)
	}
	m3, _ := m2.Update(teaAppendMsg{content: "第二行"})

	v := m3.View()
	for _, want := range []string{"第一行", "第二行"} {
		if !strings.Contains(v, want) {
			t.Errorf("View 缺少对话行 %q，实际:\n%s", want, v)
		}
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
