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
// 样式定义（bubble.go 追加式样式迁移：全 tea 渲染下由 ChatModel/box.go 使用）
// ──────────────────────────────────────────────────────────

var (
	// styleUserPrefix 用户消息前缀样式：青色加粗（"> "）。
	styleUserPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	// styleThink 思考/流式状态样式：灰色斜体。
	styleThink = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	// styleError 错误消息样式：红色加粗。
	styleError = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	// styleMuted 次要文本样式：灰色（token 统计、参数、助手前缀等）。
	styleMuted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	// styleSuccess 成功消息样式：绿色（工具执行成功标题）。
	styleSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	// styleToolPrefix 工具框线样式：黄色加粗（框线竖线，box.go 使用）。
	styleToolPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
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
	// content 是 Markdown 格式的最终回答，token 字段来自 API usage 精确值
	// （totalTokens 为 0 表示 API 未返回 usage，不展示统计行）。
	chatFinalMsg struct {
		content                   string
		inputTokens, outputTokens int
		totalTokens               int
	}
	// chatToolCallMsg 工具调用请求（OnToolCall）。
	chatToolCallMsg struct{ name, args string }
	// chatToolResultMsg 工具执行结果（OnToolResult）。
	chatToolResultMsg struct {
		name, result string
		isError      bool
	}
	// chatContinueMsg 继续推理提示（OnContinue）。
	chatContinueMsg struct{ iteration int }
	// chatErrorMsg 错误信息（OnError）。
	chatErrorMsg struct{ err error }
	// chatMessageMsg 一般性消息（OnMessage）。
	chatMessageMsg struct{ content string }
	// chatWelcomeMsg 欢迎界面（Welcome）。独立类型：追加后滚到顶部展示
	// 完整 banner（logo 在首屏顶部），而非滚到底部（chatMessageMsg 行为）。
	chatWelcomeMsg struct{ content string }
	// chatBalanceMsg 账户余额（ShowBalance）。
	chatBalanceMsg struct{ balance string }
)

// chatLine 对话区的一行（渲染后文本）。
// text 是 lipgloss/glamour 渲染后的行文本，可能含换行（框线文本、Markdown 渲染）。
// streaming 标记该行是否仍在累积流式增量（OnDelta 合并到 streaming 行的末尾）。
type chatLine struct {
	text      string
	streaming bool
}

