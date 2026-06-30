package agent

import (
	"agentic/internal/llm"
)

// ──────────────────────────────────────────────────────────
// QueryEvent 异步生成器事件类型
// ──────────────────────────────────────────────────────────

// QueryEventType 事件类型枚举。
// 用于 queryLoop 异步生成器向 QueryEngine 传递中间状态。
type QueryEventType string

const (
	QueryEventThink      QueryEventType = "think"       // LLM 思考中
	QueryEventDelta      QueryEventType = "delta"       // 增量文本（流式输出）
	QueryEventToolCall   QueryEventType = "tool_call"   // 工具调用请求
	QueryEventToolResult QueryEventType = "tool_result" // 工具执行结果
	QueryEventContinue   QueryEventType = "continue"    // 继续推理
	QueryEventFinal      QueryEventType = "final"       // 最终回答
	QueryEventError      QueryEventType = "error"       // 错误

	// TODO: 添加更多事件类型
	// QueryEventConfirm   QueryEventType = "confirm"   // 用户确认请求
	// QueryEventProgress  QueryEventType = "progress"  // 进度更新
	// QueryEventCost      QueryEventType = "cost"      // 费用统计
)

// QueryEvent 表示 queryLoop 的中间事件，通过 channel 传递给上层。
// 设计：类似 Claude Code 的 async function* yield 机制。
//
// 使用场景：
//   - QueryEngine 从 channel 读取事件并实时显示给用户
//   - 每个事件代表 queryLoop 的一个关键状态变化
//   - 上层可以根据事件类型决定如何展示（UI 输出、事件记录等）
type QueryEvent struct {
	Type      QueryEventType // 事件类型
	Content   string         // 文本内容（final 类型时为最终回答）
	ToolCalls []llm.ToolCall // 工具调用请求（tool_call 类型）
	ToolName  string         // 工具名称（tool_result 类型）
	ToolResult string        // 工具结果（tool_result 类型）
	IsError   bool           // 是否是错误（tool_result 类型）
	Iteration int            // 当前迭代次数（从 1 开始）
	Error     error          // 错误信息（error 类型）

	// Token 追踪
	InputTokens  int // 本次调用的输入 token 数
	OutputTokens int // 本次调用的输出 token 数
	TotalTokens  int // 累计总 token 数

	// TODO: 添加更多字段
	// Duration    time.Duration  // 执行耗时
	// Cost        float64        // 费用（美元）
	// Model       string         // 使用的模型
	// Metadata    map[string]interface{} // 自定义元数据
}
