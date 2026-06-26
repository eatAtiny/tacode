package agent

import (
	"context"
	"fmt"
	"strings"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/tool"

	openai "github.com/sashabaranov/go-openai"
)

// ──────────────────────────────────────────────────────────
// 常量定义
// ──────────────────────────────────────────────────────────

// compressThreshold 压缩阈值：token 使用率超过 80% 触发压缩。
const compressThreshold = 0.8

// ──────────────────────────────────────────────────────────
// queryLoop 核心循环（异步生成器模式）
// ──────────────────────────────────────────────────────────

// queryLoopContext 循环上下文，用于在循环中共享状态。
type queryLoopContext struct {
	ctx             context.Context
	llmClient       *llm.OpenAIClient
	messages        []llm.ChatMessage
	tools           []openai.Tool
	toolRegistry    *tool.Registry
	maxIter         int
	contextLimit    int
	events          chan<- QueryEvent
	seenToolCalls   map[string]bool
	totalInputTokens  int
	totalOutputTokens int
	lastInputTokens   int // 暂存每次调用的输入 token 数
	lastOutputTokens  int // 暂存每次调用的输出 token 数
}

// queryLoop 是纯粹的 Agent Loop 核心循环，使用异步生成器模式。
//
// 职责：
//   - while(true) 循环，调用 LLM → 检查 tool_use → 执行工具 → 结果推入消息 → 重复
//   - 通过 channel yield 中间事件（思考、工具调用、工具结果等）
//   - 直到 LLM 给出最终回答（无 tool_use）或达到最大迭代次数
//   - 自动检查 token 用量，接近上限时压缩旧的工具调用消息
//
// 设计：
//   - 返回一个只读 channel，上层可以实时读取中间事件
//   - 类似 Claude Code 的 async function* yield 机制
//   - 不关心 UI 输出和事件记录，只负责核心循环逻辑
//   - 可独立测试和复用
//
// 参数：
//   - ctx: 上下文，用于取消和超时控制
//   - llmClient: LLM 客户端，用于调用模型
//   - messages: 初始消息数组（包含 system prompt 和 user prompt）
//   - tools: 工具定义列表（OpenAI function calling 格式）
//   - toolRegistry: 工具注册表，用于查找和执行工具
//   - maxIter: 最大迭代次数，防止无限循环
//   - contextLimit: 模型的上下文窗口大小（token 数），用于压缩检查
//
// 返回：
//   - <-chan QueryEvent: 只读 channel，上层可以读取中间事件
//
// 事件类型：
//   - think: LLM 思考中
//   - delta: 增量文本（流式输出）
//   - tool_call: 工具调用请求（包含 ToolCalls 字段）
//   - tool_result: 工具执行结果（包含 ToolName、ToolResult、IsError 字段）
//   - continue: 继续推理（工具执行完毕，继续下一轮循环）
//   - final: 最终回答（包含 Content 字段）
//   - error: 错误（包含 Error 字段）
//
// 使用示例：
//
//	eventChan := queryLoop(ctx, llm, messages, tools, registry, 10, 128000)
//	for event := range eventChan {
//	    switch event.Type {
//	    case QueryEventThink:
//	        fmt.Println("思考中...")
//	    case QueryEventToolCall:
//	        fmt.Printf("调用工具: %v\n", event.ToolCalls)
//	    case QueryEventToolResult:
//	        fmt.Printf("工具结果: %s\n", event.ToolResult)
//	    case QueryEventFinal:
//	        fmt.Printf("最终回答: %s\n", event.Content)
//	    case QueryEventError:
//	        fmt.Printf("错误: %v\n", event.Error)
//	    }
//	}
func queryLoop(
	ctx context.Context,
	llmClient *llm.OpenAIClient,
	messages []llm.ChatMessage,
	tools []openai.Tool,
	toolRegistry *tool.Registry,
	maxIter int,
	contextLimit int,
) <-chan QueryEvent {
	events := make(chan QueryEvent)

	go func() {
		defer close(events)

		// 初始化循环上下文。
		lc := &queryLoopContext{
			ctx:           ctx,
			llmClient:     llmClient,
			messages:      messages,
			tools:         tools,
			toolRegistry:  toolRegistry,
			maxIter:       maxIter,
			contextLimit:  contextLimit,
			events:        events,
			seenToolCalls: make(map[string]bool),
		}

		// 执行核心循环。
		lc.runLoop()
	}()

	return events
}

