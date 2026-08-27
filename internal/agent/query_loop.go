// query_loop.go ReAct 核心循环（异步生成器模式）：流式调用 LLM、迭代与 token
// 统计、事件 yield、压缩管线衔接。工具执行子系统见 tool_exec.go。

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentic/internal/llm"
	"agentic/internal/tool"

	openai "github.com/sashabaranov/go-openai"
)

// ──────────────────────────────────────────────────────────
// 常量定义（默认值，可由 config 覆盖）
// ──────────────────────────────────────────────────────────

// defaultResultLimit 默认结果截断上限（字符数）。
const defaultResultLimit = 8000

// ──────────────────────────────────────────────────────────
// queryLoop 核心循环（异步生成器模式）
// ──────────────────────────────────────────────────────────

// queryLoopContext 循环上下文，用于在循环中共享状态。
type queryLoopContext struct {
	ctx               context.Context
	llmClient         *llm.OpenAIClient
	messages          []llm.ChatMessage      // 完整消息历史（system + user + assistant + tool）
	tools             []openai.Tool          // 工具定义列表（OpenAI function calling 格式）
	toolRegistry      *tool.Registry         // 工具注册表，用于查找和执行工具
	maxIter           int                    // 最大迭代次数
	resultLimit       int                    // 结果截断上限（字符数，默认 8000）
	inputForward      <-chan string          // 输入转发通道（/interrupt 等控制命令），nil = 不启用
	events            chan<- QueryEvent      // 事件输出 channel（yield 事件到此）
	seenToolCalls     map[string]bool        // 已见过的工具调用签名（用于重复检测）
	totalInputTokens  int                    // 累计输入 token 数
	totalOutputTokens int                    // 累计输出 token 数
	lastInputTokens   int                    // 暂存每次调用的输入 token 数
	lastOutputTokens  int                    // 暂存每次调用的输出 token 数
	fileReads         map[string]time.Time   // 已读文件的 mtime（key=绝对路径，用于 read-before-edit 检测）
	compactor         *Compactor             // s08 四步压缩管线（nil = 禁用）
	activeRequest     string                 // 当前轮用户请求（压缩时注入 [Compacted] 消息用）
	reactiveRetries   int                    // prompt_too_long 补救重试次数（上限 MAX_REACTIVE_RETRIES）
}

// queryLoop 是纯粹的 Agent Loop 核心循环，使用异步生成器模式。
//
// 这是整个项目最核心的函数。负责 ReAct 的完整 while(true) 循环：
//
//	┌─────────────────────────────────────────────────────────┐
//	│  for iter := 0; iter < maxIter; iter++                  │
//	│    ├─ 步骤 4a: callLLMStream()                           │
//	│    │    调用 LLM 流式接口，yield Think/Delta 事件         │
//	│    │    返回: content（文本）+ toolCalls（工具调用列表）    │
//	│    │                                                     │
//	│    ├─ 步骤 4b: 检查 toolCalls                             │
//	│    │    if len(toolCalls) == 0 → yield Final + return    │
//	│    │                                                     │
//	│    ├─ 步骤 4c: executeToolCalls()                        │
//	│    │    对每个 toolCall:                                 │
//	│    │      ├─ checkToolPermission() ← 工具自检权限        │
//	│    │      ├─ toolRegistry.Get().Execute() ← 执行工具     │
//	│    │      ├─ TruncateResult() ← 统一截断                │
//	│    │      └─ yield ToolCall / ToolResult / Permission    │
//	│    │                                                     │
//	│    ├─ 步骤 4d: detectDuplicateAndWarn()                  │
//	│    │    检测重复调用，注入警告消息                         │
//	│    │                                                     │
//	│    ├─ 步骤 4e: prepareIfNeeded()                         │
//	│    │    字符用量超过 contextCharLimit（默认 50K）时压缩   │
//	│    │                                                     │
//	│    └─ yield Continue → 进入下一轮迭代                     │
//	│                                                          │
//	│  超限处理: generateFinalSummary()                         │
//	│    达到 maxIter → 强制 LLM 生成总结                       │
//	└─────────────────────────────────────────────────────────┘
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
//   - opts: 可选配置（结果截断上限等），零值使用默认
//
// 返回：
//   - <-chan QueryEvent: 只读 channel，上层通过 range 实时读取中间事件
func queryLoop(
	ctx context.Context,
	llmClient *llm.OpenAIClient,
	messages []llm.ChatMessage,
	tools []openai.Tool,
	toolRegistry *tool.Registry,
	maxIter int,
	opts ...queryLoopOptions,
) <-chan QueryEvent {
	events := make(chan QueryEvent)

	go func() {
		defer close(events)

		// 应用可选配置（零值回退默认）。
		resultLimit := defaultResultLimit
		var inputForward <-chan string
		var activeRequest string
		var compactor *Compactor
		if len(opts) > 0 {
			if opts[0].ResultLimit > 0 {
				resultLimit = opts[0].ResultLimit
			}
			inputForward = opts[0].InputForward
			activeRequest = opts[0].ActiveRequest
			compactor = opts[0].Compactor
		}

		// 初始化循环上下文。
		lc := &queryLoopContext{
			ctx:           ctx,
			llmClient:     llmClient,
			messages:      messages,
			tools:         tools,
			toolRegistry:  toolRegistry,
			maxIter:       maxIter,
			resultLimit:   resultLimit,
			inputForward:  inputForward,
			events:        events,
			seenToolCalls: make(map[string]bool),
			fileReads:     make(map[string]time.Time),
			compactor:     compactor,
			activeRequest: activeRequest,
		}

		// 执行核心循环。
		lc.runLoop()
	}()

	return events
}

