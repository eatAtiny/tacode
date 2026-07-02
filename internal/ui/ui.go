package ui

// UI 定义了 Agent 与用户交互的接口。
// 所有 UI 操作都通过此接口完成，实现可插拔。
type UI interface {
	// ReadInput 读取用户输入，阻塞直到用户按下 Enter。
	ReadInput() (string, error)

	// ReadInputChan 返回一个只读 channel，后台持续读取用户输入。
	// 首次调用启动后台 goroutine，后续调用返回同一个 channel。
	ReadInputChan() <-chan string

	// OnThink 通知 LLM 正在思考。
	OnThink(iteration int)

	// OnDelta 流式输出增量文本。
	OnDelta(content string)

	// OnToolCall 通知工具调用请求。
	OnToolCall(name, args string)

	// OnToolResult 通知工具执行结果。
	OnToolResult(name, result string, isError bool)

	// OnContinue 通知继续推理。
	OnContinue(iteration int)

	// OnFinal 通知最终回答。
	OnFinal(answer string)

	// OnError 通知错误。
	OnError(err error)

	// OnMessage 输出一般性消息（成功提示、帮助信息等）。
	OnMessage(msg string)

	// ConfirmPermission 请求用户确认权限。
	// 返回 true 表示允许，false 表示拒绝。
	// inputForward 不为 nil 时从此 channel 读取用户输入。
	ConfirmPermission(tool, args string, inputForward <-chan string) (bool, error)

	// Welcome 打印启动欢迎信息。
	Welcome(model string)

	// Close 关闭 UI，释放资源。
	Close() error

	// SetSessionName 设置当前会话的显示名称。
	SetSessionName(name string)

	// SetModel 设置当前使用的模型名称。
	SetModel(model string)

	// UpdateTokens 累计本轮 token 用量。
	UpdateTokens(input, output int)

	// ResetTokens 重置本轮 token 计数。
	ResetTokens()
}
