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
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"agentic/internal/session"
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
	// chatContextMsg 上下文窗口占用（UpdateContext）。
	chatContextMsg struct{ usedTokens, contextLimit int }
	// chatHistoryMsg 会话历史（ShowHistory，切换会话后加载）。
	chatHistoryMsg struct{ events []chatHistoryEvent }
	// chatHistoryEvent 历史中的一条记录（已渲染文本）。
	chatHistoryEvent struct {
		text      string // 渲染后的对话行（用户消息/助手回答/工具框线）
		streaming bool   // 是否流式（历史加载恒 false）
	}
	// chatPickerMsg 启动会话选择器（/list）。
	chatPickerMsg struct {
		sessions []session.SessionMeta
		activeID string
	}
	// chatPickerResultMsg 选择器结果（选中会话 ID 或空=取消）。
	chatPickerResultMsg struct{ selected string }
	// chatPermissionMsg 权限确认弹层（ConfirmPermission 触发，工具执行前）。
	chatPermissionMsg struct{ tool, args, reason string }
	// chatPermissionDoneMsg 权限确认完成（用户已输入 y/N，清除弹层）。
	chatPermissionDoneMsg struct{}
)

// chatLine 对话区的一行（渲染后文本）。
// text 是 lipgloss/glamour 渲染后的行文本，可能含换行（框线文本、Markdown 渲染）。
// streaming 字段在流式改为「增量缓冲 + 换行切段定稿」后不再写入（保留字段，
// 后续任务处理光标标记时可能复用）。
type chatLine struct {
	text      string
	streaming bool
}

// ChatModel 全 tea 聊天界面模型。
//
// 结构（参照 j178/chatgpt ui.go）：
//   - textarea 输入区：Enter 提交（Alt+Enter 留待多行换行）
//   - renderFooter 底部状态栏：占位提示（spinner/错误状态后续细化）
//
// 消息流：BubbleUI 事件方法 → Program.Send(msg) → Update 收到 → append line。
// 用户输入：Enter → submitCh（Runner 从 channel 读，不直接持有 ChatModel）。
type ChatModel struct {
	width  int
	height int

	textarea textarea.Model        // 输入框（单行）
	renderer *glamour.TermRenderer // markdown 渲染

	lines []chatLine // 对话区累积行（事件驱动追加）

	// streamBuf 未定稿的流式缓冲（无换行的增量累积）。
	streamBuf string
	// streamed 自最近一次 think 起是否有流式内容（final 判断是否重印全文）。
	streamed bool
	// status 活区状态行文本（查询中显示：思考/输出/工具执行；空=不渲染）。
	status string

	submitCh chan string // 用户提交桥接（BubbleUI → Runner）

	// balance 账户余额展示文本（footer 状态栏显示，空=不显示）。
	balance string
	// contextUsedTokens 上下文已用 token 数（footer 显示）。
	contextUsedTokens int
	// contextLimit 模型上下文窗口大小（footer 显示，0=未知）。
	contextLimit int

	// picking 是否处于会话选择模式（/list 时 true，选择器视图渲染）。
	picking bool
	// picker 会话选择器模型（picking 时按键转发给它）。
	picker *session.SessionPickerModel
	// pickerDone 选择器结果回传 channel（BubbleUI 从这读选择结果）。
	pickerDone chan string

	// permLayer 权限确认弹层（非 nil = 有权限确认在等，View 渲染弹层）。
	permLayer *permissionLayer
}

// permissionLayer 权限确认弹层状态。
type permissionLayer struct {
	tool   string // 需要确认的工具名
	args   string // 工具参数
	reason string // 确认原因
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

	renderer, _ := glamour.NewTermRenderer(glamour.WithAutoStyle(), glamour.WithWordWrap(100))

	return &ChatModel{
		textarea:   ta,
		renderer:   renderer,
		submitCh:   make(chan string, 8),
		pickerDone: make(chan string, 1),
	}
}

// SubmitCh 返回用户提交 channel（Runner 从此读输入）。
func (m *ChatModel) SubmitCh() <-chan string { return m.submitCh }

// Init 初始命令：启动光标闪烁。
// 不进 alt screen——inline 渲染，对话定稿后留在终端原生 scrollback。
func (m *ChatModel) Init() tea.Cmd {
	return textarea.Blink
}

