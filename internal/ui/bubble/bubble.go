// Package bubble 提供基于 Lip Gloss + Glamour 的终端 UI 实现。
//
// BubbleUI 采用混合模式（阶段 2 起）：
//   - 主渲染：tea.Program 全屏渲染（对话区 + 状态栏 + 常驻输入框），Start 启动
//   - 主输入：tea 输入框，Enter 经 submitInput 桥接给 Runner（ReadTeaInputChan）
//   - 会话选择器：独立 Bubble Tea 程序（/list 时前台运行，tea 主程序已由 Runner 暂停）
//
// 核心特性：
//   - 流式文本：tea 事件（teaAppendMsg）驱动对话区追加，tea 统一重绘
//   - Markdown 渲染：最终回答通过 Glamour 渲染为终端友好的格式
//   - 框线输出：工具调用和结果用 Unicode 框线字符绘制
//   - 权限确认：通过输入转发 channel 读取用户 y/N 决策
//
// 注意：阶段 1 的直接写 os.Stdout + ANSI 光标控制路径仍保留（Welcome、
// RunSessionPicker 等外部输出），但在 tea 模式（Start 后）自动退位为 no-op，
// 避免与 tea 渲染冲突。
//
// 文件组织（本文件仅含阶段 1 核心 + 事件转发；tea 部分已拆分）：
//   - bubble.go      阶段 1 ANSI（clearStatusBar/renderStatusBar/statusBarText）、
//                    事件转发方法（OnDelta/OnFinal/...，含 tea 分支）、样式、字段
//   - tea_model.go   teaUI 渲染模型（Init/Update/View/statusSnapshot）+ 消息类型
//   - tea_lifecycle.go  Start/sendToTea/IsTeaMode/PauseTea/ResumeTea/输入桥接
//   - box.go         工具框线/最终回答共享渲染（阶段 1 与 tea 模式共用）
package bubble

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"agentic/internal/ui/components"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
)

