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
	messages        []llm.ChatMessage     // 完整消息历史（system + user + assistant + tool）
	tools           []openai.Tool         // 工具定义列表（OpenAI function calling 格式）
	toolRegistry    *tool.Registry        // 工具注册表，用于查找和执行工具
	maxIter         int                   // 最大迭代次数
	contextLimit    int                   // 模型上下文窗口大小（token 数）
	events          chan<- QueryEvent     // 事件输出 channel（yield 事件到此）
	seenToolCalls   map[string]bool       // 已见过的工具调用签名（用于重复检测）
	totalInputTokens  int                 // 累计输入 token 数
	totalOutputTokens int                 // 累计输出 token 数
	lastInputTokens   int                 // 暂存每次调用的输入 token 数
	lastOutputTokens  int                 // 暂存每次调用的输出 token 数
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
//	│    │      ├─ checkToolPermission() ← 权限检查            │
//	│    │      ├─ toolRegistry.Get().Execute() ← 执行工具     │
//	│    │      └─ yield ToolCall / ToolResult / Permission    │
//	│    │                                                     │
//	│    ├─ 步骤 4d: detectDuplicateAndWarn()                  │
//	│    │    检测重复调用，注入警告消息                         │
//	│    │                                                     │
//	│    ├─ 步骤 4e: checkAndCompressContext()                 │
//	│    │    token 超过 80% 阈值时压缩旧消息                   │
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
//   - contextLimit: 模型的上下文窗口大小（token 数），用于压缩检查
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

// runLoop 执行核心 ReAct 循环。
//
// 循环体（每次迭代）：
//
//	1. 检查 ctx 是否被取消（支持 /stop）
//	2. 检查并压缩上下文（token 用量超过 80% 阈值）
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
		// token 用量超过 80% 阈值时，压缩旧工具调用消息（保留 system + 最近 2 轮）。
		if !lc.checkAndCompressContext(iter) {
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
		// 遍历所有工具调用：权限检查 → 查找工具 → 执行 → yield 结果。
		// 权限被拒绝时跳过该工具但继续执行其他工具。
		if !lc.executeToolCalls(toolCalls, iter) {
			return
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

// checkAndCompressContext 检查 token 用量，接近上限时压缩。
//
// 压缩策略：
//   - 保留 system prompt（第 0 条消息，始终不动）
//   - 保留最近 2 轮的工具调用（4 条消息：assistant + tool × 2）
//   - 压缩更早的工具调用：只保留摘要（"[已压缩] 调用工具: xxx" + "[已压缩] 工具执行成功/失败"）
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

	streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, lc.tools)

	var fullContent string
	var toolCalls []llm.ToolCall
	var inputTokens, outputTokens int

	for streamEvent := range streamChan {
		switch streamEvent.Type {
		case llm.StreamEventDelta:
			// 增量文本：追加到 fullContent 并实时 yield 给上层。
			fullContent += streamEvent.Content
			lc.events <- QueryEvent{
				Type:         QueryEventDelta,
				Content:      streamEvent.Content,
				Iteration:    iter + 1,
				InputTokens:  streamEvent.InputTokens,
				OutputTokens: streamEvent.OutputTokens,
			}

		case llm.StreamEventDone:
			// 流式完成：提取最终的工具调用列表和 token 统计。
			toolCalls = streamEvent.ToolCalls
			inputTokens = streamEvent.InputTokens
			outputTokens = streamEvent.OutputTokens
			if streamEvent.Content != "" {
				fullContent = streamEvent.Content
			}

		case llm.StreamEventError:
			// 流式错误：yield Error 事件并返回失败。
			lc.yieldError("stream error", streamEvent.Error)
			return "", nil, false
		}
	}

	// 暂存 token 信息，供后续 accumulateTokens() 累计。
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
// 流程：
//   1. yield ToolCall 事件（通知上层显示工具调用信息）
//   2. 遍历 toolCalls，对每个执行 executeSingleTool()
//      - 权限检查：deny → 跳过；confirm → yield Permission + 阻塞等待
//      - 查找工具：不存在 → yield 错误工具结果
//      - 执行工具：调用 tool.Execute()
//      - yield ToolResult 事件
//      - 推入 tool 消息到历史
//
// 返回：
//   - true: 继续执行
//   - false: 发生错误，需要退出
func (lc *queryLoopContext) executeToolCalls(toolCalls []llm.ToolCall, iter int) bool {
	// yield: 工具调用请求（含完整 toolCalls 列表和 token 统计）。
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
// 子流程（按顺序）：
//   1. checkToolPermission() → deny 跳过 / confirm 阻塞等待 / allow 继续
//   2. toolRegistry.Get() → 查找工具实现
//   3. tool.Execute() → 执行工具（shell 命令 / 文件读写）
//   4. yield ToolResult 事件
//   5. 推入 tool 消息到历史（LLM 下一轮可以看到工具结果）
//
// 返回：
//   - true: 继续执行（即使工具执行出错也继续，让 LLM 自行处理错误）
//   - false: 发生致命错误，需要退出
func (lc *queryLoopContext) executeSingleTool(tc llm.ToolCall, iter int) bool {
	// ── 子步骤 1: 权限检查 ──
	if !lc.checkToolPermission(tc, iter) {
		return true // 权限拒绝，但继续执行下一个工具
	}

	// ── 子步骤 2: 查找工具 ──
	t := lc.toolRegistry.Get(tc.Name)
	if t == nil {
		errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 3: 执行工具 ──
	result, execErr := t.Execute(tc.Arguments)
	if execErr != nil {
		// 工具执行出错时，将错误信息作为结果返回给 LLM。
		// LLM 会看到错误并尝试其他方案（而非直接失败）。
		result = fmt.Sprintf("工具执行出错: %v\n请尝试其他方案，不要重复相同的命令。", execErr)
	}

	// ── 子步骤 4: yield 工具执行结果 ──
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: result,
		IsError:    execErr != nil,
		Iteration:  iter + 1,
	}

	// ── 子步骤 5: 推入消息历史 ──
	// tool 角色消息包含 ToolCallID，LLM 可以关联到对应的 tool_call。
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:       "tool",
		Content:    result,
		ToolCallID: tc.ID,
	})

	return true
}

