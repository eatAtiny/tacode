// query_loop.go ReAct 核心循环（异步生成器模式）：流式调用 LLM、迭代与 token
// 统计、事件 yield、压缩管线衔接。工具执行子系统见 tool_exec.go。

package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"tacode/internal/llm"
	"tacode/internal/tool"

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
	messages          []llm.ChatMessage    // 完整消息历史（system + user + assistant + tool）
	tools             []openai.Tool        // 工具定义列表（OpenAI function calling 格式）
	toolRegistry      *tool.Registry       // 工具注册表，用于查找和执行工具
	maxIter           int                  // 最大迭代次数
	resultLimit       int                  // 结果截断上限（字符数，默认 8000）
	inputForward      <-chan string        // 控制命令（/interrupt）转发通道，nil = 不启用
	events            chan<- QueryEvent    // 事件输出 channel（yield 事件到此）
	seenToolCalls     map[string]bool      // 已见过的工具调用签名（用于重复检测）
	totalInputTokens  int                  // 累计输入 token 数
	totalOutputTokens int                  // 累计输出 token 数
	lastInputTokens   int                  // 暂存每次调用的输入 token 数
	lastOutputTokens  int                  // 暂存每次调用的输出 token 数
	fileReads         map[string]time.Time // 已读文件的 mtime（key=绝对路径，用于 read-before-edit 检测）
	compactor         *Compactor           // s08 四步压缩管线（nil = 禁用）
	activeRequest     string               // 当前轮用户请求（压缩时注入 [Compacted] 消息用）
	reactiveRetries   int                  // prompt_too_long 补救重试次数（上限 MAX_REACTIVE_RETRIES）
	abortTool         string               // 用户拒绝执行的那个工具名（终止提示用，由 checkToolPermission 写入）
}

// queryLoop 是纯粹的 Agent Loop 核心循环，使用异步生成器模式。
//
// 这是整个项目最核心的函数。负责 ReAct 的完整 while(true) 循环：
//
//	┌─────────────────────────────────────────────────────────┐
//	│  for iter := 0; iter < maxIter; iter++                  │
//	│    ├─ 步骤 1: ctx 取消检查（支持 /stop）                │
//	│    │                                                     │
//	│    ├─ 步骤 2: prepareIfNeeded()                         │
//	│    │    每次模型调用前运行 s08 四步压缩管线              │
//	│    │    （字符用量超 contextCharLimit（默认 50K）时压缩）│
//	│    │                                                     │
//	│    ├─ 步骤 3: callLLMStream()                           │
//	│    │    调用 LLM 流式接口，yield Think/Delta 事件         │
//	│    │    返回: content（文本）+ toolCalls（工具调用列表）    │
//	│    │                                                     │
//	│    ├─ 步骤 4: 累计 token 用量                            │
//	│    │                                                     │
//	│    ├─ 步骤 5: 推入 assistant 消息到历史                  │
//	│    │                                                     │
//	│    ├─ 步骤 6: 检查 toolCalls                             │
//	│    │    if len(toolCalls) == 0 → yield Final + return    │
//	│    │                                                     │
//	│    ├─ 步骤 7: executeToolCalls()                        │
//	│    │    对每个 toolCall:                                 │
//	│    │      ├─ checkToolPermission() ← 工具自检权限        │
//	│    │      ├─ toolRegistry.Get().Execute() ← 执行工具     │
//	│    │      ├─ TruncateResult() ← 统一截断                │
//	│    │      └─ yield ToolCall / ToolResult / Permission    │
//	│    │                                                     │
//	│    ├─ 步骤 8: detectDuplicateAndWarn()                  │
//	│    │    检测重复调用，注入警告消息                         │
//	│    │                                                     │
//	│    └─ 步骤 9: yield Continue → 进入下一轮迭代            │
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
	InputForward  <-chan string // 权限确认输入与控制命令（/interrupt）转发通道，nil = 不启用
	ActiveRequest string        // 当前轮用户请求（压缩时注入 [Compacted] 消息用）
	Compactor     *Compactor    // s08 四步压缩管线（nil = 禁用，保持旧行为）
}

