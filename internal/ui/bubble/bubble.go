// Package bubble 提供基于 Lip Gloss + Glamour 的终端 UI 实现。
//
// BubbleUI 是 inline 聊天界面（ChatModel）的 tea 包装器：
//   - 主渲染：ChatModel（chat.go）的 View 只渲染活区（查询状态行 + 权限
//     确认弹层 + textarea 输入框 + footer），原地重绘
//   - 定稿管线：对话内容经 commit → tea.Println 打印于活区上方，滚入终端
//     原生 scrollback；流式逐段定稿（增量遇换行冲刷）
//   - 无 alt screen、无鼠标捕获：终端原生选择/复制/滚轮滚动全部保留
//   - 主输入：textarea 接管输入（Enter 提交 → submitCh → Runner）
//   - 事件转发：BubbleUI 的 UI 接口方法（OnThink/OnDelta/...）→ Program.Send
//     投递消息 → ChatModel.Update 定稿对话内容
//   - 会话选择器：聊天 TUI 内融合运行（/list 经 chatPickerMsg 切活区渲染）
//
// 核心特性：
//   - 流式文本：delta 增量缓冲，遇换行切段定稿（逐段上屏）
//   - Markdown 渲染：最终回答经 Glamour 渲染为终端友好的格式
//   - 框线输出：工具调用和结果用 box.go 的 Unicode 框线字符绘制
//   - 权限确认：输入框上方模态弹层（↑↓/Enter 或 y/n 按键直接消费，答案经
//     ChatModel.permissionDone 回传，不经过 textarea 与文本输入流）
//
// 文件组织：
//   - bubble.go   tea 包装器（事件方法 → Program.Send、输入桥接、生命周期）
//   - chat.go     ChatModel（inline 聊天界面：活区渲染 + commit 定稿管线）
//   - box.go      工具框线共享渲染
//   - welcome.go  欢迎界面渲染（ASCII logo + 版本/模型/目录 + 使用提示）
package bubble

import (
	"fmt"
	"os"
	"sync"

	"tacode/internal/memory"
	"tacode/internal/session"

	tea "github.com/charmbracelet/bubbletea"
)

