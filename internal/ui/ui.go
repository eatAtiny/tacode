// Package ui 定义了 Agent 与用户交互的抽象接口。
//
// 设计目标：
//   - 可插拔：通过 UI 接口，可以切换不同的交互实现（终端 TUI、Web、headless 等）
//   - 关注点分离：Agent 核心逻辑不关心 UI 细节，只通过接口方法通信
//   - 两种内置实现：BubbleUI（终端美化）和 TextUI（headless 回调模式）
//
// 接口方法按职责分为四组：
//  1. 输入组：ReadInput / ReadInputChan — 读取用户输入
//  2. 事件通知组：OnThink / OnDelta / OnToolCall / OnToolResult / OnContinue / OnFinal / OnError / OnMessage / ShowBalance
//  3. 交互组：ConfirmPermission — 权限确认
//  4. 生命周期组：Welcome / Close
package ui

import (
	"agentic/internal/memory"
	"agentic/internal/session"
)

// UI 定义了 Agent 与用户交互的接口。
// 所有 UI 操作都通过此接口完成，Agent 核心不直接操作终端或 Web。
//
// 实现者需要处理以下场景：
//   - 流式输出：OnThink → 多次 OnDelta → OnFinal（或 OnToolCall）
//   - 工具调用：OnToolCall → OnToolResult → OnContinue → 重复或结束
//   - 错误处理：任何阶段都可能收到 OnError
//   - 权限确认：工具执行前可能触发 ConfirmPermission（阻塞等待用户决策）
//
// 并发安全：方法可能被不同 goroutine 调用（如 ReadInputChan 的后台读取
// 和 queryLoop 的事件推送），实现者需自行保证线程安全。
type UI interface {
	// ── 输入组 ──────────────────────────────────────────

	// ReadInput 读取用户输入，阻塞直到用户按下 Enter。
	// 返回去除首尾空白的输入字符串，或在 EOF 时返回错误。
	//
	// 与 ReadInputChan 的区别：
	//   - ReadInput：同步阻塞，适合简单的"一问一答"场景
	//   - ReadInputChan：异步 channel，适合需要 select 多路复用的主循环
	ReadInput() (string, error)

	// ReadInputChan 返回一个只读 channel，后台持续读取用户输入。
	// 首次调用启动后台 goroutine（通过 sync.Once 保证只启动一次），
	// 后续调用返回同一个 channel。channel 在输入流结束时关闭（EOF）。
	//
	// 使用场景：
	//   - Runner 的主循环通过 select 同时监听输入 channel 和查询结果 channel
	//   - 查询运行中可以继续接收 /stop 等控制命令
	ReadInputChan() <-chan string

	// ── 事件通知组 ──────────────────────────────────────

	// OnThink 通知 LLM 正在思考（开始新一轮推理）。
	// iteration 从 1 开始，表示当前是第几轮 ReAct 迭代。
	//
	// 典型实现：
	//   - BubbleUI：打印 "⏳ 思考中..." 并保存光标位置，为后续流式输出做准备
	//   - TextUI：通过 OnEvent 回调转发
	OnThink(iteration int)

	// OnDelta 流式输出增量文本。
	// LLM 每次返回一个 token 时调用，content 是增量片段（可能只有几个字符）。
	//
	// 典型实现：
	//   - BubbleUI：直接 fmt.Print 增量文本（不换行），配合 ANSI 光标控制
	//   - TextUI：通过 OnEvent 回调转发
	//
	// 生命周期：OnThink → 多次 OnDelta → OnFinal（或 OnToolCall 中断流式输出）
	OnDelta(content string)

	// OnToolCall 通知 LLM 请求调用工具。
	// name 是工具名称，args 是 JSON 格式的参数。
	//
	// 调用时机：queryLoop 收到 LLM 返回的 tool_calls 后立即调用。
	// 此时应中断流式输出显示（如果之前有 OnDelta 输出的话），转而展示工具调用信息。
	//
	// 后续事件：紧接着会收到 OnToolResult（工具执行结果）。
	OnToolCall(name, args string)

	// OnToolResult 通知工具执行结果。
	// name 是工具名称，result 是执行输出，isError 表示是否执行出错。
	//
	// 调用时机：工具执行完成后（无论成功或失败）。
	// 注意：工具执行出错时 isError=true，但 result 中已包含错误描述，
	// Agent 会继续推理而非终止。
	OnToolResult(name, result string, isError bool)

	// OnContinue 通知继续推理（工具执行完毕，进入下一轮）。
	// iteration 是下一轮的迭代次数。
	//
	// 调用时机：所有工具执行完毕后，queryLoop 准备进入下一轮时。
	// 此时可以清除流式状态，准备接收新一轮的 OnDelta。
	OnContinue(iteration int)

	// OnFinal 通知最终回答。
	// answer 是 LLM 的完整最终回答（Markdown 格式）。
	// inputTokens / outputTokens / totalTokens 是本轮累计的精确 token 用量
	// （来自 API usage，include_usage 开启），UI 可在答案后展示统计行。
	//
	// 调用时机：queryLoop 完成，LLM 不再需要调用工具时。
	// 这是每轮查询的终点，此后 Agent 回到空闲状态等待下一条用户输入。
	//
	// 典型实现：使用 Glamour 渲染 Markdown，添加分隔线标记本轮结束，
	// 可选展示 token 统计。
	OnFinal(answer string, inputTokens, outputTokens, totalTokens int)

	// OnError 通知错误。
	// err 可能来自 LLM 调用失败、工具执行异常、或业务逻辑错误。
	//
	// 注意：OnError 不会终止 Agent 运行，Agent 会继续等待下一条用户输入。
	// 致命错误由 runner.Run() 的返回值处理。
	OnError(err error)

	// OnMessage 输出一般性消息（成功提示、帮助信息、状态更新等）。
	// 与 OnError 的区别：OnMessage 是中性/正面消息，OnError 是错误消息。
	//
	// 使用场景：
	//   - 会话切换成功提示
	//   - 记忆提取状态
	//   - 压缩完成通知
	OnMessage(msg string)

	// ShowBalance 展示账户余额（每轮结束或 /balance 命令触发）。
	// line 是已格式化的单行文本，如 "💰 余额: ¥110.00（充值 ¥100.00 / 赠金 ¥10.00）"。
	ShowBalance(line string)

	// UpdateContext 更新上下文占用（每轮 Final 后 + 启动时触发，状态栏常驻展示）。
	// usedChars 是当前消息数组估算字符数，contextCharLimit 是压缩触发的字符上限
	// （与 Compactor 的 context_char_limit 同一口径）。contextCharLimit 为 0 时
	// UI 应跳过展示（数据未就绪）。
	UpdateContext(usedChars, contextCharLimit int)

	// RunSessionPicker 运行交互式会话选择器（/list 命令）。
	// 返回用户选中的会话 ID；空串表示取消。
	// 实现差异：BubbleUI 融合进聊天 TUI 内渲染（不另起程序）；TextUI 独立程序。
	RunSessionPicker(sessions []session.SessionMeta, activeID string) (string, error)

	// ShowHistory 展示会话历史（切换会话后调用）。
	// 实现差异：BubbleUI 渲染为结构化对话（用户消息/助手回答/工具框线）；
	// TextUI 转文本行。
	ShowHistory(events []memory.Event)

	// ── 交互组 ──────────────────────────────────────────

	// ConfirmPermission 请求用户确认权限。
	// tool 是工具名称，args 是 JSON 格式的参数，reason 是需要确认的原因。
	// inputForward 是输入转发 channel：实现应从该 channel 读取用户确认输入
	// （而非从 ReadInputChan），这样 Runner 可以在确认期间继续接收控制命令。
	//
	// 返回值：true 表示允许执行，false 表示拒绝。
	//
	// 调用时机：权限检查器对工具返回 "confirm" 动作时。
	// 此方法是阻塞的——queryLoop 会等待确认结果后才继续执行。
	//
	// inputForward 说明：
	//   - 不为 nil 时，从此 channel 读取用户输入（与主循环共享输入流）
	//   - 为 nil 时，实现应自行读取输入（如直接调用 ReadInput）
	ConfirmPermission(tool, args, reason string, inputForward <-chan string) (bool, error)

	// ── 生命周期组 ──────────────────────────────────────

	// Welcome 打印启动欢迎信息。
	// model 是当前使用的 LLM 模型名称。
	//
	// 调用时机：Runner.Run() 开始时调用一次。
	// 典型实现：打印 ASCII art banner、版本号、模型名称、使用提示。
	Welcome(model string)

	// Close 关闭 UI，释放资源。
	// 调用时机：Runner.Run() 退出时（正常退出或错误退出）。
	// 典型实现：恢复终端状态（raw mode → cooked mode）、关闭文件句柄。
	Close() error
}