// runLoop 执行核心 ReAct 循环。
//
// 循环体（每次迭代）：
//
//  1. 检查 ctx 是否被取消（支持 /stop）
//  2. 运行压缩管线（字符用量超过 contextCharLimit 时压缩，默认上限 50K）
//  3. 调用 LLM 流式接口 → 收集 content + toolCalls
//  4. 累计 token 用量
//  5. 推入 assistant 消息到历史
//  6. 如果无 toolCalls → 任务完成，yield Final + return
//  7. 执行工具调用（含权限检查）
//  8. 检测重复调用并警告
//  9. yield Continue → 回到步骤 1
func (lc *queryLoopContext) runLoop() {
	for iter := 0; iter < lc.maxIter; iter++ {
		// ── 步骤 1: 检查上下文是否被取消 ──
		// 支持 /stop 命令：Runner 调用 cancel() → ctx.Err() != nil
		if lc.ctx.Err() != nil {
			lc.yieldError(lc.ctx.Err())
			return
		}

		// ── 步骤 2: 检查并压缩上下文 ──
		// 每次模型调用前运行 s08 四步压缩管线（Prepare）。
		// compactor 为 nil 时保持旧行为（不压缩）。
		if !lc.prepareIfNeeded(iter) {
			return
		}

		// ── 步骤 3: 调用 LLM 流式接口 ──
		// 流式调用 LLM，通过 channel yield Delta 事件（实时增量文本）。
		// 返回完整的 content 文本和 toolCalls 列表。
		content, toolCalls, ok := lc.callLLMStream(iter)
		if !ok {
			return
		}

		// ── 步骤 4: 累计 token 用量 ──
		lc.accumulateTokens()

		// ── 步骤 5: 推入 assistant 响应到消息历史 ──
		lc.appendAssistantMessage(content, toolCalls)

		// ── 步骤 6: 检查是否完成 ──
		// 如果没有工具调用，LLM 直接给出了最终答案 → 任务完成。
		if len(toolCalls) == 0 {
			lc.yieldFinal(content)
			return
		}

		// ── 步骤 7: 执行工具调用 ──
		// 遍历所有工具调用：权限检查 → 查找工具 → 执行 → 截断 → yield 结果。
		// 全局策略拒绝只跳过该工具；用户拒绝则终止整个查询（见下）。
		if lc.executeToolCalls(toolCalls) == execAbortUser {
			// 用户拒绝执行工具 → 终止本次查询，不在本轮里自动重试。
			//
			// 代价是本轮内 LLM 失去了重新规划的机会；补偿是把这条记录留在
			// 累积 messages 里——下一轮用户开口时 LLM 看得到它，据此换方案。
			// 等于把重新规划从「循环内下一次迭代」挪到「对话的下一轮」，
			// 中间插入用户本人。
			//
			// 用 final 收束而非 error：Runner 只在 err == nil 时累积
			// result.messages（repl.go），走 error 路径这条记录会丢，
			// 下一轮 LLM 就看不到自己被执行过什么。
			text := fmt.Sprintf("⛔ 已拒绝执行 %s，本次查询已终止。", lc.abortTool)
			lc.appendAssistantMessage(text, nil)
			lc.yieldFinal(text)
			return
		}

		// ── 步骤 7-后置: compact 工具 ──
		// 整批工具执行完毕（tool 结果已全部追加）后，若模型请求了 compact，
		// 对该已闭合的回合执行历史摘要（镜像 s08：避免孤儿 tool_result，不丢副作用记录）。
		if lc.compactor != nil && lc.hasCompactRequest(toolCalls) {
			lc.messages = lc.compactor.compactHistory(lc.messages, lc.activeRequest)
		}

		// ── 步骤 8: 重复调用检测 ──
		// 如果 LLM 重复调用相同的工具+参数，注入警告消息引导 LLM 改变策略。
		lc.detectDuplicateAndWarn(toolCalls)

		// ── 步骤 9: yield 继续推理 ──
		// 通知上层进入下一轮迭代。
		lc.yieldContinue(iter + 1)
	}

	// ── 达到最大迭代次数 ──
	// 强制 LLM 根据已有信息生成最终总结（不调用工具）。
	lc.generateFinalSummary()
}

