package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/tool"

	openai "github.com/sashabaranov/go-openai"
)

// ──────────────────────────────────────────────────────────
// 常量定义（默认值，可由 config 覆盖）
// ──────────────────────────────────────────────────────────

// defaultCompressThreshold 默认压缩阈值：token 使用率超过 80% 触发压缩。
const defaultCompressThreshold = 0.8

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
	contextLimit      int                    // 模型上下文窗口大小（token 数）
	compressThreshold float64                // 压缩阈值（token 使用率，默认 0.8）
	resultLimit       int                    // 结果截断上限（字符数，默认 8000）
	inputForward      <-chan string          // 输入转发通道（/interrupt 等控制命令），nil = 不启用
	events            chan<- QueryEvent      // 事件输出 channel（yield 事件到此）
	seenToolCalls     map[string]bool        // 已见过的工具调用签名（用于重复检测）
	totalInputTokens  int                    // 累计输入 token 数
	totalOutputTokens int                    // 累计输出 token 数
	lastInputTokens   int                    // 暂存每次调用的输入 token 数
	lastOutputTokens  int                    // 暂存每次调用的输出 token 数
	currentIter       int                    // 当前迭代次数（用于 mtime 追踪）
	fileReads         map[string]time.Time   // 已读文件的 mtime（key=绝对路径，用于 read-before-edit 检测）
	msgTokens         int                    // 消息数组精确 token 数（来自 API usage），-1 = 未知（用估算）
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
//   - opts: 可选配置（压缩阈值、结果截断上限），零值使用默认
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
	opts ...queryLoopOptions,
) <-chan QueryEvent {
	events := make(chan QueryEvent)

	go func() {
		defer close(events)

		// 应用可选配置（零值回退默认）。
		compressThreshold := defaultCompressThreshold
		resultLimit := defaultResultLimit
		var inputForward <-chan string
		if len(opts) > 0 {
			if opts[0].CompressThreshold > 0 {
				compressThreshold = opts[0].CompressThreshold
			}
			if opts[0].ResultLimit > 0 {
				resultLimit = opts[0].ResultLimit
			}
			inputForward = opts[0].InputForward
		}

		// 初始化循环上下文。
		lc := &queryLoopContext{
			ctx:               ctx,
			llmClient:         llmClient,
			messages:          messages,
			tools:             tools,
			toolRegistry:      toolRegistry,
			maxIter:           maxIter,
			contextLimit:      contextLimit,
			compressThreshold: compressThreshold,
			resultLimit:       resultLimit,
			inputForward:      inputForward,
			events:            events,
			seenToolCalls:     make(map[string]bool),
			fileReads:         make(map[string]time.Time),
			msgTokens:         -1, // 未知，首次压缩判断用估算
		}

		// 执行核心循环。
		lc.runLoop()
	}()

	return events
}

