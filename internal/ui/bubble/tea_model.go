// tea 渲染模型：常驻输入框 + 状态栏，对话区追加式累积。
//
// 阶段 1 结论：追加式输出 + ANSI 锚定底部状态栏在终端滚动后必然错位（pyte 模拟证实），
// 无法可靠「常驻」。阶段 2 改为用 tea.Program 全屏渲染模型：
//
//	对话区（追加式累积）      ← teaUI.conversation
//	──────────────────────  ← 分割线
//	[状态栏]                 ← components.StatusModel
//	> [输入框]               ← components.InputModel
//
// 对话区不再走外部 fmt.Println，而是作为 tea 模型的一部分（追加式增长的字符串），
// 流式 delta / 最终回答通过 teaAppendMsg 消息更新对话区，由 tea 统一重绘。
// 输入走 tea 消息（KeyMsg），Enter 提交后通过 submitInput 桥接给 Runner 的 inputChan
// （复用现有 select 语义，Runner 循环不变）。
package bubble

import (
	"strings"

	"agentic/internal/ui/components"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// teaAppendMsg 对话内容追加消息（由 queryEngine 事件驱动，tea 模型内 accumulate）。
//
// 追加语义（流式合并，见 teaUI.Update）：
//   - content 以 \n 结尾：闭合块（最终回答/工具框线/思考行/消息行）——
//     若当前对话区最后一行是未闭合的流式行（不以 \n 结尾），先补 \n 断开再追加。
//   - content 不以 \n 结尾：流式增量——合并进最后一行（流式 token 逐段累积）。
type teaAppendMsg struct {
	content string // 追加到对话区的内容（流式增量 / 闭合块）
}

// teaBalanceMsg 余额更新消息。
type teaBalanceMsg struct {
	balance string
}

// teaUI 是 BubbleUI 的 tea 渲染模型。
//
// View() 输出：对话区（追加式累积） + 分割线 + 状态栏 + 常驻输入框。
// 内部持 *BubbleUI 引用读取字段（sessionName/model/token/余额），
// 状态栏数据在 View 时从 b 同步（持 uiMu 锁，见 statusSnapshot）。
type teaUI struct {
	b *BubbleUI

	conversation []string // 对话区累积行（追加式；流式增量合并进最后一行）
	width        int      // 终端宽度
	height       int      // 终端高度

	input  components.InputModel  // textinput 输入框
	status components.StatusModel // 状态栏
}

// _ 编译期断言：*teaUI 必须满足 tea.Model（tea.NewProgram 接收该接口）。
// Update 返回 tea.Model（内部返回 *m 指针），动态类型始终为 *teaUI。
var _ tea.Model = (*teaUI)(nil)

// teaModel 构造 tea 渲染模型（供测试与 tea.NewProgram 使用）。
func (b *BubbleUI) teaModel() *teaUI {
	return &teaUI{
		b:      b,
		input:  components.NewInputModel(),
		status: components.NewStatusModel(),
	}
}

// Init 返回初始命令（启动输入框光标闪烁）。
func (m *teaUI) Init() tea.Cmd {
	return m.input.Init()
}

// Update 处理 tea 消息。
//
// 返回值类型说明：tea.Model 接口要求 Update 返回 tea.Model（运行时动态类型被赋回接口）。
// 惯例做法是返回 teaUI 值，但 teaUI 的方法集由指针接收者实现（*teaUI 满足接口），
// 值类型 teaUI 本身并不实现 tea.Model —— 若 Update 返回 teaUI，运行时被赋回接口后
// 动态类型变为值类型 teaUI，下一次 model.Update 调用即编译不通过。
// 故这里返回 tea.Model，内部返回 *m（指针）保证动态类型始终为 *teaUI。
// 注意 m 是 *teaUI，直接修改其字段即更新共享状态；textinput 是值类型，
// 其 Update 的返回值必须写回 m.input（值接收者方法集，丢弃返回值会丢失输入状态）。
func (m *teaUI) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		// 输入框按键：Enter 提交，其他转发给 textinput。
		if v.Type == tea.KeyEnter {
			value := m.input.Value()
			if value == "" {
				return m, nil
			}
			m.input.SetValue("")
			// 提交输入：通过 BubbleUI 的 inputChan 桥接给 Runner。
			m.b.submitInput(value)
			return m, nil
		}
		// 其他按键转发给 textinput，返回值写回（textinput 是值类型组件）。
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case tea.WindowSizeMsg:
		m.width = v.Width
		m.height = v.Height
		m.setWidths()
		return m, nil

	case teaAppendMsg:
		m.appendConversation(v.content)
		return m, nil

	case teaBalanceMsg:
		// 走 SetBalanceText（持 uiMu 锁），避免与后台余额查询 goroutine 并发写 race。
		m.b.SetBalanceText(v.balance)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// appendConversation 追加内容到对话区，实现流式合并语义。
//
// 规则：
//   - 有未闭合的流式行（最后一行不以 \n 结尾）：
//     * 新内容以 \n 结尾（闭合块：最终回答/工具框线/思考行等）→ 先补 \n 断开流式行，
//       再追加新块（保证块从新行开始，不与流式 token 拼在同一行）。
//     * 新内容不以 \n 结尾（流式增量）→ 直接合并进最后一行（token 逐段累积）。
//   - 无未闭合流式行（空对话区或最后一行已闭合）→ 直接追加为新行。
func (m *teaUI) appendConversation(content string) {
	if len(m.conversation) > 0 {
		last := len(m.conversation) - 1
		lineOpen := !strings.HasSuffix(m.conversation[last], "\n")
		if lineOpen {
			if strings.HasSuffix(content, "\n") {
				// 流式行上有闭合块到达：断开当前行，块从新行开始。
				m.conversation[last] += "\n"
				m.conversation = append(m.conversation, content)
				return
			}
			// 流式增量：合并进当前行。
			m.conversation[last] += content
			return
		}
	}
	// 无未闭合流式行：新起一行（保留尾部 \n 供 join 语义）。
	m.conversation = append(m.conversation, content)
}

// setWidths 按终端宽度设置输入框与状态栏宽度。
// 注意：InputModel.SetWidth 内部已减去提示符占位（> ），这里直接传 m.width，
// 若再减 4 会双重减法（net width-8）且与分割线宽度（m.width）不一致。
// textinput 对 Width <= 0 时不做截断（完整渲染占位符），窄终端可安全退化为不限制宽度。
func (m *teaUI) setWidths() {
	m.input.SetWidth(m.width)
	m.status.SetWidth(m.width)
}

// statusSnapshot 一次性读取状态栏所需字段（持 uiMu 锁），供 tea 模型 View 使用。
// View 在 tea 事件循环 goroutine 中执行，而 sessionName/model/token/balanceText
// 由 Runner 主循环（SetSessionName 等）与后台余额 goroutine（ShowBalance）写入，
// 必须加锁读，否则 go test -race 会检测到数据竞争。
func (b *BubbleUI) statusSnapshot() (session, model string, in, out int, balance string) {
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	return b.sessionName, b.model, b.inputTokens, b.outputTokens, b.balanceText
}

// View 渲染整屏：对话区 + 分割线 + 状态栏 + 输入框。
func (m *teaUI) View() string {
	// 同步状态栏数据（从 BubbleUI 字段，加锁读避免跨 goroutine 竞争）。
	session, model, in, out, balance := m.b.statusSnapshot()
	m.status.SetSession(session)
	m.status.SetModel(model)
	m.status.SetTokens(in, out)
	if strings.Contains(balance, "\n") {
		balance = strings.ReplaceAll(balance, "\n", "、")
	}
	m.status.SetBalance(balance)

	m.setWidths()

	// 对话区（追加式累积）：直接拼接各 entry（闭合块自带尾部 \n，
	// 流式行末未闭合时补 \n 结束，避免与状态栏挤在同一行）。
	var sb strings.Builder
	for _, entry := range m.conversation {
		sb.WriteString(entry)
		if !strings.HasSuffix(entry, "\n") {
			sb.WriteString("\n")
		}
	}
	sep := strings.Repeat("─", m.width)
	if sep == "" {
		sep = "─"
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(sep))
	sb.WriteString("\n")
	sb.WriteString(m.status.View())
	sb.WriteString("\n")
	sb.WriteString(m.input.View())

	return sb.String()
}
