// Package bubble 提供基于 Lip Gloss + Glamour 的终端 UI 实现。
//
// BubbleUI 是全 tea 渲染聊天界面（ChatModel）的包装器（回退阶段 2 后重启的
// tea 主渲染方案，替代追加式主屏）：
//   - 主渲染：ChatModel（chat.go）用 viewport + textarea + footer 全屏渲染，
//     输出不写 os.Stdout（纯 tea 渲染，无 ANSI 光标控制、无 Glamour 直出）
//   - 主输入：textarea 接管输入（Enter 提交 → submitCh → Runner）
//   - 事件转发：BubbleUI 的 UI 接口方法（OnThink/OnDelta/...）→ Program.Send
//     投递消息 → ChatModel.Update 追加对话行并刷新 viewport
//   - 会话选择器：独立 Bubble Tea 全屏程序（/list 时前台运行）
//
// 核心特性：
//   - 流式文本：delta 经 chatDeltaMsg 追加对话区（逐 token 增量显示）
//   - Markdown 渲染：最终回答经 Glamour 渲染为终端友好的格式
//   - 框线输出：工具调用和结果用 box.go 的 Unicode 框线字符绘制
//   - 权限确认：提示进对话区 + 输入经 textarea 提交流转（Runner 查询运行时
//     转发到 inputForward，ConfirmPermission 从该 channel 读取）
//
// 架构调整背景（2026-08）：追加式主屏（rawInputLoop 逐 rune 输入 + ANSI 光标
// 控制）实测体验不佳，方案确定为全 tea 渲染聊天界面（参照 j178/chatgpt），
// 本文件从追加式输出核心重写为 ChatModel 包装器。raw 输入（rawInputLoop）、
// ANSI 光标控制、termios OPOST 恢复等追加式基础设施全部删除。
// 框线（box.go）保留——ChatModel 的工具消息渲染复用。
//
// 文件组织：
//   - bubble.go  tea 包装器（事件方法 → Program.Send、输入桥接、生命周期）
//   - chat.go    ChatModel（全 tea 聊天界面：viewport + textarea + footer）
//   - box.go     工具框线共享渲染
package bubble