// runLoop 执行核心循环。
func (lc *queryLoopContext) runLoop() {
	for iter := 0; iter < lc.maxIter; iter++ {
		// 检查上下文是否被取消。
		if lc.ctx.Err() != nil {
			lc.yieldError("query loop cancelled", lc.ctx.Err())
			return
		}

		// 检查并压缩上下文。
		if !lc.checkAndCompressContext(iter) {
			return
		}

		// 调用 LLM 并处理流式响应。
		content, toolCalls, ok := lc.callLLMStream(iter)
		if !ok {
			return
		}

		// 累计 token 用量。
		lc.accumulateTokens()

		// 推入 assistant 响应。
		lc.appendAssistantMessage(content, toolCalls)

		// 如果没有工具调用，任务完成。
		if len(toolCalls) == 0 {
			lc.yieldFinal(content, iter+1)
			return
		}

		// 执行工具调用。
		if !lc.executeToolCalls(toolCalls, iter) {
			return
		}

		// 重复调用检测。
		lc.detectDuplicateAndWarn(toolCalls)

		// yield: 继续推理。
		lc.yieldContinue(iter + 1)
	}

	// 达到最大迭代次数，生成最终总结。
	lc.generateFinalSummary()
}

// checkAndCompressContext 检查 token 用量，接近上限时压缩。
//
// 返回：
//   - true: 继续执行
//   - false: 发生错误，需要退出
func (lc *queryLoopContext) checkAndCompressContext(iter int) bool {
	if lc.contextLimit <= 0 {
		return true
	}

	totalTokens := estimateMessagesTokens(lc.messages)
	usageRatio := float64(totalTokens) / float64(lc.contextLimit)

	if usageRatio <= compressThreshold {
		return true
	}

	// yield: 压缩事件。
	lc.events <- QueryEvent{
		Type:      QueryEventThink,
		Content:   "🗜️ 上下文接近上限，正在压缩...",
		Iteration: iter + 1,
	}

	lc.messages = compressMessages(lc.messages, iter)
	return true
}

// callLLMStream 调用 LLM 流式接口，收集响应。
//
// 返回：
//   - content: 完整的文本内容
//   - toolCalls: 工具调用列表
//   - ok: 是否成功
func (lc *queryLoopContext) callLLMStream(iter int) (string, []llm.ToolCall, bool) {
	// yield: 思考中。
	lc.events <- QueryEvent{
		Type:      QueryEventThink,
		Iteration: iter + 1,
	}

	streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, lc.tools)

	var fullContent string
	var toolCalls []llm.ToolCall
	var inputTokens, outputTokens int

	for streamEvent := range streamChan {
		switch streamEvent.Type {
		case llm.StreamEventDelta:
			fullContent += streamEvent.Content
			// yield: 增量文本。
			lc.events <- QueryEvent{
				Type:         QueryEventDelta,
				Content:      streamEvent.Content,
				Iteration:    iter + 1,
				InputTokens:  streamEvent.InputTokens,
				OutputTokens: streamEvent.OutputTokens,
			}

		case llm.StreamEventDone:
			toolCalls = streamEvent.ToolCalls
			inputTokens = streamEvent.InputTokens
			outputTokens = streamEvent.OutputTokens
			if streamEvent.Content != "" {
				fullContent = streamEvent.Content
			}

		case llm.StreamEventError:
			lc.yieldError("stream error", streamEvent.Error)
			return "", nil, false
		}
	}

	// 暂存 token 信息，供后续累计。
	lc.lastInputTokens = inputTokens
	lc.lastOutputTokens = outputTokens

	return fullContent, toolCalls, true
}

// lastInputTokens 和 lastOutputTokens 用于暂存每次调用的 token 数。
func (lc *queryLoopContext) accumulateTokens() {
	lc.totalInputTokens += lc.lastInputTokens
	lc.totalOutputTokens += lc.lastOutputTokens
}

// appendAssistantMessage 推入 assistant 响应到消息历史。
func (lc *queryLoopContext) appendAssistantMessage(content string, toolCalls []llm.ToolCall) {
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	})
}