// queryLoopOptions queryLoop 的可选配置参数。
// 零值表示使用默认值。
type queryLoopOptions struct {
	CompressThreshold float64 // token 使用率压缩阈值（默认 0.8）
	ResultLimit       int     // 结果截断上限，字符数（默认 8000）
	InputForward      <-chan string // 输入转发通道（/interrupt 等控制命令），nil = 不启用
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
		lc.currentIter = iter

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
		// 遍历所有工具调用：权限检查 → 查找工具 → 执行 → 截断 → yield 结果。
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

	// token 用量：优先用 API usage 校准的精确值（msgTokens），
	// 未知（-1）或压缩后失效时回退到启发式估算。
	totalTokens := lc.msgTokens
	if totalTokens <= 0 {
		totalTokens = estimateMessagesTokens(lc.messages)
	}
	usageRatio := float64(totalTokens) / float64(lc.contextLimit)

	if usageRatio <= lc.compressThreshold {
		return true
	}

	// yield: 压缩事件。
	lc.events <- QueryEvent{
		Type:      QueryEventThink,
		Content:   "🗜️ 上下文接近上限，正在压缩...",
		Iteration: iter + 1,
	}

	// 压缩前持久化被丢弃的工具结果（保真：LLM 可回读），失败不阻断压缩。
	lc.messages = lc.compressMessagesWithPersistence(lc.messages, iter, tool.DefaultToolResultsDir)
	// 压缩改变了消息内容，旧账本失效：标记未知，下次用估算（或用新的 usage 重新校准）。
	lc.msgTokens = -1
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
			// usage.prompt_tokens 是本次请求输入消息的精确 token 数
			// （API 计算，含 role 标记/JSON schema 结构开销）。
			// 用它校准消息数组的 token 账本；0 表示 API 未返回（兜底估算）。
			if inputTokens > 0 {
				lc.msgTokens = inputTokens
			}
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
// 重构后支持并行执行：
//  1. 分类：并发安全工具（IsConcurrencySafe + IsReadOnly + Allow permission）
//     → 用 goroutine 并行执行
//  2. 其余工具 → 串行执行
//
// 并行执行时收集完整结果，然后按原始顺序 yield 事件和推入消息。
// 设计参考 Claude Code 的 StreamingToolExecutor。
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

	// ── 分类：并发安全 vs 串行 ──
	//
	// 并发安全条件（三者同时满足）：
	//   1. 工具存在
	//   2. IsConcurrencySafe(args) == true
	//   3. IsReadOnly(args) == true
	//   4. CheckPermission(args).Allow == true（避免并发弹窗）
	type execItem struct {
		tc         llm.ToolCall
		t          tool.Tool
		concurrent bool
	}

	var concurrentItems []execItem
	var serialItems []execItem

	for _, tc := range toolCalls {
		t := lc.toolRegistry.Get(tc.Name)
		item := execItem{tc: tc, t: t}

		if t != nil && t.IsConcurrencySafe(tc.Arguments) && t.IsReadOnly(tc.Arguments) {
			if perm := t.CheckPermission(tc.Arguments); perm.Allow {
				item.concurrent = true
				concurrentItems = append(concurrentItems, item)
				continue
			}
		}
		serialItems = append(serialItems, item)
	}

	// ── 阶段 1: 并行执行并发安全工具 ──
	if len(concurrentItems) > 0 {
		concurrentTCs := make([]llm.ToolCall, len(concurrentItems))
		for i, item := range concurrentItems {
			concurrentTCs[i] = item.tc
		}
		lc.executeConcurrentTools(concurrentTCs, iter)
	}

	// ── 阶段 2: 串行执行其余工具 ──
	// 每执行一个工具前检查中断信号（/interrupt 等）：收到则中止后续工具，
	// 注入中断提示让 LLM 知道当前状态，继续循环让 LLM 调整策略。
	for _, item := range serialItems {
		if cmd := lc.peekInterrupt(); cmd != "" {
			lc.injectInterruptNotice(item.tc, iter, cmd)
			return true
		}
		if !lc.executeSingleTool(item.tc, iter) {
			return false
		}
	}

	return true
}

// peekInterrupt 非阻塞检查输入转发通道是否有控制命令（/interrupt、/retry）。
// 返回命令字符串；无命令或通道未启用时返回空字符串。
func (lc *queryLoopContext) peekInterrupt() string {
	if lc.inputForward == nil {
		return ""
	}
	select {
	case cmd := <-lc.inputForward:
		cmd = strings.TrimSpace(cmd)
		if cmd == "/interrupt" || strings.HasPrefix(cmd, "/retry") {
			return cmd
		}
		return ""
	default:
		return ""
	}
}

// injectInterruptNotice 注入中断提示到消息历史。
// 让 LLM 知道用户中断了工具执行，可据此调整策略（或直接回答）。
func (lc *queryLoopContext) injectInterruptNotice(tc llm.ToolCall, iter int, cmd string) {
	notice := fmt.Sprintf("用户已中断工具 %s 的执行（%s）。"+
		"请根据已有信息调整策略：可直接给出回答，或尝试其他方案。", tc.Name, cmd)
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:    "user",
		Content: notice,
	})
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: notice,
		IsError:    true,
		Iteration:  iter + 1,
	}
}