// queryLoopOptions queryLoop 的可选配置参数。
// 零值表示使用默认值。
type queryLoopOptions struct {
	ResultLimit   int           // 结果截断上限，字符数（默认 8000）
	InputForward  <-chan string // 输入转发通道（/interrupt 等控制命令），nil = 不启用
	ActiveRequest string        // 当前轮用户请求（压缩时注入 [Compacted] 消息用）
	Compactor     *Compactor    // s08 四步压缩管线（nil = 禁用，保持旧行为）
}

// runLoop 执行核心 ReAct 循环。
//
// 循环体（每次迭代）：
//
//	1. 检查 ctx 是否被取消（支持 /stop）
//	2. 运行压缩管线（字符用量超过 contextCharLimit 时压缩，默认上限 50K）
//	3. 调用 LLM 流式接口 → 收集 content + toolCalls
//	4. 累计 token 用量
//	5. 推入 assistant 消息到历史
//	6. 如果无 toolCalls → 任务完成，yield Final + return
//	7. 执行工具调用（含权限检查）
//	8. 检测重复调用并警告
//	9. yield Continue → 回到步骤 1
func (lc *queryLoopContext) runLoop() {
	for iter := 0; iter < lc.maxIter; iter++ {
		// ── 步骤 4-前置: 检查上下文是否被取消 ──
		// 支持 /stop 命令：Runner 调用 cancel() → ctx.Err() != nil
		if lc.ctx.Err() != nil {
			lc.yieldError("query loop cancelled", lc.ctx.Err())
			return
		}

		// ── 步骤 4e: 检查并压缩上下文 ──
		// 每次模型调用前运行 s08 四步压缩管线（Prepare）。
		// compactor 为 nil 时保持旧行为（不压缩）。
		if !lc.prepareIfNeeded(iter) {
			return
		}

		// ── 步骤 4a: 调用 LLM 流式接口 ──
		// 流式调用 LLM，通过 channel yield Delta 事件（实时增量文本）。
		// 返回完整的 content 文本和 toolCalls 列表。
		content, toolCalls, ok := lc.callLLMStream(iter)
		if !ok {
			return
		}

		// ── 累计 token 用量 ──
		lc.accumulateTokens()

		// ── 步骤 4a-后置: 推入 assistant 响应到消息历史 ──
		lc.appendAssistantMessage(content, toolCalls)

		// ── 步骤 4b: 检查是否完成 ──
		// 如果没有工具调用，LLM 直接给出了最终答案 → 任务完成。
		if len(toolCalls) == 0 {
			lc.yieldFinal(content, iter+1)
			return
		}

		// ── 步骤 4c: 执行工具调用 ──
		// 遍历所有工具调用：权限检查 → 查找工具 → 执行 → 截断 → yield 结果。
		// 权限被拒绝时跳过该工具但继续执行其他工具。
		if !lc.executeToolCalls(toolCalls, iter) {
			return
		}

		// ── 步骤 4c-后置: compact 工具 ──
		// 整批工具执行完毕（tool 结果已全部追加）后，若模型请求了 compact，
		// 对该已闭合的回合执行历史摘要（镜像 s08：避免孤儿 tool_result，不丢副作用记录）。
		if lc.compactor != nil && lc.hasCompactRequest(toolCalls) {
			lc.messages = lc.compactor.compactHistory(lc.messages, lc.activeRequest)
		}

		// ── 步骤 4d: 重复调用检测 ──
		// 如果 LLM 重复调用相同的工具+参数，注入警告消息引导 LLM 改变策略。
		lc.detectDuplicateAndWarn(toolCalls)

		// ── yield: 继续推理 ──
		// 通知上层进入下一轮迭代。
		lc.yieldContinue(iter + 1)
	}

	// ── 达到最大迭代次数 ──
	// 强制 LLM 根据已有信息生成最终总结（不调用工具）。
	lc.generateFinalSummary()
}