// Update 处理消息。签名满足 tea.Model 接口（返回 tea.Model）。
func (m *ChatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// ── 选择器模式（/list 进行中） ──
	// 所有按键转发给 SessionPickerModel，选择器完成（Enter/Esc）时捕获结果。
	if m.picking {
		if keyMsg, ok := msg.(tea.KeyMsg); ok {
			updated, cmd := m.picker.Update(keyMsg)
			cmds = append(cmds, cmd)
			_ = updated
			// 选择完成：捕获结果后切回对话模式（不让外层 tea 退出）。
			if m.picker.IsDone() {
				m.picking = false
				m.pickerDone <- m.picker.Chosen()
				m.picker = nil
			}
			return m, nil
		}
		// 非按键消息（如 WindowSize）不转发，直接忽略。
		return m, nil
	}

	// textarea 按键：Enter 提交（Alt+Enter 不拦截，留给多行场景）。
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		if keyMsg.Type == tea.KeyEnter && !keyMsg.Alt {
			value := strings.TrimSpace(m.textarea.Value())
			if value != "" {
				m.submitCh <- value
				m.textarea.Reset()
				// 新一轮提交：重置状态行（上一轮残留的思考/输出状态清除）。
				m.status = ""
				// 用户消息经 commit 定稿（打印于活区上方入 scrollback）。
				cmds = append(cmds, m.commit(m.renderUser(value)))
			}
			// 消费掉 Enter，避免 textarea 内插入换行。
			return m, tea.Batch(cmds...)
		}
	}
	// 更新子组件（textarea 处理按键、光标等消息）。
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.textarea.SetWidth(v.Width)
	case chatThinkMsg:
		m.streamed = false
		// 思考状态进活区状态行（不再追加转录行）。
		m.status = "⏳ 思考中"
	case chatDeltaMsg:
		m.status = "📝 输出中"
		if segs := m.appendStreaming(v.content); len(segs) > 0 {
			cmds = append(cmds, m.commit(segs...))
		}
	case chatFinalMsg:
		// 冲刷残余段落；有流式内容时 final 不重印全文（增量与最终回答同源）。
		// 整个 case 只产生一次 commit（单次 Println）——残余/全文/token 行
		// 合并为一个打印命令，避免 tea.Batch 内多 Cmd 并发不保序。
		parts := m.flushBuf()
		if !m.streamed && v.content != "" {
			parts = append(parts, m.renderAssistant()+finalAnswerText(m, v.content))
		}
		if v.totalTokens > 0 {
			parts = append(parts, styleMuted.Render(fmt.Sprintf(
				"  ⚡ 本轮 %d tokens（输入 %d / 输出 %d）",
				v.totalTokens, v.inputTokens, v.outputTokens,
			)))
		}
		if len(parts) > 0 {
			cmds = append(cmds, m.commit(parts...))
		}
		m.streamed = false
		m.status = ""
	case chatToolCallMsg:
		// 先冲刷流式残余再接框线（同一次 commit 打印，保序）。
		parts := append(m.flushBuf(), toolCallBox(v.name, v.args))
		cmds = append(cmds, m.commit(parts...))
		m.status = "🔧 执行工具: " + v.name
	case chatToolResultMsg:
		// 先冲刷流式残余再接框线（同一次 commit 打印，保序）。
		parts := append(m.flushBuf(), toolResultBox(v.name, v.result, v.isError))
		cmds = append(cmds, m.commit(parts...))
	case chatContinueMsg:
		// 继续推理提示进活区状态行（不再追加转录行）。
		m.status = fmt.Sprintf("🔄 继续推理 (iter %d)", v.iteration)
	case chatErrorMsg:
		// 错误中断轮：先冲刷流式残余再接错误行（单次 commit 保序），
		// 避免残余泄漏到下一轮（下一轮首 delta 会重复注入助手前缀）。
		parts := append(m.flushBuf(), styleError.Render(fmt.Sprintf("❌ Error: %v", v.err)))
		cmds = append(cmds, m.commit(parts...))
		m.status = ""
	case chatMessageMsg:
		// 同错误路径：先冲刷流式残余再接内容（单次 commit 保序）。
		parts := append(m.flushBuf(), v.content)
		cmds = append(cmds, m.commit(parts...))
		m.status = ""
	case chatWelcomeMsg:
		// 同 error/message 一致模式：先冲刷流式残余再接内容（welcome 当前
		// 仅启动时发、理论无进行中流式，防御性保持冲刷一致）。
		parts := append(m.flushBuf(), v.content)
		cmds = append(cmds, m.commit(parts...))
		m.status = ""
	case chatBalanceMsg:
		// 余额进 footer 状态栏（常驻显示），不再追加对话行。
		m.balance = v.balance
	case chatContextMsg:
		// 上下文占用进 footer（已用/总/百分比）。
		m.contextUsedTokens = v.usedTokens
		m.contextLimit = v.contextLimit
	case chatHistoryMsg:
		// 切换会话后加载历史：先清空对话区，再经 commit 单次打印整段历史
		// （多段合并为一个 tea.Println，保序——tea.Batch 内 Cmd 并发不保序）。
		m.lines = nil
		// 防御性冲刷：历史行接在流式残余之后（正常场景加载历史时无进行中流式）。
		parts := m.flushBuf()
		for _, e := range v.events {
			parts = append(parts, e.text)
		}
		cmds = append(cmds, m.commit(parts...))
	case chatPickerMsg:
		// 启动会话选择器（/list）：创建 picker 模型，进入选择模式。
		m.picker = session.NewSessionPickerModel(v.sessions, v.activeID)
		m.picking = true
	case chatPickerResultMsg:
		// 选择结果（理论不经此消息，结果走 pickerDone channel；保留兜底）。
		m.picking = false
		m.picker = nil
	case chatPermissionMsg:
		// 权限确认弹层：工具执行前显示（覆盖在输入框上方）。
		m.permLayer = &permissionLayer{tool: v.tool, args: v.args, reason: v.reason}
	case chatPermissionDoneMsg:
		// 权限确认完成：清除弹层。
		m.permLayer = nil
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

