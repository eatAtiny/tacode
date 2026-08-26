// tea 渲染程序生命周期：启动/投递/暂停/恢复 + tea 输入桥接。
//
// 字段（teaProgram/teaInputCh/teaMu）声明在 bubble.go 的 BubbleUI struct，
// 方法定义在本文件（同包，方法可定义在任意文件）。
package bubble

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// Start 启动 tea 渲染程序（阶段 2 起 BubbleUI 的主渲染循环）。
// 返回后 tea 在后台 goroutine 渲染，UI 事件通过 Program.Send 投递。
// 返回 cancel 函数：退出时调用以停止 tea 程序。
//
// 选项说明：
//   - tea.WithAltScreen()：对话区累积在 tea 模型内全屏渲染，正文与状态栏/输入框
//     由 tea 统一重绘。必须 alt screen——否则追加式长对话会超出主屏回滚区，
//     与阶段 1 的"滚动后状态栏错位"问题同源。
//   - 环境分支（按 stdout 是否 TTY）：
//     * TTY（真实终端）：标准渲染器 + 默认输入（tea 接管 raw 模式读键），
//       textinput 正常接收 KeyMsg，输入框可交互。
//     * 非 TTY（测试/CI/管道）：tea.WithInput(nil) + tea.WithoutRenderer()
//       ——tea v1 在非 TTY 下默认会 openInputTTY（/dev/tty），失败则 Run 返回错误；
//       显式禁输入 + headless 渲染让事件循环在测试环境完整运转（sendToTea 可用）。
//       此时 WithAltScreen 无效果（nilRenderer.altScreen 恒 false），安全。
func (b *BubbleUI) Start() (func(), error) {
	b.teaMu.Lock()
	if b.teaProgram != nil {
		b.teaMu.Unlock()
		return func() {}, nil // 已启动
	}
	opts := []tea.ProgramOption{tea.WithAltScreen()}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		// 非 TTY：headless 模式（见方法注释）。
		opts = append(opts, tea.WithInput(nil), tea.WithoutRenderer())
	}
	p := tea.NewProgram(b.teaModel(), opts...)
	b.teaProgram = p
	b.teaMu.Unlock()

	go func() {
		if _, err := p.Run(); err != nil {
			// tea 运行错误：记录（stderr），并回退阶段 1 渲染。
			// 可能原因：外部信号 Kill、initTerminal 失败等。
			fmt.Fprintf(os.Stderr, "bubbletea run error: %v\n", err)
		}
		// Run 返回（正常退出/崩溃）后清空 teaProgram：IsTeaMode 回退 false，
		// 阶段 1 ANSI 路径恢复，sendToTea 安全丢弃——避免"tea 崩溃后 teaProgram
		// 保持非 nil → 阶段 1 永久退位 + 事件全丢 → 冻结屏"。
		b.teaMu.Lock()
		b.teaProgram = nil
		b.teaMu.Unlock()
	}()

	return func() {
		b.teaMu.Lock()
		p := b.teaProgram
		if p != nil {
			p.Quit() // 优雅退出：Quit 是 no-op if not running，安全
		}
		b.teaProgram = nil
		b.teaMu.Unlock()
	}, nil
}

// sendToTea 把消息投递给 tea 模型（非阻塞，tea 未启动时丢弃）。
// 可能从多个 goroutine 调用（queryEngine 事件流 + 后台余额查询），teaProgram
// 读写走 teaMu 锁；Program.Send 自身在程序退出后安全丢弃（channel 关闭）。
func (b *BubbleUI) sendToTea(msg tea.Msg) {
	b.teaMu.Lock()
	p := b.teaProgram
	b.teaMu.Unlock()
	if p != nil {
		p.Send(msg)
	}
}

// IsTeaMode 是否处于 tea 渲染模式（Start 已调用且未取消）。
// tea 模式下阶段 1 的 ANSI 直写路径（clearStatusBar/renderStatusBar/OnThink
// 等）全部退位为 no-op，避免与 tea 全屏渲染冲突。
// Run 崩溃/退出后自动回退 false（见 Start）。
func (b *BubbleUI) IsTeaMode() bool {
	b.teaMu.Lock()
	started := b.teaProgram != nil
	b.teaMu.Unlock()
	return started
}

// PauseTea 暂停 tea 渲染程序并释放终端（退出 alt screen、恢复终端状态）。
// 用于 /list 等前台运行其他全屏程序（独立 tea 程序）的场景：
// 两个 tea 程序共享一个终端，主程序不暂停则选择器渲染被 alt screen 遮蔽且输入被抢占。
// 未启动/已暂停时安全 no-op（返回 nil）。Task 8 将进一步融合 /list。
func (b *BubbleUI) PauseTea() error {
	b.teaMu.Lock()
	p := b.teaProgram
	b.teaMu.Unlock()
	if p == nil {
		return nil
	}
	return p.ReleaseTerminal()
}

// ResumeTea 恢复被 PauseTea 暂停的 tea 渲染程序（重进 alt screen、重启渲染）。
// 未启动时安全 no-op。
func (b *BubbleUI) ResumeTea() error {
	b.teaMu.Lock()
	p := b.teaProgram
	b.teaMu.Unlock()
	if p == nil {
		return nil
	}
	return p.RestoreTerminal()
}

// submitInput 把用户提交的输入桥接给 Runner（写入 tea 输入专用 channel）。
//
// 阶段 2 起输入由 tea 接管，tea 输入与 raw 路径完全隔离：
//   - 写 b.teaInputCh（NewBubbleUI 创建，永不关闭），不经 ReadInputChan/rawInputLoop，
//     避免 raw loop 在非 TTY 下 term.MakeRaw 失败退出 → defer close(inputChan) 与发送竞态
//     （go test -race 曾检测到 send vs close 的 data race，且 raw loop 退出后发送会 panic）。
//   - 缓冲满（Runner 未及时消费）时丢弃输入，避免阻塞 tea 事件循环。
func (b *BubbleUI) submitInput(value string) {
	select {
	case b.teaInputCh <- value:
	default:
		// 缓冲满（Runner 未及时消费）时丢弃，避免阻塞 tea 事件循环。
	}
}

// ReadTeaInputChan 返回 tea 输入桥接 channel（阶段 2 起 Runner 从此读用户输入）。
func (b *BubbleUI) ReadTeaInputChan() <-chan string {
	return b.teaInputCh
}
