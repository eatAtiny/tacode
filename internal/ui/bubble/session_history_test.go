package bubble

// /list → 选中 → 切换 → 历史加载 的端到端回归测试。
//
// 回归背景（2026-08-27 验收发现）：ChatModel 选择器分支曾把 SessionPickerModel
// 返回的 tea.Quit 传播出去（picker 在 Enter/Esc 上返回 Quit——那是它独立运行时的
// 退出信号），导致 /list 选中瞬间整个聊天程序退出、切换后的历史加载消息全部丢失。
// 本测试复刻 runner 的完整编排（真 tea Program），钉住「选中后程序存活且历史上屏」。

import (
	"strings"
	"testing"
	"time"

	"agentic/internal/memory"
	"agentic/internal/session"

	tea "github.com/charmbracelet/bubbletea"
)

// 全链路：textarea 提交 /list → RunSessionPicker 阻塞 → 用户 Enter 选中 →
// runner 发 OnMessage（已切换）+ ShowHistory（历史事件）→ 转录应包含全部内容。
func TestListSwitchLoadsHistory(t *testing.T) {
	b := startTest(t)

	runnerDone := make(chan struct{})
	// runner 侧：主循环读输入 → /list → RunSessionPicker（阻塞）→ 选中后
	// 按 handleListCommand 顺序发 OnMessage + ShowHistory。
	go func() {
		defer close(runnerDone)
		input, ok := <-b.ReadInputChan()
		if !ok || input != "/list" {
			t.Errorf("input = %q, want /list", input)
			return
		}
		selected, err := b.RunSessionPicker(
			[]session.SessionMeta{{ID: "s1", Name: "历史会话"}}, "s1")
		if err != nil {
			t.Errorf("picker err: %v", err)
			return
		}
		if selected != "s1" {
			t.Errorf("selected = %q, want s1", selected)
			return
		}
		b.OnMessage("✅ 已切换到会话: 历史会话")
		b.ShowHistory([]memory.Event{
			{Type: memory.EventUser, Content: "历史用户消息"},
			{Type: memory.EventAssistant, Content: "历史助手回答"},
		})
	}()

	// 用户侧：键入 /list 回车提交（全程经 Program.Send，不直接触碰模型状态）。
	b.program.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/list")})
	b.program.Send(tea.KeyMsg{Type: tea.KeyEnter})
	sleepMs(200) // 等 chatPickerMsg 处理、进入选择模式

	// 用户在选择器上按 Enter（默认光标在 activeID 项上）。
	b.program.Send(tea.KeyMsg{Type: tea.KeyEnter})

	select {
	case <-runnerDone:
	case <-time.After(3 * time.Second):
		t.Fatal("runner 模拟 goroutine 未完成（选择器结果未回传？）")
	}
	sleepMs(100) // 等 OnMessage/chatHistoryMsg 排队处理完
	b.Close()

	// 切换会话的设计语义：chatHistoryMsg 清空转录再载入历史（m.lines 是当前
	// 会话的转录 seam）。更早的 "/list" 用户行与「已切换」提示已各自 tea.Println
	// 进真实终端 scrollback（打印不受转录重置影响），m.lines 只保留历史。
	all := ""
	for _, l := range b.chat.lines {
		all += l.text + "\n"
	}
	t.Logf("转录共 %d 行:\n%s", len(b.chat.lines), all)
	if len(b.chat.lines) != 2 {
		t.Errorf("转录应恰为 2 条历史（清空重建），实际 %d 条:\n%s", len(b.chat.lines), all)
	}
	if !strings.Contains(all, "历史用户消息") {
		t.Error("缺少历史用户消息（ShowHistory 未生效——选择器 Quit 传播回归？）")
	}
	if !strings.Contains(all, "历史助手回答") {
		t.Error("缺少历史助手回答（ShowHistory 未生效——选择器 Quit 传播回归？）")
	}
}