// prepareIfNeeded 每次模型调用前运行 s08 四步压缩管线。
//
// 与旧 checkAndCompressContext 的差异：
//   - 由 token 阈值触发改为 Prepare 内部按字符超限分级触发（无条件跑步骤 1/2）
//   - 旧压缩用占位符丢弃信息；新管线每步都落盘可恢复（transcript/tool-results）
//   - compactor 为 nil 时 no-op（保持旧行为，保护不注入 compactor 的测试）
//
// 返回：
//   - true: 继续执行
//   - false: 发生错误，需要退出
func (lc *queryLoopContext) prepareIfNeeded(iter int) bool {
	if lc.compactor == nil {
		return true
	}

	before := len(lc.messages)
	lc.messages = lc.compactor.Prepare(lc.messages, lc.activeRequest)
	if len(lc.messages) != before {
		// yield: 压缩事件（供 UI 提示）。
		lc.events <- QueryEvent{
			Type:      QueryEventThink,
			Content:   "🗜️ 上下文接近上限，正在压缩...",
			Iteration: iter + 1,
		}
	}
	return true
}

// hasCompactRequest 判断工具调用批次中是否包含 compact 请求。
func (lc *queryLoopContext) hasCompactRequest(toolCalls []llm.ToolCall) bool {
	for _, tc := range toolCalls {
		if tc.Name == "compact" {
			return true
		}
	}
	return false
}

// callLLMStream 调用 LLM 流式接口，收集响应。
//
// 流程：
//   1. yield Think 事件（通知上层开始思考）
//   2. 调用 llmClient.ChatWithToolsStream() → 获取 stream channel
//   3. 遍历 stream channel：
//      - StreamEventDelta → 累加 fullContent + yield Delta 事件
//      - StreamEventDone → 提取 toolCalls 和 token 信息
//      - StreamEventError → yield Error 事件
//   4. 暂存 token 信息供后续累计
//
// 返回：
//   - content: 完整的文本内容（LLM 在工具调用前的思考文本）
//   - toolCalls: 工具调用列表（OpenAI 流式累加组装）
//   - ok: 是否成功
func (lc *queryLoopContext) callLLMStream(iter int) (string, []llm.ToolCall, bool) {
	// yield: 思考中。
	lc.events <- QueryEvent{
		Type:      QueryEventThink,
		Iteration: iter + 1,
	}

	// ── 流式调用 + prompt_too_long 补救 ──
	// 每次模型调用失败且为 too-long 错误时，reactiveCompact 压缩后重试（最多 1 次）。
	// 镜像 s08 的 reactive_compact：保最近 5 条 + 摘要前置。
	var fullContent string
	var toolCalls []llm.ToolCall
	var inputTokens, outputTokens int
	for {
		streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, lc.tools)
		streamErr := lc.drainStream(streamChan, iter, &fullContent, &toolCalls, &inputTokens, &outputTokens)
		if streamErr == nil {
			break // 成功
		}
		if lc.compactor != nil && isTooLongError(streamErr) && lc.reactiveRetries < maxReactiveRetries {
			lc.reactiveRetries++
			lc.messages = lc.compactor.reactiveCompact(lc.messages, lc.activeRequest)
			fullContent, toolCalls = "", nil
			continue // 压缩后重试
		}
		// 非 too-long 或重试耗尽：yield 错误并返回失败。
		lc.yieldError("stream error", streamErr)
		return "", nil, false
	}

	// 暂存 token 信息，供后续 accumulateTokens() 累计。
	lc.lastInputTokens = inputTokens
	lc.lastOutputTokens = outputTokens

	return fullContent, toolCalls, true
}