// toolExecResult 工具执行结果（用于并行执行收集）。
type toolExecResult struct {
	result  string
	isError bool
}

// executeConcurrentTools 并行执行一组并发安全工具。
//
// 所有工具并发执行（goroutine + WaitGroup），
// 但结果按原始顺序 yield 和推入消息（保证 LLM 上下文一致性）。
//
// 前置条件：传入的 toolCalls 均已通过并发安全检查
// （IsConcurrencySafe + IsReadOnly + CheckPermission.Allow）。
//
// 线程安全：
//   - 各工具读取独立文件，无竞争
//   - fileReads map 在并行写时由 mu 保护
//   - event channel 只在主 goroutine 写入
func (lc *queryLoopContext) executeConcurrentTools(toolCalls []llm.ToolCall, iter int) {
	// 结果切片（预分配，按索引存储，保持原始顺序）。
	results := make([]toolExecResult, len(toolCalls))

	var wg sync.WaitGroup
	var mu sync.Mutex // 保护 fileReads map

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, tc llm.ToolCall) {
			defer wg.Done()

			t := lc.toolRegistry.Get(tc.Name)
			if t == nil {
				results[idx] = toolExecResult{
					result:  fmt.Sprintf("未知工具: %s", tc.Name),
					isError: true,
				}
				return
			}

			// 注入读取状态（read-before-edit 内聚）。
			if aware, ok := t.(tool.ReadStateAware); ok {
				aware.SetReadState(lc.fileReads)
			}

			// Pre-tool hooks。
			if hookErr := lc.toolRegistry.BeforeHooks(tc.Name, tc.Arguments); hookErr != nil {
				results[idx] = toolExecResult{
					result:  fmt.Sprintf("工具执行被阻止: %v", hookErr),
					isError: true,
				}
				return
			}

			result, execErr := t.Execute(tc.Arguments)
			if execErr != nil {
				result = fmt.Sprintf("工具执行出错: %v\n请分析错误原因并尝试其他方案。", execErr)
			}

			if strings.TrimSpace(result) == "" {
				result = "(无输出)"
			}

			// 统一截断 + 大结果持久化。
			limit := t.ResultLimit()
			if limit <= 0 {
				limit = lc.resultLimit
			}
			if len(result) > limit {
				fullResult := result
				result = tool.TruncateResult(result, limit)
				if savedPath, err := tool.SaveLargeResult(tool.DefaultToolResultsDir, tc.Name, fullResult); err == nil {
					result += fmt.Sprintf("\n\n💾 完整结果已保存到: %s（可使用 file read 读取）", savedPath)
				}
			}

			// Post-tool hooks。
			lc.toolRegistry.AfterHooks(tc.Name, tc.Arguments, result, execErr)

			// 记录文件 mtime（并行安全：mu 保护）。
			if absPath, ok := extractFilePath(tc.Name, tc.Arguments); ok {
				if info, err := os.Stat(absPath); err == nil {
					mu.Lock()
					lc.fileReads[absPath] = info.ModTime()
					mu.Unlock()
				}
			}

			results[idx] = toolExecResult{
				result:  result,
				isError: execErr != nil,
			}
		}(i, tc)
	}

	wg.Wait()

	// 按原始顺序 yield 事件 + 推入消息。
	for i, tc := range toolCalls {
		r := results[i]

		lc.events <- QueryEvent{
			Type:       QueryEventToolResult,
			ToolName:   tc.Name,
			ToolResult: r.result,
			IsError:    r.isError,
			Iteration:  iter + 1,
		}

		lc.messages = append(lc.messages, llm.ChatMessage{
			Role:       "tool",
			Content:    r.result,
			ToolCallID: tc.ID,
		})
	}
}