// executeToolCalls 执行工具调用列表。
//
// 返回：
//   - true: 继续执行
//   - false: 发生错误，需要退出
func (lc *queryLoopContext) executeToolCalls(toolCalls []llm.ToolCall, iter int) bool {
	// yield: 工具调用请求。
	lc.events <- QueryEvent{
		Type:         QueryEventToolCall,
		ToolCalls:    toolCalls,
		Iteration:    iter + 1,
		InputTokens:  lc.lastInputTokens,
		OutputTokens: lc.lastOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
	}

	for _, tc := range toolCalls {
		if !lc.executeSingleTool(tc, iter) {
			return false
		}
	}

	return true
}

// executeSingleTool 执行单个工具调用。
//
// 返回：
//   - true: 继续执行
//   - false: 发生错误，需要退出
func (lc *queryLoopContext) executeSingleTool(tc llm.ToolCall, iter int) bool {
	// 权限检查。
	if !lc.checkToolPermission(tc, iter) {
		return true // 权限拒绝，但继续执行下一个工具
	}

	// 查找工具。
	t := lc.toolRegistry.Get(tc.Name)
	if t == nil {
		errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// 执行工具。
	result, execErr := t.Execute(tc.Arguments)
	if execErr != nil {
		result = fmt.Sprintf("工具执行出错: %v\n请尝试其他方案，不要重复相同的命令。", execErr)
	}

	// yield: 工具执行结果。
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: result,
		IsError:    execErr != nil,
		Iteration:  iter + 1,
	}

	// 推入消息历史。
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:       "tool",
		Content:    result,
		ToolCallID: tc.ID,
	})

	return true
}

// checkToolPermission 检查工具权限。
//
// 返回：
//   - true: 允许执行
//   - false: 权限拒绝
func (lc *queryLoopContext) checkToolPermission(tc llm.ToolCall, iter int) bool {
	permResult := checkToolPermission(tc.Name, tc.Arguments)

	if permResult.Action == "deny" {
		errMsg := fmt.Sprintf("权限拒绝: %s", permResult.Message)
		lc.yieldToolError(tc, errMsg, iter)
		return false
	}

	if permResult.Action == "confirm" {
		// 需要用户确认（留好扩展接口，后续可通知上层 UI）。
		// 目前暂时直接允许。
		lc.events <- QueryEvent{
			Type:      QueryEventThink,
			Content:   fmt.Sprintf("⚠️ 工具 %s 需要确认，暂时允许执行", tc.Name),
			Iteration: iter + 1,
		}
	}

	return true
}

// detectDuplicateAndWarn 检测重复调用并警告。
func (lc *queryLoopContext) detectDuplicateAndWarn(toolCalls []llm.ToolCall) {
	currentToolCall := toolCallSignature(toolCalls)
	isDuplicate := currentToolCall != "" && lc.seenToolCalls[currentToolCall]

	if currentToolCall != "" {
		lc.seenToolCalls[currentToolCall] = true
	}

	if isDuplicate {
		lc.messages = append(lc.messages, llm.ChatMessage{
			Role:    "user",
			Content: "你已经调用过相同的工具并获得了相同的结果。请根据已有信息直接给出最终回答，不要再调用任何工具。",
		})
	}
}

// generateFinalSummary 达到最大迭代次数时，生成最终总结。
func (lc *queryLoopContext) generateFinalSummary() {
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:    "user",
		Content: "你已经尝试了多次工具调用。请根据已有信息直接给出回答，不要再调用工具。",
	})

	streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, lc.tools)
	var finalContent string
	var finalInputTokens, finalOutputTokens int

	for streamEvent := range streamChan {
		switch streamEvent.Type {
		case llm.StreamEventDelta:
			finalContent += streamEvent.Content
		case llm.StreamEventDone:
			finalInputTokens = streamEvent.InputTokens
			finalOutputTokens = streamEvent.OutputTokens
		case llm.StreamEventError:
			lc.yieldError(fmt.Sprintf("reached max iterations (%d) without final answer", lc.maxIter), streamEvent.Error)
			return
		}
	}

	lc.totalInputTokens += finalInputTokens
	lc.totalOutputTokens += finalOutputTokens

	lc.yieldFinal(finalContent, lc.maxIter)
}

// ──────────────────────────────────────────────────────────
// yield 辅助函数
// ──────────────────────────────────────────────────────────