// checkToolPermission 检查工具权限（queryLoop 内部调用）。
//
// 三种结果：
//   - allow: 直接允许，继续执行
//   - deny: 拒绝执行，yield 错误工具结果（继续执行其他工具）
//   - confirm: yield Permission 事件，阻塞等待 channel 返回用户决策
//
// 阻塞机制：
//   queryLoop 创建 PermissionCh channel → yield Permission 事件
//   → QueryEngine 收到事件 → 调用 UI.ConfirmPermission()
//   → 用户在终端输入 y/N → 写入 PermissionCh
//   → queryLoop 从 PermissionCh 读取结果 → 继续或拒绝
func (lc *queryLoopContext) checkToolPermission(tc llm.ToolCall, iter int) bool {
	permResult := checkToolPermission(tc.Name, tc.Arguments)

	if permResult.Action == "deny" {
		// 禁止的工具：直接拒绝，yield 错误工具结果。
		errMsg := fmt.Sprintf("权限拒绝: %s", permResult.Message)
		lc.yieldToolError(tc, errMsg, iter)
		return false
	}

	if permResult.Action == "confirm" {
		// 需要确认：创建 channel，yield Permission 事件，阻塞等待结果。
		ch := make(chan bool, 1)
		lc.events <- QueryEvent{
			Type:               QueryEventPermission,
			PermissionRequired: true,
			PermissionTool:     tc.Name,
			PermissionArgs:     tc.Arguments,
			PermissionReason:   permResult.Message,
			PermissionCh:       ch,
			Iteration:          iter + 1,
		}

		// 阻塞等待用户确认（QueryEngine 收到事件后调用 UI.ConfirmPermission
		// 并将结果写入此 channel）。
		approved := <-ch
		if !approved {
			errMsg := "用户拒绝执行"
			lc.yieldToolError(tc, errMsg, iter)
			return false
		}
	}

	return true
}

// detectDuplicateAndWarn 检测重复调用并警告。
//
// 检测方式：将当前工具调用列表序列化为签名（工具名:参数），
// 与 seenToolCalls 比较。如果签名已存在，注入 user 消息警告 LLM。
//
// 这避免了 LLM 陷入"重复调用相同工具"的死循环。
func (lc *queryLoopContext) detectDuplicateAndWarn(toolCalls []llm.ToolCall) {
	currentToolCall := toolCallSignature(toolCalls)
	isDuplicate := currentToolCall != "" && lc.seenToolCalls[currentToolCall]

	if currentToolCall != "" {
		lc.seenToolCalls[currentToolCall] = true
	}

	if isDuplicate {
		// 注入警告：告诉 LLM 不要重复调用。
		lc.messages = append(lc.messages, llm.ChatMessage{
			Role:    "user",
			Content: "你已经调用过相同的工具并获得了相同的结果。请根据已有信息直接给出最终回答，不要再调用任何工具。",
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

// yieldError yield 错误事件（通过 event channel 发送给上层）。
func (lc *queryLoopContext) yieldError(content string, err error) {
	lc.events <- QueryEvent{
		Type:    QueryEventError,
		Content: content,
		Error:   err,
	}
}

// yieldFinal yield 最终回答事件（通过 event channel 发送给上层）。
// 包含完整的 token 统计。
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

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// estimateMessagesTokens 估算消息数组的 token 数。
//
// 计算方式：遍历所有消息，拼接内容（含工具调用名和参数），
// 使用 memory.EstimateTokens 估算（ASCII 约 4 字符/token，中文约 2 字符/token）。
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
//   - 保留 system prompt（第 0 条，始终不动）
//   - 保留最近 2 轮的工具调用（完整保留 4 条消息）
//   - 压缩更早的 assistant(含 toolCalls) 消息 → "[已压缩] 调用工具: xxx"
//   - 压缩更早的 tool(结果) 消息 → "[已压缩] 工具执行成功/失败"
//   - 非工具消息保留原样
//
// 这样在 token 接近上限时仍能保留上下文的关键信息。
func compressMessages(messages []llm.ChatMessage, _ int) []llm.ChatMessage {
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
			// 压缩工具结果消息（判断成功/失败）。
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