// executeSingleTool 执行单个工具调用。
//
// 子流程（按顺序，重构后）：
//   1. 全局禁止列表检查（isToolForbidden → 直接拒绝）
//   2. 工具自检权限（Tool.CheckPermission → Allow/Confirm）
//   3. toolRegistry.Get() → 查找工具实现
//   4. read-before-edit 检测（edit 工具：验证文件已读 + mtime 未变）
//   5. tool.Execute() → 执行工具
//   6. 统一结果截断（Tool.ResultLimit + TruncateResult）
//   7. yield ToolResult 事件
//   8. 记录文件 mtime（用于 read-before-edit）
//   9. 推入 tool 消息到历史
//
// 返回：
//   - true: 继续执行（即使工具执行出错也继续，让 LLM 自行处理错误）
//   - false: 发生致命错误，需要退出
func (lc *queryLoopContext) executeSingleTool(tc llm.ToolCall, iter int) bool {
	// ── 子步骤 1: 全局禁止列表 ──
	if isToolForbidden(tc.Name) {
		errMsg := fmt.Sprintf("工具已被禁止使用: %s", tc.Name)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 2: 查找工具 ──
	t := lc.toolRegistry.Get(tc.Name)
	if t == nil {
		errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 3: 权限检查（工具自检） ──
	if !lc.checkToolPermission(tc, t, iter) {
		return true // 权限拒绝，但继续执行下一个工具
	}

	// ── 子步骤 4: 注入读取状态（read-before-edit 内聚） ──
	if aware, ok := t.(tool.ReadStateAware); ok {
		aware.SetReadState(lc.fileReads)
	}

	// ── 子步骤 4b: Pre-tool hooks ──
	if hookErr := lc.toolRegistry.BeforeHooks(tc.Name, tc.Arguments); hookErr != nil {
		errMsg := fmt.Sprintf("工具执行被钩子阻止: %v", hookErr)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 5: 执行工具 ──
	result, execErr := t.Execute(tc.Arguments)
	if execErr != nil {
		// 工具执行出错时，将错误信息作为结果返回给 LLM。
		// LLM 会看到错误并尝试其他方案（而非直接失败）。
		result = fmt.Sprintf("工具执行出错: %v\n请分析错误原因并尝试其他方案。", execErr)
	}

	// 空结果保护：命令成功但无输出时（如 mkdir、空 grep），
	// 用 "(无输出)" 占位，避免空 content 导致 API 报错。
	if strings.TrimSpace(result) == "" {
		result = "(无输出)"
	}

	// ── 子步骤 6: 统一结果截断 + 大结果持久化 ──
	// 框架层统一处理截断（head+tail 保留策略），工具无需自行截断。
	// 超过上限时：完整结果写入磁盘 → 模型可后续通过 file read 获取。
	limit := t.ResultLimit()
	if limit <= 0 {
		limit = lc.resultLimit
	}
	if len(result) > limit {
		fullResult := result
		result = tool.TruncateResult(result, limit)
		// 持久化完整结果到磁盘。
		if savedPath, err := tool.SaveLargeResult(tool.DefaultToolResultsDir, tc.Name, fullResult); err == nil {
			result += fmt.Sprintf("\n\n💾 完整结果已保存到: %s（可使用 file read 读取）", savedPath)
		}
	}

	// ── Post-tool hooks ──
	lc.toolRegistry.AfterHooks(tc.Name, tc.Arguments, result, execErr)

	// ── 子步骤 7: yield 工具执行结果 ──
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: result,
		IsError:    execErr != nil,
		Iteration:  iter + 1,
	}

	// ── 子步骤 8: 记录文件 mtime（用于 read-before-edit 检测） ──
	lc.recordFileRead(tc.Name, tc.Arguments)

	// ── 子步骤 9: 推入消息历史 ──
	// tool 角色消息包含 ToolCallID，LLM 可以关联到对应的 tool_call。
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:       "tool",
		Content:    result,
		ToolCallID: tc.ID,
	})

	return true
}