// yieldError yield 错误事件。
func (lc *queryLoopContext) yieldError(content string, err error) {
	lc.events <- QueryEvent{
		Type:    QueryEventError,
		Content: content,
		Error:   err,
	}
}

// yieldFinal yield 最终回答事件。
func (lc *queryLoopContext) yieldFinal(content string, iter int) {
	lc.events <- QueryEvent{
		Type:         QueryEventFinal,
		Content:      content,
		Iteration:    iter,
		InputTokens:  lc.totalInputTokens,
		OutputTokens: lc.totalOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
	}
}

// yieldToolError yield 工具错误事件。
func (lc *queryLoopContext) yieldToolError(tc llm.ToolCall, errMsg string, iter int) {
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: errMsg,
		IsError:    true,
		Iteration:  iter + 1,
	}

	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:       "tool",
		Content:    errMsg,
		ToolCallID: tc.ID,
	})
}

// yieldContinue yield 继续推理事件。
func (lc *queryLoopContext) yieldContinue(iter int) {
	lc.events <- QueryEvent{
		Type:         QueryEventContinue,
		Iteration:    iter,
		InputTokens:  lc.totalInputTokens,
		OutputTokens: lc.totalOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
	}
}

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// estimateMessagesTokens 估算消息数组的 token 数。
//
// 计算方式：
//   - 遍历所有消息，拼接内容
//   - 使用 memory.EstimateTokens 估算
//
// 参数：
//   - messages: 消息数组
//
// 返回：
//   - int: 估算的 token 数
func estimateMessagesTokens(messages []llm.ChatMessage) int {
	var totalContent string
	for _, msg := range messages {
		totalContent += msg.Content
		for _, tc := range msg.ToolCalls {
			totalContent += tc.Name + tc.Arguments
		}
	}
	return memory.EstimateTokens(totalContent)
}

// compressMessages 压缩旧的工具调用消息，减少 token 用量。
//
// 策略：
//   - 保留 system prompt（第 0 条）
//   - 保留最近 2 轮的工具调用（完整保留）
//   - 压缩更早的工具调用（只保留摘要）
//   - 压缩后的消息格式："[已压缩] 工具: xxx, 结果: 成功/失败"
//
// 参数：
//   - messages: 原始消息数组
//   - currentIter: 当前迭代次数
//
// 返回：
//   - []llm.ChatMessage: 压缩后的消息数组
func compressMessages(messages []llm.ChatMessage, currentIter int) []llm.ChatMessage {
	if len(messages) <= 3 {
		return messages // 消息太少，不需要压缩
	}

	// 找到需要压缩的消息范围。
	// 保留：system prompt + 最近 2 轮的工具调用（4 条消息：assistant + tool + assistant + tool）
	// 压缩：更早的工具调用
	compressEnd := len(messages) - 4 // 保留最后 4 条消息
	if compressEnd < 1 {
		compressEnd = 1
	}

	// 创建压缩后的消息数组。
	compressed := make([]llm.ChatMessage, 0, len(messages))
	compressed = append(compressed, messages[0]) // 保留 system prompt

	// 压缩早期的工具调用消息。
	for i := 1; i < compressEnd; i++ {
		msg := messages[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			// 压缩 assistant 的工具调用消息。
			toolNames := make([]string, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				toolNames[j] = tc.Name
			}
			compressed = append(compressed, llm.ChatMessage{
				Role:    "assistant",
				Content: fmt.Sprintf("[已压缩] 调用工具: %s", strings.Join(toolNames, ", ")),
			})
		} else if msg.Role == "tool" {
			// 压缩工具结果消息。
			isSuccess := !strings.Contains(msg.Content, "出错") && !strings.Contains(msg.Content, "错误")
			status := "成功"
			if !isSuccess {
				status = "失败"
			}
			compressed = append(compressed, llm.ChatMessage{
				Role:       "tool",
				Content:    fmt.Sprintf("[已压缩] 工具执行%s", status),
				ToolCallID: msg.ToolCallID,
			})
		} else {
			// 其他消息保留原样。
			compressed = append(compressed, msg)
		}
	}

	// 保留最近的工具调用消息（完整保留）。
	compressed = append(compressed, messages[compressEnd:]...)

	return compressed
}
