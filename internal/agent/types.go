package agent

import (
	"agentic/internal/llm"
)

// ──────────────────────────────────────────────────────────
// 内部类型
// ──────────────────────────────────────────────────────────

// queryResult 异步查询结果。
// Runner.Run() 通过 channel 接收此结果。
// answer 为空且 err 不为 nil 表示查询失败。
type queryResult struct {
	answer string
	err    error
}

// ──────────────────────────────────────────────────────────
// QueryEvent 异步生成器事件类型
// ──────────────────────────────────────────────────────────

// QueryEventType 事件类型枚举。
// 用于 queryLoop 异步生成器向 QueryEngine 传递中间状态。
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
type QueryEventType string

const (
	// QueryEventThink LLM 开始新一轮思考。
	// 触发时机：每次调用 LLM 之前。
	// 数据字段：Iteration（从 1 开始）。
	QueryEventThink QueryEventType = "think"

	// QueryEventDelta 流式增量文本。
	// 触发时机：LLM 每次返回一个 token 时。
	// 数据字段：Content（增量文本片段）、InputTokens、OutputTokens。
	// 注意：多次 Delta 后可能被 ToolCall 中断（LLM 转而请求工具），
	// 也可能在 Final 之前被清除（UI 恢复光标替换为渲染后的最终回答）。
	QueryEventDelta QueryEventType = "delta"

	// QueryEventToolCall LLM 请求调用工具。
	// 触发时机：LLM 返回 tool_calls 后。
	// 数据字段：ToolCalls（完整工具调用列表）、InputTokens、OutputTokens、TotalTokens。
	QueryEventToolCall QueryEventType = "tool_call"

	// QueryEventToolResult 工具执行结果。
	// 触发时机：每个工具执行完成后。
	// 数据字段：ToolName、ToolResult（执行输出）、IsError（是否出错）。
	QueryEventToolResult QueryEventType = "tool_result"

	// QueryEventContinue 继续推理（本轮工具执行完毕，准备进入下一轮迭代）。
	// 触发时机：所有工具执行完毕后。
	// 数据字段：Iteration（下一轮的迭代次数）。
	QueryEventContinue QueryEventType = "continue"

	// QueryEventFinal 最终回答。
	// 触发时机：LLM 不再需要调用工具，直接给出回答。
	// 数据字段：Content（完整最终回答，Markdown 格式）、InputTokens、OutputTokens、TotalTokens。
	QueryEventFinal QueryEventType = "final"

	// QueryEventError 错误。
	// 触发时机：LLM 调用失败、流式中断等。
	// 数据字段：Error（错误对象）、Content（错误描述）。
	QueryEventError QueryEventType = "error"

	// QueryEventPermission 权限确认请求。
	// 触发时机：执行需要确认的工具之前。
	// 数据字段：PermissionTool、PermissionArgs、PermissionReason、PermissionCh。
	// 上层将用户决策写入 PermissionCh，queryLoop 阻塞等待此 channel。
	QueryEventPermission QueryEventType = "permission"
)

// QueryEvent 表示 queryLoop 的中间事件，通过 channel 传递给上层。
//
// 设计：类似 Claude Code 的 async function* yield 机制。
// queryLoop（生产者）通过 goroutine + channel 发送事件，
// QueryEngine（消费者）通过 range channel 接收并处理。
//
// 字段分组：
//   - 通用字段：Type、Content、Iteration
//   - 工具调用：ToolCalls、ToolName、ToolResult、IsError
//   - Token 追踪：InputTokens、OutputTokens、TotalTokens
//   - 权限确认：PermissionRequired、PermissionTool、PermissionArgs、PermissionReason、PermissionCh
//   - 错误：Error
//
// 使用场景：
//   - QueryEngine 从 channel 读取事件并实时显示给用户
//   - 每个事件代表 queryLoop 的一个关键状态变化
//   - 上层可以根据事件类型决定如何展示（UI 输出、事件记录等）
type QueryEvent struct {
	// ── 通用字段 ──
	Type      QueryEventType // 事件类型
	Content   string         // 文本内容（final 类型时为最终回答，delta 类型时为增量片段）
	Iteration int            // 当前迭代次数（从 1 开始）

	// ── 工具调用字段 ──
	ToolCalls  []llm.ToolCall // 工具调用请求列表（tool_call 类型）
	ToolName   string         // 工具名称（tool_result 类型）
	ToolResult string         // 工具结果文本（tool_result 类型）
	IsError    bool           // 工具执行是否出错（tool_result 类型）

	// ── Token 追踪 ──
	InputTokens  int // 本次调用的输入 token 数
	OutputTokens int // 本次调用的输出 token 数
	TotalTokens  int // 累计总 token 数（input + output）

	// ── 错误字段 ──
	Error error // 错误信息（error 类型）

	// ── 权限请求字段（PermissionRequired 为 true 时有效） ──
	PermissionRequired bool     // 是否需要权限确认
	PermissionTool     string   // 需要确认的工具名
	PermissionArgs     string   // 工具参数（JSON 字符串）
	PermissionReason   string   // 需要确认的原因（如 "high_risk_operation"）
	PermissionCh       chan bool // 上层写入确认结果（true=允许，false=拒绝）
}
