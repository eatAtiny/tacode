// Package bubble 提供基于 Lip Gloss + Glamour 的终端 UI 实现。
//
// BubbleUI 采用混合模式：
//   - 主循环使用 bufio.Scanner 读输入 + fmt.Println 输出（Claude Code 风格）
//   - 会话选择器使用 Bubble Tea 交互式组件
//
// 核心特性：
//   - 流式文本：通过 ANSI 光标控制实现"先显示思考中，再实时追加增量文本，
//     最后清除流式文本替换为最终回答"
//   - Markdown 渲染：最终回答通过 Glamour 渲染为终端友好的格式
//   - 框线输出：工具调用和结果用 Unicode 框线字符绘制
//   - 权限确认：通过 inputForward channel 读取用户 y/N 决策
//
// 注意：BubbleUI 直接写 os.Stdout，不通过 Bubble Tea 的渲染管线。
// 仅在 /list 命令时临时切换到 Bubble Tea 程序。
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
)

// BubbleUI 是 UI 接口的终端美化实现。
//
// 混合架构：
//   - 主输出：直接写 os.Stdout，用 ANSI 转义序列控制光标
//   - 会话选择器：Bubble Tea 全屏程序（仅在 /list 时运行）
//   - 子组件：保留 Bubble Tea 组件引用（conversation、input、status、toolView），
//     但主循环不使用它们的渲染管线，仅用于 /list 等交互场景
//
// 流式输出机制：
//  1. OnThink 保存光标位置（\033[s）
//  2. 多次 OnDelta 输出增量文本
//  3. OnFinal 恢复光标位置（\033[u），清除后面内容（\033[J），渲染最终回答
//  4. 如果中间出现 OnToolCall，同样恢复光标清除流式文本
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
		conversation: components.NewConversationModel(),
		input:        components.NewInputModel(),
		status:       components.NewStatusModel(),
		toolView:     components.NewToolViewModel(),
		glamour:      glamourRenderer,
	}
}

// Close 关闭 UI，释放资源。目前无持久资源需要释放，返回 nil。
func (b *BubbleUI) Close() error {
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
// UI 接口实现
// ──────────────────────────────────────────────────────────

// ReadInput 同步读取用户输入。打印提示符 "> " 后阻塞等待。
// 内部复用 ReadInputChan 的 channel。
func (b *BubbleUI) ReadInput() (string, error) {
	fmt.Print(styleUserPrefix.Render("> "))
	ch := b.ReadInputChan()
	input, ok := <-ch
	if !ok {
		return "", fmt.Errorf("EOF")
	}
	return input, nil
}

// ReadInputChan 返回异步输入 channel。
// 首次调用时启动后台 goroutine，使用 bufio.Scanner 持续读取 os.Stdin。
// 后续调用返回同一个 channel（通过 sync.Once 保证）。
// channel 在输入流结束（EOF）时关闭。
func (b *BubbleUI) ReadInputChan() <-chan string {
	b.inputOnce.Do(func() {
		b.inputChan = make(chan string, 1)
		go func() {
			defer close(b.inputChan)
			scanner := bufio.NewScanner(os.Stdin)
			for scanner.Scan() {
				input := strings.TrimSpace(scanner.Text())
				b.inputChan <- input
			}
		}()
	})
	return b.inputChan
}

// OnThink 通知新一轮思考开始。
// 打印空行后保存光标位置（\033[s），然后显示 "⏳ 思考中..."。
// cursorSaved 标记和 hasDelta 标记被设置，为后续可能的流式输出做准备。
func (b *BubbleUI) OnThink(_ int) {
	fmt.Println()
	// 保存光标位置：后续 OnDelta 的流式文本从此位置开始输出，
	// OnFinal 或 OnToolCall 时从此位置恢复并清除。
	fmt.Print("\033[s")
	b.cursorSaved = true
	b.hasDelta = false
	fmt.Println(styleThink.Render("  ⏳ 思考中..."))
}

// OnDelta 输出流式增量文本。
// 直接打印 content（不换行、不加前缀），用灰色斜体样式。
// 首次调用时设置 hasDelta = true，标记本轮有流式内容。
func (b *BubbleUI) OnDelta(content string) {
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
}

// OnToolResult 显示工具执行结果。
// 超过 15 行的输出会被截断。成功用绿色 "✅ 结果"，失败用红色 "❌ 错误"。
// 框线样式与 OnToolCall 保持一致。
func (b *BubbleUI) OnToolResult(name, result string, isError bool) {
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
}

// OnContinue 通知继续推理。清除流式状态，打印继续提示。
func (b *BubbleUI) OnContinue(iteration int) {
	b.hasDelta = false
	b.cursorSaved = false
	fmt.Println(styleThink.Render(fmt.Sprintf("  🔄 Continuing... (iteration %d)", iteration)))
}

// OnFinal 显示最终回答。
// 1. 恢复光标位置并清除流式文本区域
// 2. 如果之前有流式文本，打印 "✅ 思考完成"
// 3. 用 Glamour 渲染 Markdown（带 2 空格缩进），失败则回退纯文本
// 4. 打印分隔线标记本轮结束
func (b *BubbleUI) OnFinal(answer string) {
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
	fmt.Println(styleSeparator.Render(strings.Repeat("─", 60)))
}

// OnError 打印错误信息（红色加粗）。
func (b *BubbleUI) OnError(err error) {
	fmt.Println(styleError.Render(fmt.Sprintf("❌ Error: %v", err)))
}

// OnMessage 打印一般性消息（无额外样式）。
func (b *BubbleUI) OnMessage(msg string) {
	fmt.Println(msg)
}

// ConfirmPermission 显示权限确认提示，等待用户输入。
// 提示格式："⚠️ 权限确认: <tool>" + 参数 + "允许执行? [y/N] "。
// 如果 inputForward 不为 nil，从此 channel 读取用户输入；
// 否则从 ReadInputChan 读取。
// 返回 true 表示用户输入 "y" 或 "yes"（大小写不敏感）。
func (b *BubbleUI) ConfirmPermission(tool, args string, inputForward <-chan string) (bool, error) {
	fmt.Printf("\n%s %s\n", styleThink.Render("⚠️  权限确认:"), tool)
	fmt.Printf("  参数: %s\n", styleMuted.Render(args))
	fmt.Print(styleUserPrefix.Render("  允许执行? [y/N] "))

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
// 元数据设置
// ──────────────────────────────────────────────────────────

// SetSessionName 设置当前会话的显示名称。
func (b *BubbleUI) SetSessionName(name string) {
	b.sessionName = name
}

// SetModel 设置当前使用的模型名称。
func (b *BubbleUI) SetModel(model string) {
	b.model = model
}

// UpdateTokens 累计本轮 token 用量。
func (b *BubbleUI) UpdateTokens(input, output int) {
	b.inputTokens += input
	b.outputTokens += output
}

// ResetTokens 重置本轮 token 计数（新一轮查询开始前调用）。
func (b *BubbleUI) ResetTokens() {
	b.inputTokens = 0
	b.outputTokens = 0
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
