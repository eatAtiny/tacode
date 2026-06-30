package ui

import (
	"fmt"

	"agentic/internal/ui/components"

	tea "github.com/charmbracelet/bubbletea"
)

// ──────────────────────────────────────────────────────────
// Bubble Tea 消息类型
// ──────────────────────────────────────────────────────────

type deltaMsg struct{ content string }
type toolCallMsg struct{ name, args string }
type toolResultMsg struct {
	name    string
	result  string
	isError bool
}
type thinkMsg struct{ iteration int }
type continueMsg struct{ iteration int }
type finalMsg struct{ answer string }
type errorMsg struct{ err error }
type waitInputMsg struct{}
type inputDoneMsg struct{ input string }
type confirmMsg struct {
	tool string
	args string
}
type confirmDoneMsg struct{ approved bool }

// ──────────────────────────────────────────────────────────
// BubbleUI 核心模型
// ──────────────────────────────────────────────────────────

// BubbleUI 实现 UI 接口，使用 Bubble Tea 构建 TUI。
type BubbleUI struct {
	program *tea.Program

	// 子组件
	conversation *components.ConversationModel
	input        components.InputModel
	status       components.StatusModel
	toolView     *components.ToolViewModel

	// 状态
	waitingInput bool
	confirming   bool
	confirmCh    chan bool
	inputCh      chan string

	// 元数据
	sessionName string
	model       string
	round       int

	// token 统计（本轮）
	inputTokens  int
	outputTokens int
}

// NewBubbleUI 创建 BubbleUI 实例。
func NewBubbleUI() *BubbleUI {
	b := &BubbleUI{
		conversation: components.NewConversationModel(),
		input:        components.NewInputModel(),
		status:       components.NewStatusModel(),
		toolView:     components.NewToolViewModel(),
		confirmCh:    make(chan bool, 1),
		inputCh:      make(chan string, 1),
	}
	b.program = tea.NewProgram(b, tea.WithAltScreen())
	return b
}

// Init 实现 tea.Model 接口。
func (b *BubbleUI) Init() tea.Cmd {
	return tea.Batch(
		b.input.Init(),
		b.status.Init(),
	)
}

// Update 实现 tea.Model 接口。
func (b *BubbleUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch m := msg.(type) {
	case tea.KeyMsg:
		// 全局按键处理
		switch m.String() {
		case "ctrl+c":
			if b.confirming {
				// 确认中按 Ctrl+C = 拒绝
				b.confirmCh <- false
				b.confirming = false
				return b, nil
			}
			return b, tea.Quit
		case "esc":
			if b.toolView.IsOpen() {
				b.toolView.Close()
				return b, nil
			}
			if b.confirming {
				b.confirmCh <- false
				b.confirming = false
				return b, nil
			}
		}

		// 确认弹窗优先处理
		if b.confirming {
			return b.updateConfirm(m)
		}

		// 工具弹窗获得焦点时处理
		if b.toolView.IsOpen() {
			b.toolView, _ = b.toolView.Update(m)
			return b, nil
		}

		// 输入模式
		if b.waitingInput {
			return b.updateInput(m)
		}

	// UI 接口桥接消息
	case waitInputMsg:
		b.waitingInput = true
		b.input.Focus()
		return b, nil

	case inputDoneMsg:
		b.waitingInput = false
		b.inputCh <- m.input
		return b, nil

	case thinkMsg:
		b.conversation.AddThink(m.iteration)
		return b, nil

	case deltaMsg:
		b.conversation.AddDelta(m.content)
		return b, nil

	case toolCallMsg:
		b.toolView.AddTool(m.name, m.args)
		if !b.toolView.IsOpen() {
			b.toolView.Open()
		}
		return b, nil

	case toolResultMsg:
		b.toolView.SetResult(m.name, m.result, m.isError)
		return b, nil

	case confirmMsg:
		b.confirming = true
		b.toolView.SetConfirming(m.tool, m.args)
		return b, nil

	case confirmDoneMsg:
		b.confirming = false
		b.confirmCh <- m.approved
		return b, nil

	case continueMsg:
		b.conversation.AddContinue(m.iteration)
		b.inputTokens = 0
		b.outputTokens = 0
		return b, nil

	case finalMsg:
		b.conversation.AddFinal(m.answer)
		b.toolView.Close()
		return b, nil

	case errorMsg:
		b.conversation.AddError(m.err)
		return b, nil

	case tea.WindowSizeMsg:
		b.status.SetWidth(m.Width)
		b.conversation.SetSize(m.Width, m.Height-4) // 减去状态栏和输入栏高度
		b.input.SetWidth(m.Width)
		b.toolView.SetSize(m.Width, m.Height)
		return b, nil
	}

	// 子组件更新
	b.input, _ = b.input.Update(msg)
	b.status, _ = b.status.Update(msg)

	return b, tea.Batch(cmds...)
}