// appendStreaming 追加流式增量，返回可定稿段落（遇 \n 切段，保序）。
// 本轮首个增量前注入助手前缀（进 streamBuf，随首次冲刷一起定稿）。
func (m *ChatModel) appendStreaming(content string) []string {
	if !m.streamed {
		m.streamBuf += m.renderAssistant()
	}
	m.streamBuf += content
	m.streamed = true
	var segs []string
	for {
		i := strings.IndexByte(m.streamBuf, '\n')
		if i < 0 {
			break
		}
		segs = append(segs, m.streamBuf[:i])
		m.streamBuf = m.streamBuf[i+1:]
	}
	return segs
}

// flushBuf 取出残余缓冲（无残余返回 nil）。
func (m *ChatModel) flushBuf() []string {
	if m.streamBuf == "" {
		return nil
	}
	s := m.streamBuf
	m.streamBuf = ""
	return []string{s}
}

// commit 定稿若干段：逐段记入转录 m.lines，并返回单次 tea.Println
// （多段合并为一个打印命令——tea.Batch 内的 Cmd 并发执行不保序，
// 单次 Println 用 \n 连接保证段落顺序）。
// 空串段照记（保留段落间空行语义）；整次调用无段时返回 nil。
func (m *ChatModel) commit(parts ...string) tea.Cmd {
	if len(parts) == 0 {
		return nil
	}
	for _, p := range parts {
		m.lines = append(m.lines, chatLine{text: p})
	}
	return tea.Println(strings.Join(parts, "\n"))
}

// View 渲染底部活区。
// 选择器模式（picking）时渲染会话选择器；查询中（status 非空）顶部渲染状态行；
// 权限确认（permLayer）时在输入框上方渲染弹层；否则渲染输入框 + footer
// （对话内容不在 View 内）。
func (m *ChatModel) View() string {
	if m.picking && m.picker != nil {
		return lipgloss.JoinVertical(lipgloss.Left,
			m.picker.View(),
			lipgloss.NewStyle().Height(1).Faint(true).Render("↑↓ 移动 · Enter 切换 · Esc 取消"),
		)
	}
	parts := []string{}
	if m.status != "" {
		parts = append(parts, styleThink.Render(m.status+"▌"))
	}
	if m.permLayer != nil {
		parts = append(parts, m.renderPermissionLayer())
	}
	parts = append(parts, m.textarea.View(), m.renderFooter())
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderPermissionLayer 渲染权限确认弹层。
// 显示在输入框上方，黄色警告框，提示工具/参数/原因 + 输入方式。
func (m *ChatModel) renderPermissionLayer() string {
	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("11")). // 黄色
		Bold(true).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(0, 1)

	content := fmt.Sprintf("⚠️ 权限确认: %s", m.permLayer.tool)
	if m.permLayer.args != "" {
		content += "\n  参数: " + m.permLayer.args
	}
	if m.permLayer.reason != "" {
		content += "\n  原因: " + m.permLayer.reason
	}
	content += "\n  输入 y 允许 / n 拒绝"

	return style.Render(content)
}

// renderFooter 底部状态栏。
// 格式：agentic │ 上下文 15K/50K (30%) │ 💰 余额（有则显示）│ ctrl+c 退出
func (m *ChatModel) renderFooter() string {
	var parts []string

	// 上下文占用（limit>0 时显示；used 可为 0——启动时初始显示 0/50.0k (0%)）。
	if m.contextLimit > 0 {
		parts = append(parts, fmt.Sprintf("上下文 %s/%s (%d%%)",
			formatToken(m.contextUsedTokens), formatToken(m.contextLimit),
			m.contextUsedTokens*100/m.contextLimit))
	}

	// 余额（非空时显示）。
	if m.balance != "" {
		parts = append(parts, m.balance)
	}

	parts = append(parts, "ctrl+c 退出")

	style := lipgloss.NewStyle().Height(1).Faint(true)
	return style.Render("agentic │ " + strings.Join(parts, " │ "))
}

// formatToken 格式化 token 数（<1000 原样，>=1000 显示 "1.2k"）。
func formatToken(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