// BubbleUI 是 UI 接口的终端美化实现。
//
// 混合架构（阶段 2 起）：
//   - 主渲染：tea.Program 全屏渲染（Start 启动），事件经 sendToTea 投递
//   - 主输入：tea 输入框 → submitInput → ReadTeaInputChan（与 raw 路径隔离）
//   - 会话选择器：独立 Bubble Tea 全屏程序（/list 时前台运行）
//   - 阶段 1 直写 stdout 路径：Welcome 等外部输出保留；正文/状态栏在 tea
//     模式（IsTeaMode）下退位为 no-op，避免与 tea 渲染冲突
//
// 流式输出机制（阶段 2）：
//  1. queryEngine 事件（think/delta/final/tool/balance）经 UI 接口方法到达
//  2. 各方法内部通过 sendToTea(teaAppendMsg{...}) 把内容投递到 tea 模型
//  3. tea 模型对话区追加式累积，View 全屏重绘
//
// 注意：tea 并发模型要求所有 UI 更新走 Program.Send（消息 channel），
// 不直接修改 tea 模型字段。
type BubbleUI struct {
	// conversation 对话历史组件（用于 Bubble Tea 交互场景，如 /list 中的对话预览）
	conversation *components.ConversationModel
	// input 输入栏组件（预留，主循环使用 bufio.Scanner 而非此组件）
	input components.InputModel
	// status 状态栏组件（预留，未来切换到全 Bubble Tea 模式时使用）
	status components.StatusModel
	// toolView 工具调用弹窗组件（预留，未来实现交互式工具详情查看）
	toolView *components.ToolViewModel

	// sessionName 当前会话的显示名称（由 Runner.SetSessionName 设置）
	sessionName string
	// model 当前使用的 LLM 模型名称（启动时设置）
	model string

	// inputTokens 本轮已累计的输入 token 数
	inputTokens int
	// outputTokens 本轮已累计的输出 token 数
	outputTokens int

	// balanceText 账户余额展示文本（/balance 成功后由 Runner 设置）
	balanceText string
	// statusVisible 状态栏是否启用（阶段 1：默认启用；可通过 SetStatusBarVisible 控制）
	statusVisible bool
	// statusBarShown 状态栏是否当前在屏（renderStatusBar 后 true，clearStatusBar 后 false）。
	// 状态位驱动生命周期：clear/render 仅在应然状态下执行 ANSI 序列，
	// 避免"空上移清错行"与"重复上移叠字"。
	statusBarShown bool

	// uiMu 保护状态栏相关字段（balanceText/statusVisible/statusBarShown/sessionName/model/inputTokens/outputTokens）
	// 与 clearStatusBar/renderStatusBar 的 ANSI 输出。
	// 后台余额查询 goroutine（ShowBalance）与主循环并发访问，用单把大锁粗粒度串行化。
	uiMu sync.Mutex

	// hasDelta 本轮是否收到过 OnDelta（用于判断是否需要清除流式文本）
	hasDelta bool
	// cursorSaved 是否已保存光标位置（OnThink 时设为 true，OnFinal/OnToolCall 时清除）
	cursorSaved bool

	// glamour 是 Markdown 渲染器，用于 OnFinal 时渲染最终回答。
	// 初始化失败时为 nil，此时回退到纯文本输出。
	glamour *glamour.TermRenderer

	// inputChan 是异步输入 channel，由 ReadInputChan 首次调用时启动
	inputChan chan string
	// inputOnce 保证后台输入 goroutine 只启动一次
	inputOnce sync.Once

	// teaInputCh 是 tea 输入桥接 channel（阶段 2 起 tea 输入专用，与 raw 路径隔离）。
	// submitInput 写入，Runner 通过 ReadTeaInputChan 读取。
	// 生命周期：由 NewBubbleUI 创建，永不关闭——与 raw 路径的 inputChan
	// （sync.Once + rawInputLoop defer close）完全隔离，避免 close 与 send 竞态
	// （go test -race 曾检测到 submitInput 发送 vs rawInputLoop close 的 data race）。
	teaInputCh chan string

	// teaProgram 是阶段 2 起的 tea 渲染程序（Start 启动，sendToTea 投递消息）。
	// tea 在后台 goroutine 运行，事件通过 Program.Send 投递（tea 并发模型要求消息走 channel，
	// 不直接改模型字段）。tea 未启动/已退出时为 nil，sendToTea 安全丢弃。
	teaProgram *tea.Program
	// teaMu 保护 teaProgram 字段读写：Start/cancel 在主 goroutine，sendToTea/IsTeaMode
	// 在事件 goroutine（queryEngine 事件流 + 后台余额查询），tea.Run 返回后的清理
	// 也在后台 goroutine——三者并发访问，必须加锁。
	teaMu sync.Mutex

	// oldTermState 生模式前的终端状态，Close() 时恢复以防止终端残留生模式。
	oldTermState *term.State
	// termFd 终端文件描述符。
	termFd int
}

// NewBubbleUI 创建 BubbleUI 实例。
// 初始化 Glamour 渲染器（自动检测终端样式，100 列自动换行），
// 以及各子组件（conversation、input、status、toolView）。
//
// _ 参数是预留的 Bubble Tea ProgramOption，目前未使用但保留以兼容未来
// 切换到全 Bubble Tea 模式的接口。
func NewBubbleUI(_ ...tea.ProgramOption) *BubbleUI {
	glamourRenderer, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(100),
	)
	if err != nil {
		// 渲染器初始化失败时降级为 nil，后续回退纯文本输出。
		glamourRenderer = nil
	}
	return &BubbleUI{
		conversation:  components.NewConversationModel(),
		input:         components.NewInputModel(),
		status:        components.NewStatusModel(),
		toolView:      components.NewToolViewModel(),
		glamour:       glamourRenderer,
		statusVisible: true,
		teaInputCh:    make(chan string, 1),
	}
}

// Close 恢复终端状态并释放资源。
// 必须调用以将终端从生模式恢复到熟模式，否则退出后终端不回显输入。
func (b *BubbleUI) Close() error {
	if b.oldTermState != nil {
		term.Restore(b.termFd, b.oldTermState)
		b.oldTermState = nil
	}
	return nil
}

// ──────────────────────────────────────────────────────────
// 样式定义
// ──────────────────────────────────────────────────────────