// View 实现 tea.Model 接口。
func (b *BubbleUI) View() string {
	// 状态栏
	statusBar := b.status.View()

	// 对话区
	conversationView := b.conversation.View()

	// 工具弹窗（覆盖在对话区上方）
	if b.toolView.IsOpen() {
		toolPopup := b.toolView.View()
		conversationView = toolPopup
	}

	// 输入栏
	inputView := b.input.View()

	return fmt.Sprintf("%s\n%s\n%s", statusBar, conversationView, inputView)
}

// ──────────────────────────────────────────────────────────
// UI 接口实现
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) ReadInput() (string, error) {
	b.program.Send(waitInputMsg{})
	input := <-b.inputCh
	if input == "" {
		return "", fmt.Errorf("empty input")
	}
	return input, nil
}

func (b *BubbleUI) OnThink(iteration int) {
	b.program.Send(thinkMsg{iteration})
}

func (b *BubbleUI) OnDelta(content string) {
	b.program.Send(deltaMsg{content})
}

func (b *BubbleUI) OnToolCall(name, args string) {
	b.program.Send(toolCallMsg{name, args})
}

func (b *BubbleUI) OnToolResult(name, result string, isError bool) {
	b.program.Send(toolResultMsg{name, result, isError})
}

func (b *BubbleUI) OnContinue(iteration int) {
	b.program.Send(continueMsg{iteration})
}

func (b *BubbleUI) OnFinal(answer string) {
	b.program.Send(finalMsg{answer})
}

func (b *BubbleUI) OnError(err error) {
	b.program.Send(errorMsg{err})
}

func (b *BubbleUI) ConfirmPermission(tool, args string) (bool, error) {
	b.program.Send(confirmMsg{tool, args})
	approved := <-b.confirmCh
	return approved, nil
}

func (b *BubbleUI) Close() error {
	b.program.Quit()
	return nil
}

// ──────────────────────────────────────────────────────────
// 内部按键处理
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) updateInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		input := b.input.Value()
		b.input.SetValue("")
		b.program.Send(inputDoneMsg{input})
		return b, nil
	case "up":
		// TODO: 历史上翻
	case "down":
		// TODO: 历史下翻
	}
	b.input, _ = b.input.Update(msg)
	return b, nil
}

func (b *BubbleUI) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "tab":
		b.toolView.ConfirmToggle()
	case "right":
		b.toolView.ConfirmToggle()
	case "enter":
		approved := b.toolView.ConfirmSelection()
		b.program.Send(confirmDoneMsg{approved})
	}
	return b, nil
}

// ──────────────────────────────────────────────────────────
// 元数据设置
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) SetSessionName(name string) {
	b.sessionName = name
	b.status.SetSession(name)
}

func (b *BubbleUI) SetModel(model string) {
	b.model = model
	b.status.SetModel(model)
}

func (b *BubbleUI) UpdateTokens(input, output int) {
	b.inputTokens += input
	b.outputTokens += output
	b.status.SetTokens(b.inputTokens, b.outputTokens)
}

// ResetTokens 重置本轮 token 计数并更新状态栏显示。
func (b *BubbleUI) ResetTokens() {
	b.inputTokens = 0
	b.outputTokens = 0
	b.status.SetTokens(0, 0)
}
