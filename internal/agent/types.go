package agent

import (
	"tacode/internal/llm"
)

// ──────────────────────────────────────────────────────────
// 内部类型
// ──────────────────────────────────────────────────────────

// queryResult 异步查询结果。
// Runner.Run() 通过 channel 接收此结果。
// answer 为空且 err 不为 nil 表示查询失败。
type queryResult struct {
	answer   string
	messages []llm.ChatMessage // 查询结束后的完整消息数组（跨轮累积用，err 为 nil 时有效）
	err      error
}

// ──────────────────────────────────────────────────────────
// QueryEvent 事件流
// ──────────────────────────────────────────────────────────

// QueryEvent 是 queryLoop 事件流的元素。
//
// 密封接口：唯一方法是包内私有的 isQueryEvent()，因此只有本包能新增事件
// 类型，消费者可以放心穷举。这替代了原先「单 struct + Type 判别字段」的
// union 形态——那时 15 个字段中大部分只在特定 Type 下有效，编译器无从检查，
// 也容易出现「生产侧写入、消费侧从不读取」的死字段。
//
// 事件流顺序（典型的一次 ReAct 查询）：
//
//	Think → Delta... → ToolCall → ToolResult → Continue
//	       → Think → Delta... → ToolCall → ToolResult → Continue
//	       → Think → Delta... → Final
//
// 权限确认插入在 ToolCall 和 ToolResult 之间：
//
//	ToolCall → Permission → ToolResult（或跳过）
//
// 设计：类似 Claude Code 的 async function* yield 机制。
// queryLoop（生产者）通过 goroutine + channel 发送事件，
// dispatch（消费者）通过 range channel 接收并处理。
type QueryEvent interface{ isQueryEvent() }

// ThinkEvent LLM 开始新一轮思考。
// 触发时机：每次调用 LLM 之前。
type ThinkEvent struct {
	Content   string
	Iteration int // 当前迭代次数（从 1 开始）
}

func (ThinkEvent) isQueryEvent() {}

// DeltaEvent 流式增量文本。
// 触发时机：LLM 每次返回一个 token 时。
// 注意：多次 Delta 后可能被 ToolCall 中断（LLM 转而请求工具），
// 也可能在 Final 之前被清除（UI 恢复光标替换为渲染后的最终回答）。
type DeltaEvent struct {
	Content      string
	Iteration    int
	InputTokens  int
	OutputTokens int
}

func (DeltaEvent) isQueryEvent() {}

// ToolCallEvent LLM 请求调用工具。
// 触发时机：LLM 返回 tool_calls 后。
type ToolCallEvent struct {
	ToolCalls    []llm.ToolCall
	Iteration    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

func (ToolCallEvent) isQueryEvent() {}

// ToolResultEvent 工具执行结果。
// 触发时机：每个工具执行完成后。
type ToolResultEvent struct {
	ToolName   string
	ToolResult string // 执行输出
	IsError    bool   // 是否出错
	Iteration  int
}

func (ToolResultEvent) isQueryEvent() {}

// PermissionRequest 权限确认请求。
// 触发时机：执行需要确认的工具之前。
type PermissionRequest struct {
	Tool   string
	Args   string // 工具参数（JSON 字符串）
	Reason string // 需要确认的原因（如 "high_risk_operation"）

	// Reply 承载用户决策：dispatch 写入，checkToolPermission 阻塞读取。
	// 缓冲为 1，勿改——无缓冲会改变调度时序。
	Reply chan bool

	Iteration int
}

func (PermissionRequest) isQueryEvent() {}

// ContinueEvent 继续推理（本轮工具执行完毕，准备进入下一轮迭代）。
type ContinueEvent struct {
	Iteration    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

func (ContinueEvent) isQueryEvent() {}

// FinalEvent 最终回答。
// 触发时机：LLM 不再需要调用工具，直接给出回答。
type FinalEvent struct {
	Content      string // 完整最终回答（Markdown 格式）
	Iteration    int
	InputTokens  int
	OutputTokens int
	TotalTokens  int

	// Messages 是 queryLoopContext.messages 的 slice header 别名（不拷贝）。
	// 生产者 yield 后立即返回，不会有后续 append 触及；保持别名语义。
	Messages []llm.ChatMessage
}

func (FinalEvent) isQueryEvent() {}

// LoopError 循环错误。
// 触发时机：LLM 调用失败、流式中断、达到最大迭代次数等。
type LoopError struct {
	Content string // 人类可读描述（如 "stream error"）
	Err     error  // 底层错误对象
}

func (LoopError) isQueryEvent() {}