// 预定义的 Lip Gloss 样式，用于不同类型的输出。
// 颜色使用 ANSI 256 色调色板编号。
var (
	// styleUserPrefix 用户输入提示符样式：青色加粗（"> "）
	styleUserPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	// styleThink 思考状态样式：灰色斜体（"⏳ 思考中..."）
	styleThink = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	// styleError 错误消息样式：红色加粗
	styleError = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	// styleMuted 次要文本样式：灰色（参数、提示等）
	styleMuted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	// styleSuccess 成功消息样式：绿色（工具执行成功标题）
	styleSuccess = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	// styleSeparator 分隔线样式：深灰色（每轮回答结束后的分隔线）
	styleSeparator = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	// styleToolPrefix 工具调用前缀样式：黄色加粗（框线竖线）
	styleToolPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
)

// ──────────────────────────────────────────────────────────
// UI 接口实现：输入组
// ──────────────────────────────────────────────────────────

// ReadInput 同步读取用户输入。打印提示符 "> " 后阻塞等待。
// 内部复用 ReadInputChan 的 channel。
func (b *BubbleUI) ReadInput() (string, error) {
	fmt.Print(styleUserPrefix.Render("> "))
	os.Stdout.Sync()
	ch := b.ReadInputChan()
	input, ok := <-ch
	if !ok {
		return "", fmt.Errorf("EOF")
	}
	return input, nil
}

// ReadInputChan 返回异步输入 channel。
// 首次调用时启动后台 goroutine，将终端设为生模式（raw mode），
// 逐 rune 读取输入以正确处理多字节字符（中文等）的退格删除。
// 后续调用返回同一个 channel（通过 sync.Once 保证）。
// channel 在输入流结束（EOF）时关闭。
func (b *BubbleUI) ReadInputChan() <-chan string {
	b.inputOnce.Do(func() {
		b.inputChan = make(chan string, 1)

		// 在进入生模式前保存终端状态，Close() 用它恢复终端。
		fd := int(os.Stdin.Fd())
		b.termFd = fd
		b.oldTermState, _ = term.GetState(fd)

		go b.rawInputLoop(fd)
	})
	return b.inputChan
}

// rawInputLoop 在生模式下运行，逐 rune 读取输入。
//
// 与熟模式 bufio.Scanner 的关键区别：
//   - 按 rune（而非字节）处理退格：中文"好"(3字节) → 一次退格删掉整个字符
//   - 手动回显：每个可打印字符即时回显到终端
//   - 退格清除：根据 rune 显示宽度擦除正确的列数（中文 2 列，ASCII 1 列）
//
// 生模式下 Bubble Tea 的 session picker（/list）不受影响，
// 因为它通过 /dev/tty 读取输入（独立的 fd）。
func (b *BubbleUI) rawInputLoop(fd int) {
	defer close(b.inputChan)

	if _, err := term.MakeRaw(fd); err != nil {
		return // 无法设置生模式，放弃
	}
	// term.MakeRaw 清除了 OPOST（输出后处理）标志，导致 \n 不再自动转换为 \r\n，
	// 从而造成 fmt.Println 等输出换行后光标不回行首的格式化错位。
	// 重新启用 OPOST 以保持输出端的正常换行行为，仅保留输入端的生模式特性。
	// 平台差异（请求码、req 参数类型）封装在 termios 平台文件中。
	enableOPOST(fd)
	// 后备恢复：如果 Close() 未能运行（如崩溃），defer 尽力恢复终端。
	// 正常退出时 Close() 已恢复 → 此处 Restore 已是熟模式 → 安全无操作。
	defer func() {
		if b.oldTermState != nil {
			term.Restore(fd, b.oldTermState)
			b.oldTermState = nil
		}
	}()

	reader := bufio.NewReader(os.Stdin)
	var line []rune

	for {
		r, _, err := reader.ReadRune()
		if err != nil {
			return // EOF 或读取错误，退出
		}

		switch r {
		case '\r': // Enter（生模式下回车发送 \r）
			os.Stdout.WriteString("\r\n")
			os.Stdout.Sync()
			b.inputChan <- string(line)
			line = line[:0]

		case 0x03: // Ctrl+C — 清空当前行
			os.Stdout.WriteString("^C\r\n")
			os.Stdout.Sync()
			line = line[:0]

		case 0x04: // Ctrl+D — 空行时退出
			if len(line) == 0 {
				os.Stdout.WriteString("\r\n")
				os.Stdout.Sync()
				return
			}
			// 非空行时忽略 Ctrl+D

		case 0x7F: // Backspace — 删除最后一个 rune
			if len(line) > 0 {
				last := line[len(line)-1]
				line = line[:len(line)-1]

				// 按显示宽度擦除：中文占 2 列，ASCII 占 1 列
				w := runewidth.RuneWidth(last)
				for i := 0; i < w; i++ {
					os.Stdout.WriteString("\b \b")
				}
				os.Stdout.Sync()
			}

		default:
			// 可打印字符 + Tab
			if r >= 0x20 || r == '\t' {
				line = append(line, r)
				os.Stdout.WriteString(string(r))
				os.Stdout.Sync()
			}
			// 方向键等转义序列（0x1B[... ）静默忽略
		}
	}
}