// checkToolPermission 使用工具自身的 CheckPermission 方法检查权限。
//
// 重构后逻辑：
//   - 先检查全局权限注入点（globalPermissionChecker，非 nil 时覆盖）
//   - 否则调用 Tool.CheckPermission(args)
//     → Allow=true → 直接允许
//     → Allow=false → yield Permission 事件 → 阻塞等待用户确认
//
// 阻塞机制：
//   queryLoop 创建 PermissionCh channel → yield Permission 事件
//   → QueryEngine 收到事件 → 调用 UI.ConfirmPermission()
//   → 用户在终端输入 y/N → 写入 PermissionCh
//   → queryLoop 从 PermissionCh 读取结果 → 继续或拒绝
func (lc *queryLoopContext) checkToolPermission(tc llm.ToolCall, t tool.Tool, iter int) bool {
	// ── 全局权限注入点（极端定制场景） ──
	if globalPermissionChecker != nil {
		if !globalPermissionChecker.CheckPermission(tc.Name, tc.Arguments) {
			errMsg := "全局权限策略拒绝执行"
			lc.yieldToolError(tc, errMsg, iter)
			return false
		}
		return true
	}

	// ── 工具自检权限 ──
	perm := t.CheckPermission(tc.Arguments)
	if perm.Allow {
		return true
	}

	// ── 需要确认 ──
	ch := make(chan bool, 1)
	lc.events <- QueryEvent{
		Type:               QueryEventPermission,
		PermissionRequired: true,
		PermissionTool:     tc.Name,
		PermissionArgs:     tc.Arguments,
		PermissionReason:   perm.Reason,
		PermissionCh:       ch,
		Iteration:          iter + 1,
	}

	// 阻塞等待用户确认。
	approved := <-ch
	if !approved {
		errMsg := "用户拒绝执行"
		lc.yieldToolError(tc, errMsg, iter)
		return false
	}

	// 用户确认通过：若工具支持放行接口（如 ShellTool 的 network 放行），
	// 通知工具记录"已放行"，Execute 时据此重建允许网络/权限的沙箱。
	if allow, ok := t.(interface{ AllowNetworkFor(args string) }); ok {
		allow.AllowNetworkFor(tc.Arguments)
	}

	return true
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
// Read-Before-Edit + mtime 追踪
// ──────────────────────────────────────────────────────────

// extractFilePath 从工具参数中提取文件绝对路径。
//
// 返回绝对路径和是否成功提取。
// 用于 mtime 追踪的集中提取逻辑，避免在多个函数中重复解析。
func extractFilePath(toolName string, args string) (string, bool) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil || params.Path == "" {
		return "", false
	}
	absPath, err := filepath.Abs(params.Path)
	if err != nil {
		return "", false
	}
	return absPath, true
}

// recordFileRead 从工具参数中提取文件路径，记录其 mtime。
//
// 用于 read-before-edit 检测：编辑类工具（edit、file write）执行前，
// 通过 checkReadBeforeEdit 验证目标文件已被读取且未被外部修改。
//
// 只处理包含 "path" 参数的工具：file(read)、edit、grep（文件模式时）。
func (lc *queryLoopContext) recordFileRead(toolName string, args string) {
	absPath, ok := extractFilePath(toolName, args)
	if !ok {
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return
	}

	lc.fileReads[absPath] = info.ModTime()
}

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// estimateMessagesTokens 估算消息数组的 token 数。
//
// 计算方式：遍历所有消息，对每条消息的 content、工具调用名和参数
// 逐段调用 memory.EstimateTokens 累加（避免拼接大字符串的重复分配）。
// 结果乘 1.2 安全系数，覆盖 role 标记、JSON schema 等结构性开销——
// 纯文本估算会低估真实用量，压缩判断宁可偏早不可偏晚。
func estimateMessagesTokens(messages []llm.ChatMessage) int {
	var total int
	for _, msg := range messages {
		total += memory.EstimateTokens(msg.Content)
		for _, tc := range msg.ToolCalls {
			total += memory.EstimateTokens(tc.Name + tc.Arguments)
		}
	}
	return total + total/5 // *1.2 安全系数（total/5 为整数除法）
}