// ChatModel 全 tea 聊天界面模型。
//
// 结构（参照 j178/chatgpt ui.go）：
//   - viewport 对话区：事件驱动追加 chatLine，scrollBottom 刷新 + 滚到底
//   - textarea 输入区：Enter 提交（Alt+Enter 留待多行换行）
//   - renderFooter 底部状态栏：占位提示（spinner/错误状态后续细化）
//
// 消息流：BubbleUI 事件方法 → Program.Send(msg) → Update 收到 → append line → 刷新。
// 用户输入：Enter → submitCh（Runner 从 channel 读，不直接持有 ChatModel）。
type ChatModel struct {
	width  int
	height int

	viewport viewport.Model        // 对话区（滚动）
	textarea textarea.Model        // 输入框（单行）
	renderer *glamour.TermRenderer // markdown 渲染

	lines []chatLine // 对话区累积行（事件驱动追加）

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

// Update 处理消息。签名满足 tea.Model 接口（返回 tea.Model）。
func (m *ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		m.appendStreaming(v.content)
	case chatFinalMsg:
		// 最终回答：渲染 Markdown 追加对话区。
		// 无工具调用的直接回答：流式增量已逐 token 累积到最后一条流式行
		// （appendStreaming 合并），用渲染后的完整回答原地替换该行——
		// 增量内容与最终回答同源，否则同一答案会在对话区显示两遍。
		if len(m.lines) > 0 && m.lines[len(m.lines)-1].streaming {
			last := &m.lines[len(m.lines)-1]
			if v.content != "" {
				last.text = m.renderAssistant() + finalAnswerText(m, v.content)
			}
			last.streaming = false
		} else {
			// 无进行中的流式行（工具调用/空增量场景）：直接追加渲染版。
			m.closeStreaming()
			if v.content != "" {
				rendered := finalAnswerText(m, v.content)
				m.lines = append(m.lines, chatLine{text: m.renderAssistant() + rendered})
			}
		}
		m.scrollBottom()
		// 本轮 token 统计（精确值，来自 API usage；totalTokens 为 0 时跳过，
		// 避免误导——API 未返回 usage）。
		if v.totalTokens > 0 {
			line := fmt.Sprintf(
				"  ⚡ 本轮 %d tokens（输入 %d / 输出 %d）",
				v.totalTokens, v.inputTokens, v.outputTokens,
			)
			m.lines = append(m.lines, chatLine{text: styleMuted.Render(line)})
			m.scrollBottom()
		}
	case chatToolCallMsg:
		m.closeStreaming()
		// 复用 box.go 的 toolCallBox 生成框线文本（尾 \n 由 multi-line 渲染处理）。
		m.lines = append(m.lines, chatLine{text: toolCallBox(v.name, v.args)})
		m.scrollBottom()
	case chatToolResultMsg:
		m.closeStreaming()
		// 复用 box.go 的 toolResultBox 生成框线文本（>15 行截断 + 成功/失败标题）。
		m.lines = append(m.lines, chatLine{text: toolResultBox(v.name, v.result, v.isError)})
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
	case chatWelcomeMsg:
		// 欢迎界面：追加后滚到顶部（logo 在首屏顶部），而非滚到底部。
		// 启动时 viewport 高度可能尚未由 WindowSizeMsg 校准（默认 10 行），
		// GotoBottom 会把超出的顶部 logo 滚出视口——这是「要上滑才能看见」的根因。
		m.lines = append(m.lines, chatLine{text: v.content})
		m.refresh()
		m.viewport.GotoTop()
	case chatBalanceMsg:
		m.lines = append(m.lines, chatLine{text: styleMuted.Render(v.balance)})
		m.scrollBottom()
	}

	return m, tea.Batch(cmds...)
}

// renderUser 渲染用户消息。
func (m *ChatModel) renderUser(content string) string {
	return styleUserPrefix.Render("> ") + content
}

// renderAssistant 渲染助手消息前缀。
func (m *ChatModel) renderAssistant() string {
	return styleMuted.Render("🧑 助手") + " "
}

// appendStreaming 追加流式增量文本（OnDelta）。
// 增量合并到对话区最后一条助手消息的流式行上（不逐行 append），
// 保证思考文本在一条 chatLine 内连续累积，渲染稳定。
func (m *ChatModel) appendStreaming(content string) {
	if len(m.lines) > 0 {
		last := &m.lines[len(m.lines)-1]
		if last.streaming {
			last.text += content
			m.scrollBottom()
			return
		}
	}
	// 无进行中的流式行：新开一条（助手消息首行）。
	m.lines = append(m.lines, chatLine{text: content, streaming: true})
	m.scrollBottom()
}

// closeStreaming 关闭进行中的流式行（chatToolCall/chatToolResult 前调用）。
// 流式行仍是对话区的一部分（内容保留），只是停止累积增量。
// 注意：chatFinalMsg 不再走此路径——它用渲染后的最终回答原地替换流式行（防双份显示）。
func (m *ChatModel) closeStreaming() {
	if len(m.lines) > 0 {
		m.lines[len(m.lines)-1].streaming = false
	}
}

// scrollBottom 刷新 viewport 内容并滚到底部。
func (m *ChatModel) scrollBottom() {
	m.refresh()
	m.viewport.GotoBottom()
}

// refresh 从 lines 重新渲染 viewport 内容。
// 每条 chatLine 独立追加，行内换行（框线/Markdown）保留；
// 最后一条流式行末尾追加 "▌" 光标标记，指示输出进行中。
func (m *ChatModel) refresh() {
	var sb strings.Builder
	for i, l := range m.lines {
		text := l.text
		// 流式行（正在累积增量）末尾追加光标标记；最后一行才标记，
		// 避免中间行误带标记。
		if i == len(m.lines)-1 && l.streaming && !strings.HasSuffix(text, "▌") {
			text += "▌"
		}
		sb.WriteString(text)
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