// ──────────────────────────────────────────────────────────
// UI 接口实现：事件通知组
// ──────────────────────────────────────────────────────────

// OnThink 通知新一轮思考开始。
// 阶段 1：打印空行后保存光标位置（\033[s），然后显示 "⏳ 思考中..."。
// 阶段 2（tea 模式）：sendToTea 追加闭合行（尾部 \n），ANSI 光标控制退位。
func (b *BubbleUI) OnThink(_ int) {
	if b.IsTeaMode() {
		b.sendToTea(teaAppendMsg{content: "  ⏳ 思考中...\n"})
		return
	}
	// 新一轮思考开始前清除在屏状态栏：下一轮内容将从该行覆盖输出。
	// 若状态栏不在屏（正常场景）则空操作。
	b.clearStatusBar()
	fmt.Println()
	// 保存光标位置：后续 OnDelta 的流式文本从此位置开始输出，
	// OnFinal 或 OnToolCall 时从此位置恢复并清除。
	fmt.Print("\033[s")
	b.cursorSaved = true
	b.hasDelta = false
	fmt.Println(styleThink.Render("  ⏳ 思考中..."))
}

// OnDelta 输出流式增量文本。
// 阶段 1：直接打印 content（不换行、不加前缀），用灰色斜体样式。
// 阶段 2（tea 模式）：sendToTea 追加流式增量（tea 模型合并进当前行）。
func (b *BubbleUI) OnDelta(content string) {
	if b.IsTeaMode() {
		b.sendToTea(teaAppendMsg{content: content})
		return
	}
	b.hasDelta = true
	// 浅色斜体直接输出流式文本。
	fmt.Print(styleThink.Render(content))
}

// OnToolCall 显示工具调用请求。
// 先清除流式文本（恢复光标 + 清除到屏幕底），然后绘制框线包裹的工具调用信息。
// 参数 JSON 会格式化缩进显示，单行参数用紧凑格式。
//
// 框线样式（宽度根据内容自适应，最小 60 列）：
//
//	┌─ 🔧 shell ───────────────────────────
//	│ {"command": "ls -la"}
//	└──────────────────────────────────────
func (b *BubbleUI) OnToolCall(name, args string) {
	if b.IsTeaMode() {
		// tea 模式：追加工具调用框线（闭合块，尾部 \n，见 box.go），ANSI 退位。
		b.sendToTea(teaAppendMsg{content: toolCallBox(name, args)})
		return
	}
	// 清除流式文本区域。
	if b.cursorSaved {
		fmt.Print("\033[u\033[J")
		os.Stdout.Sync()
		b.cursorSaved = false
		b.hasDelta = false
	}
	// 尝试格式化参数 JSON（缩进输出）。
	displayArgs := args
	var parsed map[string]any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		if formatted, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			displayArgs = string(formatted)
		}
	}
	width := boxWidth(displayArgs, 60)

	fmt.Println()
	fmt.Printf("  %s %s\n",
		styleToolPrefix.Render("┌─ 🔧 "+name),
		styleMuted.Render(strings.Repeat("─", max(0, width-len(name)-6))))
	for _, line := range strings.Split(displayArgs, "\n") {
		fmt.Printf("  %s %s\n", styleToolPrefix.Render("│"), styleMuted.Render(line))
	}
	fmt.Printf("  %s\n", styleToolPrefix.Render("└"+strings.Repeat("─", width+1)))

	// 多迭代时保持状态栏：工具调用框线后重绘（状态位守卫避免重复上移）。
	b.renderStatusBar()
}