// BubbleUI 是 UI 接口的终端美化实现（inline 聊天界面包装器）。
//
// 职责边界：
//   - BubbleUI：UI 接口适配层——事件方法收 agent 事件 → Program.Send 投递；
//     不持有任何渲染状态（活区组件、转录、流式缓冲全在 ChatModel）
//   - ChatModel：tea.Model 实现——持有活区组件（textarea）与流式/转录
//     状态（streamBuf/m.lines），footer 由 renderFooter 即时渲染（无独立
//     组件），View 渲染活区，Update 响应事件消息与按键
//
// 线程安全：事件方法可能被多个 goroutine 调用（queryLoop 事件推送、
// 后台余额查询、后台记忆保存），send 用 uiMu 串行化 Program.Send 调用
// （Program.Send 本身线程安全，锁是防御性保证字段读写的可见性）。
type BubbleUI struct {
	// program 聊天 TUI 渲染程序（Start 时创建，inline 渲染不进 alt screen）。
	program *tea.Program
	// chat 聊天模型（活区渲染 + commit 定稿管线）。
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
	// 不开鼠标捕获、不进 alt screen（inline 渲染）：
	// 终端原生选择/复制/滚轮滚动全部保留，对话经 tea.Println 流入原生 scrollback。
	p := tea.NewProgram(b.chat, opts...)
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
// 投递 chatThinkMsg，ChatModel 更新活区状态行（"⏳ 思考中"）。
func (b *BubbleUI) OnThink(iteration int) {
	b.send(chatThinkMsg{iteration: iteration})
}

// OnDelta 输出流式增量文本。
// 投递 chatDeltaMsg，ChatModel 把增量累积进流式缓冲，遇换行切段定稿（tea.Println）。
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
// 投递 chatContinueMsg，ChatModel 更新活区状态行（"🔄 继续推理"）。
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
// 投递 chatBalanceMsg，ChatModel 存字段并在 footer 状态栏常驻显示。
// 注意：可能在后台 goroutine 调用（query_engine 每轮余额查询），
// send 经 uiMu 串行化 + Program.Send 线程安全。
func (b *BubbleUI) ShowBalance(line string) {
	b.send(chatBalanceMsg{balance: line})
}

// UpdateContext 更新上下文占用。
// 投递 chatContextMsg，ChatModel 存字段并在 footer 状态栏显示（已用/总/百分比）。
// usedChars 与 contextCharLimit 均为字符口径（与 Compactor 压缩界限一致）。
func (b *BubbleUI) UpdateContext(usedChars, contextCharLimit int) {
	b.send(chatContextMsg{usedChars: usedChars, contextLimit: contextCharLimit})
}

// RunSessionPicker 在聊天 TUI 内运行会话选择器（/list 融合，不另起 tea 程序）。
// 发 chatPickerMsg 让 ChatModel 切到选择模式，阻塞等待用户选择结果。
// 返回选中会话 ID（空串=取消）。与 TextUI 的独立程序模式接口一致。
func (b *BubbleUI) RunSessionPicker(sessions []session.SessionMeta, activeID string) (string, error) {
	if len(sessions) == 0 {
		return "", fmt.Errorf("没有可用的会话")
	}
	if b.program == nil {
		// tea 未启动（异常）：回退独立程序模式。
		return session.RunSessionPicker(sessions, activeID)
	}
	b.send(chatPickerMsg{sessions: sessions, activeID: activeID})
	selected := <-b.chat.pickerDone
	return selected, nil
}

// ShowHistory 展示会话历史（切换会话后调用）。
// 把历史事件渲染为结构化对话行（用户消息/助手回答/工具框线），
// 复用 ChatModel 的对话区渲染——历史与当前对话同风格，而非原始 JSON。
func (b *BubbleUI) ShowHistory(events []memory.Event) {
	var history []string
	for _, e := range events {
		switch e.Type {
		case memory.EventUser:
			// 完整显示用户消息（不截断）。
			history = append(history, b.chat.renderUser(e.Content))
		case memory.EventAssistant:
			// 完整显示助手回答（agent 最终输出，不截断）。
			history = append(history, b.chat.renderAssistant()+e.Content)
		case memory.EventToolUse:
			for _, tc := range e.ToolCalls {
				history = append(history, toolCallBox(tc.Name, tc.Arguments))
			}
		case memory.EventToolResult:
			// 工具结果用 toolResultBox（15 行截断——工具结果本就不该全量展示）。
			history = append(history, toolResultBox(e.ToolName, e.ToolResult, e.IsError))
		}
	}
	b.send(chatHistoryMsg{events: history})
}

// ConfirmPermission 显示权限确认弹层，阻塞等待用户决定。
//
// 弹层是模态的：chatPermissionMsg 让 ChatModel 进入权限模式，此后所有按键
// 由弹层消费（不进入 textarea），决定经 ChatModel.permissionDone 回传。
// 答案全程不经过文本输入流——因此不存在「用户正在打的消息被当作 y/N 吞掉」
// 的歧义，主循环也无须判断某行输入是权限答案还是普通消息。
func (b *BubbleUI) ConfirmPermission(tool, args, reason string) (bool, error) {
	if b.program == nil {
		// tea 未启动（异常）：没有任何途径能让用户作出决定，不能默认放行
		// 一个高危操作。
		return false, fmt.Errorf("聊天界面未启动，无法确认权限")
	}
	b.send(chatPermissionMsg{tool: tool, args: args, reason: reason})
	return <-b.chat.permissionDone, nil
}

// Welcome 打印启动横幅。
// inline 界面下经 commit 定稿一个 Claude Code 风格的多行欢迎界面：
// ASCII logo + 欢迎语 + 版本/模型/目录 + 使用提示（追加进转录）。
// 投递 chatWelcomeMsg。
func (b *BubbleUI) Welcome(model string) {
	cwd, _ := os.Getwd()
	b.send(chatWelcomeMsg{content: welcomeBanner(model, version, cwd)})
}

// version 当前版本号（欢迎界面展示）。
const version = "v0.1"

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
