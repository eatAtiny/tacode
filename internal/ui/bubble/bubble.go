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

// ──────────────────────────────────────────────────────────
// BubbleUI — 混合模式 UI
//
// 主循环：bufio.Scanner 读输入 + fmt.Println 输出（Claude Code 风格）
// 会话选择器：Bubble Tea 交互式组件
// ──────────────────────────────────────────────────────────

type BubbleUI struct {
	// 子组件（用于会话选择器等 Bubble Tea 交互场景）
	conversation *components.ConversationModel
	input        components.InputModel
	status       components.StatusModel
	toolView     *components.ToolViewModel

	// 元数据
	sessionName string
	model       string

	// token 统计（本轮）
	inputTokens  int
	outputTokens int

	// 流式状态
	hasDelta    bool // 本轮是否收到过 delta
	cursorSaved bool // 是否已保存光标（流式文本已显示）

	// markdown 渲染器
	glamour *glamour.TermRenderer

	// 异步输入
	inputChan chan string
	inputOnce sync.Once
}

// NewBubbleUI 创建 BubbleUI 实例。
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

// Close 关闭 UI，释放资源。
func (b *BubbleUI) Close() error {
	return nil
}

// ──────────────────────────────────────────────────────────
// 样式定义
// ──────────────────────────────────────────────────────────

var (
	styleUserPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("14")).Bold(true)
	styleThink      = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Italic(true)
	styleError      = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleMuted      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleSuccess    = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSeparator  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleToolPrefix = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
)

// ──────────────────────────────────────────────────────────
// UI 接口实现
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) ReadInput() (string, error) {
	fmt.Print(styleUserPrefix.Render("> "))
	ch := b.ReadInputChan()
	input, ok := <-ch
	if !ok {
		return "", fmt.Errorf("EOF")
	}
	return input, nil
}

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

func (b *BubbleUI) OnThink(_ int) {
	fmt.Println()
	// 保存光标位置，用于 OnFinal/OnToolCall 清除流式文本。
	fmt.Print("\033[s")
	b.cursorSaved = true
	b.hasDelta = false
	fmt.Println(styleThink.Render("  ⏳ 思考中..."))
}

func (b *BubbleUI) OnDelta(content string) {
	b.hasDelta = true
	// 浅色直接输出流式文本。
	fmt.Print(styleThink.Render(content))
}

func (b *BubbleUI) OnToolCall(name, args string) {
	// 清除流式文本。
	if b.cursorSaved {
		fmt.Print("\033[u\033[J")
		os.Stdout.Sync()
		b.cursorSaved = false
		b.hasDelta = false
	}
	// 格式化参数 JSON。
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

	// 计算框宽度。
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

func (b *BubbleUI) OnContinue(iteration int) {
	b.hasDelta = false
	b.cursorSaved = false
	fmt.Println(styleThink.Render(fmt.Sprintf("  🔄 Continuing... (iteration %d)", iteration)))
}

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
		for _, line := range strings.Split(answer, "\n") {
			fmt.Printf("  %s\n", line)
		}
	}
	b.hasDelta = false
	fmt.Println()
	fmt.Println(styleSeparator.Render(strings.Repeat("─", 60)))
}

func (b *BubbleUI) OnError(err error) {
	fmt.Println(styleError.Render(fmt.Sprintf("❌ Error: %v", err)))
}

func (b *BubbleUI) OnMessage(msg string) {
	fmt.Println(msg)
}

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

// ──────────────────────────────────────────────────────────
// 元数据设置
// ──────────────────────────────────────────────────────────

func (b *BubbleUI) SetSessionName(name string) {
	b.sessionName = name
}

func (b *BubbleUI) SetModel(model string) {
	b.model = model
}

func (b *BubbleUI) UpdateTokens(input, output int) {
	b.inputTokens += input
	b.outputTokens += output
}

func (b *BubbleUI) ResetTokens() {
	b.inputTokens = 0
	b.outputTokens = 0
}

// 辅助函数
func trimArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return args
}

// boxWidth 计算框线宽度：取最长行的显示宽度，限制在 [minWidth, maxWidth] 范围。
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
