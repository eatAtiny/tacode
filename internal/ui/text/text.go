// Package text 提供 headless 模式的 UI 实现。
//
// TextUI 适用于无 TTY 环境（如子 agent、管道、后台任务），不依赖终端渲染。
// 所有事件通过 OnEvent 回调函数转发给上层处理，不做任何终端输出。
//
// 与 BubbleUI 的关键区别：
//   - 不写 os.Stdout（Welcome 为空操作，OnDelta 不打印）
//   - ReadInput 返回错误（不支持交互输入）
//   - ConfirmPermission 默认放行（无人值守场景）
//   - 通过 OnEvent 回调将所有事件暴露给上层
//
// 使用场景：
//   - 子 agent 模式：上层 agent 通过 OnEvent 回调接收事件并自行处理
//   - 测试环境：验证事件序列而无需实际终端
//   - API 模式：将事件转换为 HTTP SSE 或 WebSocket 消息
package text

import (
	"fmt"

	"tacode/internal/memory"
	"tacode/internal/session"
)

// TextUI 是 UI 接口的 headless 实现。
//
// 所有事件通过 OnEvent 回调转发给上层，格式为 OnEvent(eventType, data)。
// eventType 是事件类型字符串（"think"、"delta"、"final" 等），
// data 的类型取决于事件类型。
//
// 使用示例：
//
//	ui := text.NewTextUI()
//	ui.OnEvent = func(event string, data any) {
//	    switch event {
//	    case "delta":
//	        fmt.Print(data.(string))
//	    case "final":
//	        fmt.Println("Answer:", data.(string))
//	    }
//	}
type TextUI struct {
	// OnEvent 回调函数，上层 agent 可注入处理逻辑。
	// 参数：
	//   - event: 事件类型字符串，对应 UI 接口的方法名：
	//     "think"、"delta"、"tool_call"、"tool_result"、"continue"、
	//     "final"、"error"、"message"、"permission"、"balance"、
	//     "context"、"session_picker"、"history"
	//   - data: 事件数据，类型因事件而异：
	//     - think/continue: int（迭代次数）
	//     - delta/message/balance: string（文本内容）
	//     - final: map[string]any{"answer": ..., "input_tokens": ...,
	//       "output_tokens": ..., "total_tokens": ...}
	//     - tool_call: map[string]string{"name": ..., "args": ...}
	//     - tool_result: map[string]any{"name": ..., "result": ..., "is_error": ...}
	//     - error: error
	//     - permission: map[string]string{"tool": ..., "args": ...}
	//     - context: map[string]int{"used_chars": ..., "context_char_limit": ...}
	//     - session_picker: map[string]any{"sessions": ..., "active_id": ...}
	//     - history: []memory.Event
	OnEvent func(event string, data any)
}

// NewTextUI 创建 TextUI 实例。OnEvent 初始为 nil，上层需自行设置。
func NewTextUI() *TextUI {
	return &TextUI{}
}

// emit 转发事件给 OnEvent 回调；回调未设置时静默丢弃（不 panic）。
func (t *TextUI) emit(event string, data any) {
	if t.OnEvent != nil {
		t.OnEvent(event, data)
	}
}

// ReadInput 不支持。TextUI 不提供交互式输入，始终返回错误。
func (t *TextUI) ReadInput() (string, error) {
	return "", fmt.Errorf("TextUI: ReadInput not implemented")
}

// ReadInputChan 返回一个立即关闭的空 channel。
// TextUI 不提供交互式输入，上层不应依赖此 channel。
func (t *TextUI) ReadInputChan() <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}

// OnThink 转发思考事件。data 为 iteration（int）。
func (t *TextUI) OnThink(iteration int) {
	t.emit("think", iteration)
}

// OnDelta 转发流式增量文本事件。data 为 content（string）。
func (t *TextUI) OnDelta(content string) {
	t.emit("delta", content)
}

// OnToolCall 转发工具调用事件。
// data 为 map[string]string{"name": ..., "args": ...}。
func (t *TextUI) OnToolCall(name, args string) {
	t.emit("tool_call", map[string]string{"name": name, "args": args})
}

// OnToolResult 转发工具执行结果事件。
// data 为 map[string]any{"name": ..., "result": ..., "is_error": ...}。
func (t *TextUI) OnToolResult(name, result string, isError bool) {
	t.emit("tool_result", map[string]any{"name": name, "result": result, "is_error": isError})
}

// OnContinue 转发继续推理事件。data 为 iteration（int）。
func (t *TextUI) OnContinue(iteration int) {
	t.emit("continue", iteration)
}

// OnFinal 转发最终回答事件。data 为 map：{"answer", "input_tokens", "output_tokens", "total_tokens"}。
func (t *TextUI) OnFinal(answer string, inputTokens, outputTokens, totalTokens int) {
	t.emit("final", map[string]any{
		"answer":        answer,
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"total_tokens":  totalTokens,
	})
}

// ShowBalance 转发余额展示事件。data 为 line（string）。
func (t *TextUI) ShowBalance(line string) {
	t.emit("balance", line)
}

// UpdateContext 转发上下文占用事件。data 为 map{"used_chars", "context_char_limit"}。
func (t *TextUI) UpdateContext(usedChars, contextCharLimit int) {
	t.emit("context", map[string]int{"used_chars": usedChars, "context_char_limit": contextCharLimit})
}

// RunSessionPicker 运行会话选择器（headless 模式：独立 tea 程序前台运行）。
// 生产 one-shot 路径不可达（单次运行无 REPL 主循环、不触发 /list），
// 仅为接口完整性保留。
func (t *TextUI) RunSessionPicker(sessions []session.SessionMeta, activeID string) (string, error) {
	t.emit("session_picker", map[string]any{"sessions": sessions, "active_id": activeID})
	return session.RunSessionPicker(sessions, activeID)
}

// ShowHistory 转发会话历史事件。data 为 events（[]memory.Event）。
func (t *TextUI) ShowHistory(events []memory.Event) {
	t.emit("history", events)
}

// OnError 转发错误事件。data 为 err（error）。
func (t *TextUI) OnError(err error) {
	t.emit("error", err)
}

// OnMessage 转发一般性消息事件。data 为 msg（string）。
func (t *TextUI) OnMessage(msg string) {
	t.emit("message", msg)
}

// Welcome 在 headless 模式下为空操作（不输出欢迎信息）。
func (t *TextUI) Welcome(model string) {
	// 子 agent 模式不输出欢迎信息。
}

// ConfirmPermission 默认放行所有权限请求（无人值守场景）。
// 转发 permission 事件（data 为 map[string]string，含 reason）后直接返回 true。
// 如需权限控制，上层应通过 OnEvent 回调拦截并自行处理。
func (t *TextUI) ConfirmPermission(tool, args, reason string) (bool, error) {
	t.emit("permission", map[string]string{"tool": tool, "args": args, "reason": reason})
	return true, nil // 默认放行
}

// Close 无资源需释放，返回 nil。
func (t *TextUI) Close() error { return nil }
