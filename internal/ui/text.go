package ui

import "fmt"

// TextUI 用于子 agent 模式，无 TTY 依赖。
// 所有事件通过 OnEvent 回调转发给上层。
type TextUI struct {
	// OnEvent 回调函数，上层 agent 可注入处理逻辑。
	// 参数: event 事件类型, data 事件数据（类型取决于 event）。
	OnEvent func(event string, data any)
}

// NewTextUI 创建 TextUI 实例。
func NewTextUI() *TextUI {
	return &TextUI{}
}

func (t *TextUI) ReadInput() (string, error) {
	return "", fmt.Errorf("TextUI: ReadInput not implemented")
}

func (t *TextUI) ReadInputChan() <-chan string {
	ch := make(chan string)
	close(ch)
	return ch
}

func (t *TextUI) OnThink(iteration int) {
	if t.OnEvent != nil {
		t.OnEvent("think", iteration)
	}
}

func (t *TextUI) OnDelta(content string) {
	if t.OnEvent != nil {
		t.OnEvent("delta", content)
	}
}

func (t *TextUI) OnToolCall(name, args string) {
	if t.OnEvent != nil {
		t.OnEvent("tool_call", map[string]string{"name": name, "args": args})
	}
}

func (t *TextUI) OnToolResult(name, result string, isError bool) {
	if t.OnEvent != nil {
		t.OnEvent("tool_result", map[string]any{"name": name, "result": result, "is_error": isError})
	}
}

func (t *TextUI) OnContinue(iteration int) {
	if t.OnEvent != nil {
		t.OnEvent("continue", iteration)
	}
}

func (t *TextUI) OnFinal(answer string) {
	if t.OnEvent != nil {
		t.OnEvent("final", answer)
	}
}

func (t *TextUI) OnError(err error) {
	if t.OnEvent != nil {
		t.OnEvent("error", err)
	}
}

func (t *TextUI) OnMessage(msg string) {
	if t.OnEvent != nil {
		t.OnEvent("message", msg)
	}
}

func (t *TextUI) Welcome(model string) {
	// 子 agent 模式不输出欢迎信息。
}

func (t *TextUI) ConfirmPermission(tool, args string, inputForward <-chan string) (bool, error) {
	if t.OnEvent != nil {
		t.OnEvent("permission", map[string]string{"tool": tool, "args": args})
	}
	return true, nil // 默认放行
}

func (t *TextUI) Close() error { return nil }