// drainStream 消费流式 channel，返回错误（nil = 成功）。
func (lc *queryLoopContext) drainStream(streamChan <-chan llm.StreamEvent, iter int, fullContent *string, toolCalls *[]llm.ToolCall, inputTokens, outputTokens *int) error {
	for streamEvent := range streamChan {
		switch streamEvent.Type {
		case llm.StreamEventDelta:
			// 增量文本：追加到 fullContent 并实时 yield 给上层。
			*fullContent += streamEvent.Content
			lc.events <- QueryEvent{
				Type:         QueryEventDelta,
				Content:      streamEvent.Content,
				Iteration:    iter + 1,
				InputTokens:  streamEvent.InputTokens,
				OutputTokens: streamEvent.OutputTokens,
			}

		case llm.StreamEventDone:
			// 流式完成：提取最终的工具调用列表和 token 统计。
			*toolCalls = streamEvent.ToolCalls
			*inputTokens = streamEvent.InputTokens
			*outputTokens = streamEvent.OutputTokens
			// usage.prompt_tokens 是本次请求输入消息的精确 token 数
			// （API 计算，含 role 标记/JSON schema 结构开销），
			// 供 accumulateTokens 累计到本轮 token 统计。
			if streamEvent.Content != "" {
				*fullContent = streamEvent.Content
			}

		case llm.StreamEventError:
			// 流式错误：返回错误（由调用方决定补救或退出）。
			return streamEvent.Error
		}
	}
	return nil
}

// accumulateTokens 把最近一次调用暂存的输入/输出 token（lastInputTokens/
// lastOutputTokens）累加到总计。
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

// detectDuplicateAndWarn 检测重复调用并警告。
//
// 检测方式：逐个检查工具调用，对每个 toolCall 生成签名（工具名:参数），
// 与 seenToolCalls 比较。如果签名已存在，注入 user 消息警告 LLM。
//
// 与之前按整批检测的区别：
//   - 整批检测：[list A, list B] 和 [list A] 签名不同 → 漏检
//   - 逐条检测：只要其中一条重复过就能发现
//
// 这避免了 LLM 陷入"重复调用相同工具"的死循环。
func (lc *queryLoopContext) detectDuplicateAndWarn(toolCalls []llm.ToolCall) {
	var duplicates []string
	for _, tc := range toolCalls {
		sig := singleCallSignature(tc)
		if sig == "" {
			continue
		}
		if lc.seenToolCalls[sig] {
			duplicates = append(duplicates, tc.Name)
		}
		lc.seenToolCalls[sig] = true
	}

	if len(duplicates) > 0 {
		// 注入警告：告诉 LLM 哪些工具被重复调用了。
		lc.messages = append(lc.messages, llm.ChatMessage{
			Role:    "user",
			Content: fmt.Sprintf("你已经调用过 %s 工具并获得了相同的结果。请根据已有信息直接给出最终回答，不要再调用任何工具。", strings.Join(duplicates, "、")),
		})
	}
}

// generateFinalSummary 达到最大迭代次数时，生成最终总结。
//
// 流程：
//   1. 注入 user 消息："请根据已有信息直接给出回答"
//   2. 再次调用 LLM 流式接口（不带工具调用能力）
//   3. 收集最终文本内容
//   4. yield Final 事件
func (lc *queryLoopContext) generateFinalSummary() {
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:    "user",
		Content: "你已经尝试了多次工具调用。请根据已有信息直接给出回答，不要再调用工具。",
	})

	// yield: 思考中（总结轮）。与 callLLMStream 开头的 Think 对齐：
	// inline UI 依赖 Think 重置流式状态（streamed），否则上一轮迭代的 delta
	// 会让 final 的 glamour 重印分支被跳过，总结文本不上屏。
	lc.events <- QueryEvent{
		Type:      QueryEventThink,
		Iteration: lc.maxIter,
	}

	// 兜底总结不带任何工具定义：此轮目的是根据已有信息直接作答，
	// 传 tools 会让 LLM 有机会再次返回 tool_calls，而本函数的事件循环
	// 不处理 toolCalls（旧实现因此产生过空答案）。
	streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, nil)
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

// yieldError yield 错误事件（通过 event channel 发送给上层）。
func (lc *queryLoopContext) yieldError(content string, err error) {
	lc.events <- QueryEvent{
		Type:    QueryEventError,
		Content: content,
		Error:   err,
	}
}

// yieldFinal yield 最终回答事件（通过 event channel 发送给上层）。
// 包含完整的 token 统计和查询结束后的完整消息数组（跨轮累积用）。
func (lc *queryLoopContext) yieldFinal(content string, iter int) {
	lc.events <- QueryEvent{
		Type:         QueryEventFinal,
		Content:      content,
		Iteration:    iter,
		InputTokens:  lc.totalInputTokens,
		OutputTokens: lc.totalOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
		Messages:     lc.messages,
	}
}

// yieldToolError yield 工具错误事件并推入 tool 消息到历史。
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

// yieldContinue yield 继续推理事件（通过 event channel 发送给上层）。
// 包含当前累计的 token 统计。
func (lc *queryLoopContext) yieldContinue(iter int) {
	lc.events <- QueryEvent{
		Type:         QueryEventContinue,
		Iteration:    iter,
		InputTokens:  lc.totalInputTokens,
		OutputTokens: lc.totalOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
	}
}