// prepareIfNeeded 每次模型调用前运行 s08 四步压缩管线。
//
// 与旧 checkAndCompressContext（该函数已删除）的差异：
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
		// 注：原先随事件携带的 "🗜️ 上下文接近上限，正在压缩..." 文本从未被消费
		// （ui.OnThink 只接收 iteration，无文本参数），剪枝后该提示彻底移除。
		lc.events <- ThinkEvent{Iteration: iter + 1}
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
//  1. yield Think 事件（通知上层开始思考）
//  2. 调用 llmClient.ChatWithToolsStream() → 获取 stream channel
//  3. 遍历 stream channel：
//     - StreamEventDelta → 累加 fullContent + yield Delta 事件
//     - StreamEventDone → 提取 toolCalls 和 token 信息
//     - StreamEventError → yield Error 事件
//  4. 暂存 token 信息供后续累计
//
// 返回：
//   - content: 完整的文本内容（LLM 在工具调用前的思考文本）
//   - toolCalls: 工具调用列表（OpenAI 流式累加组装）
//   - ok: 是否成功
func (lc *queryLoopContext) callLLMStream(iter int) (string, []llm.ToolCall, bool) {
	// yield: 思考中。
	lc.events <- ThinkEvent{Iteration: iter + 1}

	// ── 流式调用 + prompt_too_long 补救 ──
	// 每次模型调用失败且为 too-long 错误时，reactiveCompact 压缩后重试（最多 1 次）。
	// 镜像 s08 的 reactive_compact：保最近 5 条 + 摘要前置。
	var fullContent string
	var toolCalls []llm.ToolCall
	var inputTokens, outputTokens int
	for {
		streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, lc.tools)
		streamErr := lc.drainStream(streamChan, &fullContent, &toolCalls, &inputTokens, &outputTokens)
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
		lc.yieldError(streamErr)
		return "", nil, false
	}

	// 暂存 token 信息，供后续 accumulateTokens() 累计。
	lc.lastInputTokens = inputTokens
	lc.lastOutputTokens = outputTokens

	return fullContent, toolCalls, true
}

// drainStream 消费流式 channel，返回错误（nil = 成功）。
func (lc *queryLoopContext) drainStream(streamChan <-chan llm.StreamEvent, fullContent *string, toolCalls *[]llm.ToolCall, inputTokens, outputTokens *int) error {
	for streamEvent := range streamChan {
		switch streamEvent.Type {
		case llm.StreamEventDelta:
			// 增量文本：追加到 fullContent 并实时 yield 给上层。
			*fullContent += streamEvent.Content
			lc.events <- DeltaEvent{Content: streamEvent.Content}

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

// singleCallSignature 生成单个工具调用的签名，用于检测重复调用。
//
// 签名格式：工具名:参数JSON
// 与之前的 batch 签名不同，逐条检测能发现单个 toolCall 级别的重复。
//
// 返回空字符串表示空的 tool call。
func singleCallSignature(tc llm.ToolCall) string {
	if tc.Name == "" {
		return ""
	}
	return fmt.Sprintf("%s:%s", tc.Name, tc.Arguments)
}

// generateFinalSummary 达到最大迭代次数时，生成最终总结。
//
// 流程：
//  1. 注入 user 消息："请根据已有信息直接给出回答"
//  2. 再次调用 LLM 流式接口（不带工具调用能力）
//  3. 收集最终文本内容
//  4. yield Final 事件
func (lc *queryLoopContext) generateFinalSummary() {
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:    "user",
		Content: "你已经尝试了多次工具调用。请根据已有信息直接给出回答，不要再调用工具。",
	})

	// yield: 思考中（总结轮）。与 callLLMStream 开头的 Think 对齐：
	// inline UI 依赖 Think 重置流式状态（streamed），否则上一轮迭代的 delta
	// 会让 final 的 glamour 重印分支被跳过，总结文本不上屏。
	lc.events <- ThinkEvent{Iteration: lc.maxIter}

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
			lc.yieldError(streamEvent.Error)
			return
		}
	}

	lc.totalInputTokens += finalInputTokens
	lc.totalOutputTokens += finalOutputTokens

	lc.yieldFinal(finalContent)
}

// ──────────────────────────────────────────────────────────
// yield 辅助函数
// ──────────────────────────────────────────────────────────

// yieldError yield 错误事件（通过 event channel 发送给上层）。
func (lc *queryLoopContext) yieldError(err error) {
	lc.events <- LoopError{Err: err}
}

// yieldFinal yield 最终回答事件（通过 event channel 发送给上层）。
// 包含完整的 token 统计和查询结束后的完整消息数组（跨轮累积用）。
func (lc *queryLoopContext) yieldFinal(content string) {
	lc.events <- FinalEvent{
		Content:      content,
		InputTokens:  lc.totalInputTokens,
		OutputTokens: lc.totalOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
		Messages:     lc.messages,
	}
}

// yieldToolError yield 工具错误事件并推入 tool 消息到历史。
func (lc *queryLoopContext) yieldToolError(tc llm.ToolCall, errMsg string) {
	lc.events <- ToolResultEvent{
		ToolName:   tc.Name,
		ToolResult: errMsg,
		IsError:    true,
	}

	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:       "tool",
		Content:    errMsg,
		ToolCallID: tc.ID,
	})
}

// yieldContinue yield 继续推理事件（通过 event channel 发送给上层）。
func (lc *queryLoopContext) yieldContinue(iter int) {
	lc.events <- ContinueEvent{Iteration: iter}
}