// OnToolResult 显示工具执行结果。
// 超过 15 行的输出会被截断。成功用绿色 "✅ 结果"，失败用红色 "❌ 错误"。
// 框线样式与 OnToolCall 保持一致。
func (b *BubbleUI) OnToolResult(name, result string, isError bool) {
	if b.IsTeaMode() {
		// tea 模式：追加工具结果框线（闭合块，尾部 \n，见 box.go）。
		b.sendToTea(teaAppendMsg{content: toolResultBox(name, result, isError)})
		return
	}
	lines := strings.Split(result, "\n")
	totalLines := len(lines)
	if totalLines > 15 {
		lines = lines[:15]
	}

	// 标题颜色根据成功/失败切换。
	titleStyle := styleSuccess
	titleText := "✅ 结果"
	if isError {
		titleStyle = styleError
		titleText = "❌ 错误"
	}

	width := boxWidth(strings.Join(lines, "\n"), 60)

	fmt.Println()
	fmt.Printf("  %s %s\n",
		titleStyle.Render("┌─ "+titleText),
		styleMuted.Render(strings.Repeat("─", max(0, width-len(titleText)+2))))
	for _, line := range lines {
		fmt.Printf("  %s %s\n", titleStyle.Render("│"), line)
	}
	if totalLines > 15 {
		fmt.Printf("  %s %s\n",
			titleStyle.Render("│"),
			styleMuted.Render(fmt.Sprintf("... (共 %d 行，已截断)", totalLines)))
	}
	fmt.Printf("  %s\n", titleStyle.Render("└"+strings.Repeat("─", width+1)))

	// 多迭代时保持状态栏：工具结果框线后重绘（状态位守卫避免重复上移）。
	b.renderStatusBar()
}

// OnContinue 通知继续推理。清除流式状态，打印继续提示。
// 后续 queryLoop 会立刻 yield 下一轮 OnThink（先 clearStatusBar），故此处不重复清除。
func (b *BubbleUI) OnContinue(iteration int) {
	if b.IsTeaMode() {
		// tea 模式：追加继续推理闭合行。
		b.sendToTea(teaAppendMsg{content: fmt.Sprintf("  🔄 Continuing... (iteration %d)\n", iteration)})
		return
	}
	b.hasDelta = false
	b.cursorSaved = false
	fmt.Println(styleThink.Render(fmt.Sprintf("  🔄 Continuing... (iteration %d)", iteration)))
	b.renderStatusBar()
}

