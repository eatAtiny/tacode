// Package bubble 提供基于 Lip Gloss + Glamour 的终端 UI 实现。
//
// chat.go 是全 tea 渲染聊天界面（ChatModel）：
//   - viewport.Model 对话区（滚动）+ textarea.Model 输入框 + footer 状态栏
//   - 全部经 View() 渲染，输出不写 os.Stdout（纯 tea，替代追加式主渲染）
//   - 事件驱动：BubbleUI 事件方法（OnThink/OnDelta/...）经 Program.Send 投递
//     消息，ChatModel.Update 收到后追加 chatLine 并刷新 viewport
//   - 用户提交经 submitCh 桥接给 Runner（BubbleUI → Runner）
//
// 参考实现：j178/chatgpt 的 ui.go（viewport+textarea+footer 布局、流式增量更新、
// WindowSizeMsg 布局计算）。注意 chat.go 接的是 agent 事件流而非 chatgpt 库。
package bubble

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// ──────────────────────────────────────────────────────────
// 聊天 TUI 消息类型（对应 UI 事件，由 BubbleUI 事件方法经 Program.Send 投递）
// ──────────────────────────────────────────────────────────

type (
	// chatThinkMsg 新一轮思考开始（OnThink）。
	chatThinkMsg struct{ iteration int }
	// chatDeltaMsg 流式增量文本（OnDelta）。
	chatDeltaMsg struct{ content string }
	// chatFinalMsg 最终回答（OnFinal）。
	chatFinalMsg struct{ content string }
	// chatToolCallMsg 工具调用请求（OnToolCall）。
	chatToolCallMsg struct{ name, args string }
	// chatToolResultMsg 工具执行结果（OnToolResult）。
	chatToolResultMsg struct{ name, result string; isError bool }
	// chatContinueMsg 继续推理提示（OnContinue）。
	chatContinueMsg struct{ iteration int }
	// chatErrorMsg 错误信息（OnError）。
	chatErrorMsg struct{ err error }
	// chatMessageMsg 一般性消息（OnMessage）。
	chatMessageMsg struct{ content string }
	// chatBalanceMsg 账户余额（ShowBalance）。
	chatBalanceMsg struct{ balance string }
	// chatSubmitMsg 用户提交输入（textarea Enter）。
	// Task 1 骨架阶段仅定义占位，Task 2 接 BubbleUI 时使用。
	chatSubmitMsg struct{ value string }
)

// chatLine 对话区的一行（渲染后文本）。
// text 是 lipgloss/glamour 渲染后的行文本。
type chatLine struct {
	text string
}

// ChatModel 全 tea 聊天界面模型。
//
// 结构（参照 j178/chatgpt ui.go）：
//   - viewport 对话区：事件驱动追加 chatLine，scrollBottom 刷新 + 滚到底
//   - textarea 输入区：Enter 提交（Alt+Enter 留待 Task 2 做多行换行）
//   - renderFooter 底部状态栏：占位提示（spinner/错误状态 Task 2 细化）
//
// 消息流：BubbleUI 事件方法 → Program.Send(msg) → Update 收到 → append line → 刷新。
// 用户输入：Enter → submitCh（Runner 从 channel 读，不直接持有 ChatModel）。
type ChatModel struct {
	width  int
	height int

	viewport viewport.Model   // 对话区（滚动）
	textarea textarea.Model   // 输入框（单行）
	renderer *glamour.TermRenderer // markdown 渲染

	lines []chatLine // 对话区累积行（事件驱动追加）
	user  string     // 当前用户输入（提交时显示）

	submitCh chan string // 用户提交桥接（BubbleUI → Runner）
}

// NewChatModel 创建聊天模型。
func NewChatModel() *ChatModel {
	ta := textarea.New()
	ta.Placeholder = "输入消息，Enter 发送"
	ta.Focus()
	ta.Prompt = "> "
	ta.CharLimit = -1
	ta.SetWidth(50)
	ta.SetHeight(1)
	ta.ShowLineNumbers = false

	vp := viewport.New(50, 10)
	renderer, _ := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(100))

	return &ChatModel{
		textarea: ta,
		viewport: vp,
		renderer: renderer,
		submitCh: make(chan string, 8),
	}
}

// SubmitCh 返回用户提交 channel（Runner 从此读输入）。
func (m *ChatModel) SubmitCh() <-chan string { return m.submitCh }