import (
	"fmt"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// BubbleUI 是 UI 接口的终端美化实现（全 tea 聊天界面包装器）。
//
// 职责边界：
//   - BubbleUI：UI 接口适配层——事件方法收 agent 事件 → Program.Send 投递；
//     不持有任何渲染状态（对话内容、输入框、滚动位置全在 ChatModel）
//   - ChatModel：tea.Model 实现——持有对话区（viewport）/ 输入框（textarea）/
//     footer，View 渲染整屏，Update 响应事件消息与按键
//
// 线程安全：事件方法可能被多个 goroutine 调用（queryLoop 事件推送、
// 后台余额查询、后台记忆保存），send 用 uiMu 串行化 Program.Send 调用
// （Program.Send 本身线程安全，锁是防御性保证字段读写的可见性）。
type BubbleUI struct {
	// program 聊天 TUI 渲染程序（Start 时创建，tea.Program 全屏运行）。
	program *tea.Program
	// chat 聊天模型（viewport+textarea+footer 全 tea 界面）。
	chat *ChatModel
	// submitCh 用户提交 channel（ChatModel.submitCh → Runner.ReadInputChan）。
	submitCh <-chan string
	// runDone 标记 tea 程序已完全退出（Run goroutine 结束）：
	// Close 用它同步等待，确保 ChatModel 不再被事件循环写入（-race 验证）。
	runDone chan struct{}
	// uiMu 并发保护（后台 goroutine 调事件方法时串行化 send）。
	uiMu sync.Mutex
}

// NewBubbleUI 创建 BubbleUI 实例。
// 注意：此时不创建 tea.Program——Start() 时才启动（与 Runner.Run 时序解耦）。
func NewBubbleUI() *BubbleUI {
	chat := NewChatModel()
	return &BubbleUI{
		chat:     chat,
		submitCh: chat.SubmitCh(),
	}
}

// Start 启动聊天 TUI（tea.Program 后台运行，Run 主循环不阻塞）。
// opts 是可选的额外 tea 程序选项（测试注入 WithInput/WithoutRenderer 等）。
// runDone 随每次 Start 重建（支持 Close 后重启，避免 close 已关闭 channel）。
func (b *BubbleUI) Start(opts ...tea.ProgramOption) error {
	b.uiMu.Lock()
	if b.program != nil {
		b.uiMu.Unlock()
		return nil // 已启动，幂等
	}
	done := make(chan struct{})
	// 启用鼠标（CellMotion：滚轮/移动事件），让 viewport 对话区支持滚轮滚动历史。
	p := tea.NewProgram(b.chat, append([]tea.ProgramOption{tea.WithMouseCellMotion()}, opts...)...)
	b.program = p
	b.runDone = done
	b.uiMu.Unlock()

	go func() {
		defer close(done)
		if _, err := p.Run(); err != nil {
			// tea 运行错误：打印到 stderr（聊天下方已恢复终端，
			// 不污染对话区）。正常退出（Quit）返回 nil，忽略。
			fmt.Fprintf(os.Stderr, "tea program error: %v\n", err)
		}
	}()
	return nil
}

// Close 退出 tea 程序并等待其完全退出（tea.Program.Quit 幂等）。
// 等待保证 ChatModel 不再被事件循环写入，之后读取对话区是安全的。
// 可重复调用；重复调用时 program 已为 nil，直接返回。
func (b *BubbleUI) Close() error {
	b.uiMu.Lock()
	program := b.program
	runDone := b.runDone
	b.program = nil
	b.uiMu.Unlock()

	if program != nil {
		program.Quit()
	}
	if runDone != nil {
		<-runDone // 等待 Run goroutine 退出（事件循环停止处理消息）
	}
	return nil
}

// ──────────────────────────────────────────────────────────
// UI 接口实现：输入组
// ──────────────────────────────────────────────────────────

// ReadInput 同步读取用户输入。
// 聊天界面下输入由 ChatModel 的 textarea 接管，ReadInput 语义化为
// 阻塞等待一次提交（从 submitCh 读取）。
func (b *BubbleUI) ReadInput() (string, error) {
	input, ok := <-b.submitCh
	if !ok {
		return "", fmt.Errorf("EOF")
	}
	return input, nil
}

// ReadInputChan 返回用户输入 channel（聊天界面提交桥接）。
// ChatModel 的 textarea 提交（Enter）写入 chat.submitCh，
// NewBubbleUI 直接把 chat.submitCh 作为 submitCh 返回，无需额外转发。
// channel 在 ChatModel 生命周期内保持打开（不关闭）。
func (b *BubbleUI) ReadInputChan() <-chan string {
	return b.submitCh
}

// ──────────────────────────────────────────────────────────
// UI 接口实现：事件通知组
// ──────────────────────────────────────────────────────────

// OnThink 通知新一轮思考开始。
// 投递 chatThinkMsg，ChatModel 在对话区追加 "⏳ 思考中..." 行。
func (b *BubbleUI) OnThink(iteration int) {
	b.send(chatThinkMsg{iteration: iteration})
}

// OnDelta 输出流式增量文本。
// 投递 chatDeltaMsg，ChatModel 把增量追加到对话区。
func (b *BubbleUI) OnDelta(content string) {
	b.send(chatDeltaMsg{content: content})
}

// OnToolCall 显示工具调用请求。
// 投递 chatToolCallMsg，ChatModel 复用 box.go 的 toolCallBox 绘制框线。
func (b *BubbleUI) OnToolCall(name, args string) {
	b.send(chatToolCallMsg{name: name, args: args})
}

// OnToolResult 显示工具执行结果。
// 投递 chatToolResultMsg，ChatModel 复用 box.go 的 toolResultBox 绘制框线。
func (b *BubbleUI) OnToolResult(name, result string, isError bool) {
	b.send(chatToolResultMsg{name: name, result: result, isError: isError})
}

// OnContinue 通知继续推理。
// 投递 chatContinueMsg，ChatModel 追加 "🔄 继续推理" 行。
func (b *BubbleUI) OnContinue(iteration int) {
	b.send(chatContinueMsg{iteration: iteration})
}

// OnFinal 显示最终回答。
// 投递 chatFinalMsg（含 token 统计），ChatModel 用 Glamour 渲染 Markdown
// 追加对话区，并展示 "⚡ 本轮 N tokens" 统计行。
func (b *BubbleUI) OnFinal(answer string, inputTokens, outputTokens, totalTokens int) {
	b.send(chatFinalMsg{
		content:      answer,
		inputTokens:  inputTokens,
		outputTokens: outputTokens,
		totalTokens:  totalTokens,
	})
}

// OnError 打印错误信息。
// 投递 chatErrorMsg，ChatModel 用红色加粗样式追加错误行。
func (b *BubbleUI) OnError(err error) {
	b.send(chatErrorMsg{err: err})
}

// OnMessage 打印一般性消息。
// 投递 chatMessageMsg，ChatModel 原样追加一行。
func (b *BubbleUI) OnMessage(msg string) {
	b.send(chatMessageMsg{content: msg})
}

// ShowBalance 展示账户余额。
// 投递 chatBalanceMsg，ChatModel 用次要样式追加余额行。
// 注意：可能在后台 goroutine 调用（query_engine 每轮余额查询），
// send 经 uiMu 串行化 + Program.Send 线程安全。
func (b *BubbleUI) ShowBalance(line string) {
	b.send(chatBalanceMsg{balance: line})
}

// ConfirmPermission 显示权限确认提示，等待用户输入。
//
// 聊天界面下的确认输入流转（链路）：
//
//	textarea 提交 → ChatModel.submitCh → Runner.Run() 主循环
//	  → queryRunning 分支转发到 inputForward（查询运行中非 nil）
//	  → ConfirmPermission 从 inputForward 读取
//
// 提示先发进对话区（chatMessageMsg），用户据此在 textarea 输入 y/N 回车。
// inputForward 为 nil（无运行中查询，理论不发生）时回退 ReadInputChan。
func (b *BubbleUI) ConfirmPermission(tool, args, reason string, inputForward <-chan string) (bool, error) {
	// 提示进对话区（用户需在 textarea 输入确认，不提示会丢失上下文）。
	msg := fmt.Sprintf("⚠️ 权限确认: %s（参数: %s）允许? y/N", tool, args)
	if reason != "" {
		msg = fmt.Sprintf("%s\n原因: %s", msg, reason)
	}
	b.send(chatMessageMsg{content: msg})

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
// 全 tea 界面下不打印 ASCII banner（对话区已由 View 渲染 footer），
// 改为在对话区追加一行欢迎信息（模型名），保持启动即有反馈。
func (b *BubbleUI) Welcome(model string) {
	b.send(chatMessageMsg{content: fmt.Sprintf("欢迎使用 agentic！模型: %s", model)})
}

// ──────────────────────────────────────────────────────────
// 内部方法
// ──────────────────────────────────────────────────────────

// send 投递消息到 tea 程序。
// Program.Send 线程安全（msgs 无缓冲，事件循环取走后返回，或 ctx 已取消
// 时直接返回），uiMu 用于串行化事件方法与 Start/Close 的并发
// （避免 Send 与 Quit 竞态）。program 为 nil（Start 前）时消息丢弃，不阻塞。
func (b *BubbleUI) send(msg tea.Msg) {
	b.uiMu.Lock()
	defer b.uiMu.Unlock()
	if b.program != nil {
		b.program.Send(msg)
	}
}