// compressMessages 压缩旧的工具调用消息，减少 token 用量。
//
// 策略：
//   - 保留 system prompt（第 0 条，始终不动）
//   - 保留最近 2 轮的工具调用（完整保留 4 条消息）
//   - 压缩更早的 assistant(含 toolCalls) 消息 → "[已压缩] 调用工具: xxx"
//   - 压缩更早的 tool(结果) 消息 → "[已压缩] 工具结果（完整内容已持久化）"
//   - 非工具消息保留原样
//
// 与旧实现的差异：
//   - tool 结果占位符改为中性描述，不再用 Contains("出错"/"错误") 猜测状态
//     （工具输出含"错误码说明"文档也会被误判为失败）
//   - 被压缩的完整结果由调用方（compressMessagesWithPersistence）持久化，
//     LLM 可通过 file read 回读，避免信息不可逆丢失
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
			// 注意：必须保留 ToolCalls 结构！OpenAI API 要求 role=tool 消息
			// 紧跟一条带匹配 tool_calls 的 assistant，否则返回 400
			// ("Messages with role 'tool' must be a response to a preceding message with 'tool_calls'")。
			// 只替换 Content 为占位符，ToolCalls 原样保留以维持配对。
			toolNames := make([]string, len(msg.ToolCalls))
			for j, tc := range msg.ToolCalls {
				toolNames[j] = tc.Name
			}
			compressed = append(compressed, llm.ChatMessage{
				Role:      "assistant",
				Content:   fmt.Sprintf("[已压缩] 调用工具: %s", strings.Join(toolNames, ", ")),
				ToolCalls: msg.ToolCalls, // 保留，避免产生孤儿 tool 消息
			})
		} else if msg.Role == "tool" {
			// 压缩工具结果消息：中性占位符（不再猜测成功/失败）。
			// 完整内容由 compressMessagesWithPersistence 预先持久化，
			// 占位符带上回读提示（持久化失败时退化为纯占位符）。
			compressed = append(compressed, llm.ChatMessage{
				Role:       "tool",
				Content:    "[已压缩] 工具结果（完整内容已持久化，可使用 file read 读取）",
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

// compressMessagesWithPersistence 压缩消息，并在压缩前把被压缩的 tool 结果
// 完整持久化到 toolResultsDir（默认 data/tool-results），占位符带回读路径。
//
// 调用时机：checkAndCompressContext 触发压缩时。
// 持久化失败不阻断压缩（降级为纯占位符，与旧行为一致）。
func (lc *queryLoopContext) compressMessagesWithPersistence(messages []llm.ChatMessage, iter int, toolResultsDir string) []llm.ChatMessage {
	if len(messages) <= 3 {
		return messages
	}

	// 先持久化将被压缩的 tool 结果，再替换占位符。
	compressEnd := len(messages) - 4
	if compressEnd < 1 {
		compressEnd = 1
	}

	// 为每条将被压缩的 tool 消息持久化完整内容，并把路径写进占位符。
	// 需要先持久化再构造消息：遍历被压缩区间，收集 tool 结果。
	var toolResults []struct {
		content   string
		toolCallID string
	}
	for i := 1; i < compressEnd; i++ {
		msg := messages[i]
		if msg.Role == "tool" && strings.TrimSpace(msg.Content) != "" {
			toolResults = append(toolResults, struct {
				content   string
				toolCallID string
			}{content: msg.Content, toolCallID: msg.ToolCallID})
		}
	}

	// 持久化每个结果。
	if toolResultsDir == "" {
		toolResultsDir = tool.DefaultToolResultsDir
	}
	savedPaths := make(map[string]string, len(toolResults)) // key=toolCallID
	for _, tr := range toolResults {
		if tr.content == "" {
			continue
		}
		if path, err := tool.SaveLargeResult(toolResultsDir, "compressed", tr.content); err == nil {
			savedPaths[tr.toolCallID] = path
		}
	}

	compressed := compressMessages(messages, iter)

	// 把占位符替换为带回读路径的版本（仅当持久化成功）。
	if len(savedPaths) > 0 {
		for i := range compressed {
			if compressed[i].Role != "tool" {
				continue
			}
			if path, ok := savedPaths[compressed[i].ToolCallID]; ok {
				compressed[i].Content = fmt.Sprintf("[已压缩] 工具结果（完整内容已保存到: %s，可使用 file read 读取）", path)
			}
		}
	}

	return compressed
}