// Init 初始命令：进入 alt screen（全屏渲染），并启动光标闪烁。
func (m *ChatModel) Init() tea.Cmd {
	return tea.Batch(tea.EnterAltScreen, textarea.Blink)
}

// Update 处理消息。
func (m *ChatModel) Update(msg tea.Msg) (*ChatModel, tea.Cmd) {
	var cmds []tea.Cmd

	// textarea 按键：Enter 提交（Alt+Enter 不拦截，留给多行场景）。
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.Type == tea.KeyEnter && !keyMsg.Alt {
			value := strings.TrimSpace(m.textarea.Value())
			if value != "" {
				m.submitCh <- value
				m.textarea.Reset()
				// 用户消息进对话区。
				m.lines = append(m.lines, chatLine{text: m.renderUser(value)})
				m.scrollBottom()
			}
			// 消费掉 Enter，避免 textarea 内插入换行。
			return m, tea.Batch(cmds...)
		}
	}

	// 更新子组件（textarea/viewport 各自处理鼠标、滚动等消息）。
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.textarea.SetWidth(v.Width)
		m.viewport.Width = v.Width
		// 2 = footer 预留高度（文本区 + footer 高度之和）。
		m.viewport.Height = v.Height - m.textarea.Height() - 2
		m.refresh()
	case chatThinkMsg:
		m.lines = append(m.lines, chatLine{text: styleThink.Render("⏳ 思考中...")})
		m.scrollBottom()
	case chatDeltaMsg:
		// TODO(Task 2/3): 流式增量应合并到最后一行（去掉末尾 "▌" 光标标记后拼接），
		// 而非每行 append。骨架阶段先追加一行占位，跑通「事件 → lines → 刷新」链路。
		m.lines = append(m.lines, chatLine{text: v.content})
		m.scrollBottom()
	case chatFinalMsg:
		rendered, err := m.renderer.Render(v.content)
		if err != nil {
			rendered = v.content
		}
		m.lines = append(m.lines, chatLine{text: strings.TrimRight(rendered, "\n")})
		m.scrollBottom()
	case chatToolCallMsg:
		m.lines = append(m.lines, chatLine{text: styleToolPrefix.Render(fmt.Sprintf("🔧 %s", v.name))})
		m.scrollBottom()
	case chatToolResultMsg:
		style := styleSuccess
		mark := "✅ 结果"
		if v.isError {
			style = styleError
			mark = "❌ 错误"
		}
		m.lines = append(m.lines, chatLine{text: style.Render(fmt.Sprintf("%s: %s", mark, v.name))})
		m.scrollBottom()
	case chatContinueMsg:
		m.lines = append(m.lines, chatLine{text: styleThink.Render(fmt.Sprintf("🔄 继续推理 (iter %d)", v.iteration))})
		m.scrollBottom()
	case chatErrorMsg:
		m.lines = append(m.lines, chatLine{text: styleError.Render(fmt.Sprintf("❌ Error: %v", v.err))})
		m.scrollBottom()
	case chatMessageMsg:
		m.lines = append(m.lines, chatLine{text: v.content})
		m.scrollBottom()
	case chatBalanceMsg:
		m.lines = append(m.lines, chatLine{text: styleMuted.Render(v.balance)})
		m.scrollBottom()
	}

	return m, tea.Batch(cmds...)
}

// renderUser 渲染用户消息。
func (m *ChatModel) renderUser(content string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5")).Render("你: ") + content
}

// scrollBottom 刷新 viewport 内容并滚到底部。
func (m *ChatModel) scrollBottom() {
	m.refresh()
	m.viewport.GotoBottom()
}

// refresh 从 lines 重新渲染 viewport 内容。
func (m *ChatModel) refresh() {
	var sb strings.Builder
	for _, l := range m.lines {
		sb.WriteString(l.text)
		sb.WriteString("\n")
	}
	m.viewport.SetContent(sb.String())
}

// View 渲染整屏（viewport 对话区 + textarea 输入 + footer 状态栏）。
func (m *ChatModel) View() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.viewport.View(),
		m.textarea.View(),
		m.renderFooter(),
	)
}

// renderFooter 底部状态栏。
func (m *ChatModel) renderFooter() string {
	return lipgloss.NewStyle().Height(1).Faint(true).Render("agentic │ ctrl+c 退出")
}