// OnFinal 显示最终回答。
// 1. 恢复光标位置并清除流式文本区域
// 2. 如果之前有流式文本，打印 "✅ 思考完成"
// 3. 用 Glamour 渲染 Markdown（带 2 空格缩进），失败则回退纯文本
// 4. 打印分隔线标记本轮结束
func (b *BubbleUI) OnFinal(answer string, inputTokens, outputTokens, totalTokens int) {
	if b.IsTeaMode() {
		// tea 模式：最终回答作为闭合块追加到对话区（尾部 \n）。
		// 内容含换行结尾，Update 的合并逻辑会与流式增量（不含换行）区分开：
		// 若当前行是未闭合流式行，先补 \n 断开，最终回答另起一行。
		var sb strings.Builder
		sb.WriteString("  ✅ 思考完成\n")
		sb.WriteString(finalAnswerText(b, answer))
		if totalTokens > 0 {
			sb.WriteString(fmt.Sprintf("  ⚡ 本轮 %d tokens（输入 %d / 输出 %d）\n", totalTokens, inputTokens, outputTokens))
		}
		sb.WriteString(strings.Repeat("─", 60))
		sb.WriteString("\n")
		b.sendToTea(teaAppendMsg{content: sb.String()})
		return
	}
	// 清除流式文本，保留"思考中..."行并改为"思考完成"。
	if b.cursorSaved {
		fmt.Print("\033[u\033[J")
		os.Stdout.Sync()
		b.cursorSaved = false
	}
	if b.hasDelta {
		fmt.Println(styleThink.Render("  ✅ 思考完成"))
		fmt.Println()
	}
	b.hasDelta = false

	// \033[u 恢复光标位置后，光标回到 OnThink 保存的"思考中"行首——
	// 状态栏位置假设（光标位于输入提示符行）已失效，先清除在屏状态栏，
	// 防止后续渲染正文时把状态栏残留写进正文。
	b.clearStatusBar()

	// 用 Glamour 渲染 Markdown 最终答案（带左侧缩进）。
	if b.glamour != nil {
		rendered, err := b.glamour.Render(answer)
		if err == nil {
			for _, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
				fmt.Printf("  %s\n", line)
			}
		} else {
			for _, line := range strings.Split(answer, "\n") {
				fmt.Printf("  %s\n", line)
			}
		}
	} else {
		// Glamour 不可用时回退纯文本。
		for _, line := range strings.Split(answer, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	b.hasDelta = false
	fmt.Println()

	// 本轮 token 统计（精确值，来自 API usage）。
	// totalTokens 为 0 时（API 未返回 usage）不展示，避免误导。
	if totalTokens > 0 {
		fmt.Println(styleMuted.Render(fmt.Sprintf(
			"  ⚡ 本轮 %d tokens（输入 %d / 输出 %d）",
			totalTokens, inputTokens, outputTokens,
		)))
	}
	fmt.Println(styleSeparator.Render(strings.Repeat("─", 60)))

	// 输出完成后重绘状态栏（此时光标已回到输入提示符行首）。
	b.renderStatusBar()
}

// OnError 打印错误信息（红色加粗）。
func (b *BubbleUI) OnError(err error) {
	if b.IsTeaMode() {
		b.sendToTea(teaAppendMsg{content: fmt.Sprintf("❌ Error: %v\n", err)})
		return
	}
	// 追加正文前先清除在屏状态栏，避免把状态栏写进错误行。
	b.clearStatusBar()
	fmt.Println(styleError.Render(fmt.Sprintf("❌ Error: %v", err)))
	b.renderStatusBar()
}

// OnMessage 打印一般性消息（无额外样式）。
func (b *BubbleUI) OnMessage(msg string) {
	if b.IsTeaMode() {
		if !strings.HasSuffix(msg, "\n") {
			msg += "\n"
		}
		b.sendToTea(teaAppendMsg{content: msg})
		return
	}
	// 追加正文前先清除在屏状态栏，避免把状态栏写进消息行。
	b.clearStatusBar()
	fmt.Println(msg)
	b.renderStatusBar()
}

// ShowBalance 展示账户余额（阶段 1：更新状态栏并重绘）。
// line 是已格式化的余额文本，如 "💰 ¥110.00（充值 ¥100.00 / 赠金 ¥10.00）"。
// 阶段 2（tea 模式）：更新余额字段 + sendToTea(teaBalanceMsg) 让 tea 模型重绘状态栏；
// 阶段 1 的 ANSI 状态栏重绘退位。
// 注意：可能在后台 goroutine 调用（query_engine 每轮余额查询），
// 锁保护字段读写 + 状态栏 ANSI 重绘，避免与主循环竞态。
func (b *BubbleUI) ShowBalance(line string) {
	b.SetBalanceText(line)
	if b.IsTeaMode() {
		// tea 模式：状态栏由 tea 模型渲染，投递余额消息即可。
		b.sendToTea(teaBalanceMsg{balance: line})
		return
	}
	// clear/render 内部均有状态位 + statusVisible 守卫，外层无需重复判断。
	b.clearStatusBar()
	b.renderStatusBar()
}

// ConfirmPermission 显示权限确认提示，等待用户输入。
// 提示格式："⚠️ 权限确认: <tool>" + 参数 + 原因 + "允许执行? [y/N] "。
// 如果 inputForward 不为 nil，从此 channel 读取用户输入；
// 否则从 ReadInputChan 读取。
// 返回 true 表示用户输入 "y" 或 "yes"（大小写不敏感）。
func (b *BubbleUI) ConfirmPermission(tool, args, reason string, inputForward <-chan string) (bool, error) {
	// TODO(Task 9): tea 模式下权限确认需弹层渲染（当前提示打到主屏，alt screen 下不可见，
	// 功能可用但盲输）。
	fmt.Printf("\n%s %s\n", styleThink.Render("⚠️  权限确认:"), tool)
	fmt.Printf("  参数: %s\n", styleMuted.Render(args))
	if reason != "" {
		fmt.Printf("  原因: %s\n", styleMuted.Render(reason))
	}
	fmt.Print(styleUserPrefix.Render("  允许执行? [y/N] "))
	os.Stdout.Sync()

	var input string
	var ok bool
	if inputForward != nil {
		input, ok = <-inputForward
	} else {
		input, ok = <-b.ReadInputChan()
	}
	if !ok {
		return false, fmt.Errorf("EOF")
	}
	answer := strings.ToLower(strings.TrimSpace(input))
	return answer == "y" || answer == "yes", nil
}

// Welcome 打印启动横幅。
// 包含 ASCII art banner、版本号、模型名称、使用提示和可用命令列表。
// 使用多种颜色区分不同信息层级：紫色（banner）、灰色（标签）、青色（模型名）。
func (b *BubbleUI) Welcome(model string) {
	purple := lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	art := []string{
		"              _   _            _   _            ",
		"     /\\      | | (_)          | | (_)           ",
		"    /  \\   __| |_ _  ___  ___| |_ _  ___  _ __ ",
		"   / /\\ \\ / _` | | |/ _ \\/ __| __| |/ _ \\| '_ \\",
		"  / ____ \\ (_| | | |  __/\\__ \\ |_| | (_) | | | |",
		" /_/    \\_\\__,_|_|_|\\___||___/\\__|_|\\___/|_| |_|",
	}
	fmt.Println()
	for _, line := range art {
		fmt.Println(purple.Render(line))
	}
	fmt.Println()
	fmt.Printf("  %s %s\n", muted.Render(":: Agentic ::"), muted.Render("v0.1"))
	fmt.Println(dim.Render("  ─────────────────────────────────────────────"))
	fmt.Printf("  %s  %s\n", muted.Render("Model"), cyan.Render(model))
	fmt.Printf("  %s  %s\n", muted.Render("Usage"), muted.Render("输入任务开始对话，输入 exit 退出"))
	fmt.Printf("  %s  %s\n", muted.Render("Cmds "), muted.Render("/new /list /switch /delete /rename /current"))
	fmt.Println()
}

// ──────────────────────────────────────────────────────────
// UI 接口实现：元数据设置
// ──────────────────────────────────────────────────────────

// SetSessionName 设置当前会话的显示名称。
func (b *BubbleUI) SetSessionName(name string) {
	b.uiMu.Lock()
	b.sessionName = name
	b.uiMu.Unlock()
}

// SetModel 设置当前使用的模型名称。
func (b *BubbleUI) SetModel(model string) {
	b.uiMu.Lock()
	b.model = model
	b.uiMu.Unlock()
}

// UpdateTokens 累计本轮 token 用量。
func (b *BubbleUI) UpdateTokens(input, output int) {
	b.uiMu.Lock()
	b.inputTokens += input
	b.outputTokens += output
	b.uiMu.Unlock()
}

// ResetTokens 重置本轮 token 计数（新一轮查询开始前调用）。
func (b *BubbleUI) ResetTokens() {
	b.uiMu.Lock()
	b.inputTokens = 0
	b.outputTokens = 0
	b.uiMu.Unlock()
}

// SetBalanceText 设置账户余额展示文本（空=不显示余额段）。
func (b *BubbleUI) SetBalanceText(text string) {
	b.uiMu.Lock()
	b.balanceText = text
	b.uiMu.Unlock()
}

// SetStatusBarVisible 控制状态栏是否渲染（阶段 3 全量模式接管后此开关用于过渡）。
func (b *BubbleUI) SetStatusBarVisible(visible bool) {
	b.uiMu.Lock()
	b.statusVisible = visible
	b.uiMu.Unlock()
}

// ──────────────────────────────────────────────────────────
// 状态栏（阶段 1 ANSI 路径）
// ──────────────────────────────────────────────────────────

// statusBarText 渲染状态栏单行文本（供测试与 renderStatusBar 共用）。
// 复用 components.StatusModel 的渲染逻辑：同步 session/model/token/余额后调 View()。
// 余额文本多货币时为 \n 分隔多行，进状态栏前 clamp 成单行（\n → 、），保持单行渲染。
func (b *BubbleUI) statusBarText() string {
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	return b.statusBarTextLocked()
}

// statusBarTextLocked 渲染状态栏单行文本，调用方必须持有 b.uiMu。
// 拆出锁内实现，避免 renderStatusBar 锁内调用 statusBarText 时二次加锁死锁。
func (b *BubbleUI) statusBarTextLocked() string {
	if !b.statusVisible {
		return ""
	}
	st := b.status
	st.SetSession(b.sessionName)
	st.SetModel(b.model)
	st.SetTokens(b.inputTokens, b.outputTokens)
	balance := b.balanceText
	if strings.Contains(balance, "\n") {
		balance = strings.ReplaceAll(balance, "\n", "、")
	}
	st.SetBalance(balance)
	return st.View()
}

// clearStatusBar 清除状态栏所在行（将光标移到该行并清空）。
// 状态栏渲染在屏幕底部最后一行上方，追加输出前需先清除，输出完再重绘。
// 状态位驱动：仅当状态栏当前在屏（statusBarShown）时才执行清除，
// 避免"状态栏已清除却空操作"。清除后置 statusBarShown = false。
//
// 定位：先把光标移到屏幕底部最后一行（\033[999B 下移 999 行，实际停在最后一行），
// 再上移一行到状态栏位置清除。这样无论光标此前在哪（工具框线内、思考中行、正文中间），
// clear 都只作用于屏幕底部状态栏行，不会清错正文行。
// 注意：清除后光标停留在状态栏行，调用方（追加式输出）会从该行覆盖新内容。
func (b *BubbleUI) clearStatusBar() {
	if b.IsTeaMode() {
		// tea 模式：状态栏由 tea 模型渲染，ANSI 状态栏完全退位。
		return
	}
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	if !b.statusVisible {
		return
	}
	if !b.statusBarShown {
		return
	}
	fmt.Print("\033[999B\033[1A\033[2K")
	os.Stdout.Sync()
	b.statusBarShown = false
}

// renderStatusBar 在屏幕底部渲染状态栏。
// 状态位驱动：仅当状态栏不在屏（!statusBarShown）时才执行上移输出，
// 避免连续 render 时重复上移叠字。渲染后置 statusBarShown = true。
//
// 定位：先把光标移到屏幕底部最后一行（\033[999B），再上移一行渲染状态栏。
// 多迭代场景下 OnToolCall 的 \033[u\033[J 会把光标恢复到"思考中"行（非输入提示符行），
// 若直接 \033[1A 会把状态栏画进工具框线内（叠字）；\033[999B 先归位到底部可避免。
// 渲染后光标停留在底部行（状态栏 \n 换行落到最后一行），与 runner 的 "> " 提示符衔接。
func (b *BubbleUI) renderStatusBar() {
	if b.IsTeaMode() {
		// tea 模式：状态栏由 tea 模型渲染，ANSI 状态栏完全退位。
		return
	}
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	if !b.statusVisible {
		return
	}
	if b.statusBarShown {
		// 已在屏：重绘内容。仍先 \033[999B 定位到底部，避免光标漂移（多迭代/后台余额）时画进正文。
		line := b.statusBarTextLocked()
		if line == "" {
			return
		}
		fmt.Printf("\033[999B\033[1A\033[2K%s\n", line)
		os.Stdout.Sync()
		return
	}
	line := b.statusBarTextLocked()
	if line == "" {
		return
	}
	fmt.Printf("\033[999B\033[1A%s\n", line)
	os.Stdout.Sync()
	b.statusBarShown = true
}

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// trimArgs 按 rune 截断文本，超出时追加 "..."。
func trimArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return args
}

// boxWidth 计算框线宽度：取内容最长行的字符宽度，限制在 [minWidth, 80] 范围。
// 阶段 1 的 ANSI 框线路径使用；tea 模式的框线生成见 box.go。
func boxWidth(content string, minWidth int) int {
	maxLen := minWidth
	for _, line := range strings.Split(content, "\n") {
		w := len([]rune(line))
		if w > maxLen {
			maxLen = w
		}
	}
	if maxLen > 80 {
		maxLen = 80
	}
	return maxLen
}
