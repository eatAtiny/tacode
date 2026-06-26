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

// 压缩阈值：token 使用率超过 80% 触发压缩。
const compressThreshold = 0.8

// ──────────────────────────────────────────────────────────
// queryLoop 核心循环（异步生成器模式）
// ──────────────────────────────────────────────────────────

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
//   - tool_call: 工具调用请求（包含 ToolCalls 字段）
//   - tool_result: 工具执行结果（包含 ToolName、ToolResult、IsError 字段）
//   - continue: 继续推理（工具执行完毕，继续下一轮循环）
//   - final: 最终回答（包含 Content 字段）
//   - error: 错误（包含 Error 字段）
//
// 压缩机制：
//   - 每次迭代检查 token 用量
//   - 当使用率超过 80% 时，压缩早期的工具调用消息
//   - 保留最近 2 轮的工具调用，压缩更早的
//   - 压缩后的消息只保留工具名称和成功/失败状态
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
	// 创建事件 channel，用于传递中间状态。
	events := make(chan QueryEvent)

	// 启动 goroutine 执行核心循环。
	go func() {
		defer close(events) // 循环结束时关闭 channel。

		seenToolCalls := make(map[string]bool) // 记录所有工具调用签名，用于重复检测。

		// ── 核心循环：while(true) ──
		for iter := 0; iter < maxIter; iter++ {
			// 检查 token 用量，接近上限时压缩旧的工具调用消息。
			if contextLimit > 0 {
				totalTokens := estimateMessagesTokens(messages)
				usageRatio := float64(totalTokens) / float64(contextLimit)
				if usageRatio > compressThreshold {
					// yield: 压缩事件（上层可以显示压缩提示）。
					events <- QueryEvent{
						Type:      QueryEventThink,
						Content:   "🗜️ 上下文接近上限，正在压缩...",
						Iteration: iter + 1,
					}
					messages = compressMessages(messages, iter)
				}
			}
			// 检查上下文是否被取消（用户中断或超时）。
			if ctx.Err() != nil {
				events <- QueryEvent{
					Type:    QueryEventError,
					Content: "query loop cancelled",
					Error:   ctx.Err(),
				}
				return
			}

			// yield: 思考中（上层可以显示 loading 动画）。
			events <- QueryEvent{
				Type:      QueryEventThink,
				Iteration: iter + 1,
			}

			// 调用 LLM，获取响应。
			resp, err := llmClient.ChatWithTools(ctx, messages, tools)
			if err != nil {
				events <- QueryEvent{
					Type:    QueryEventError,
					Content: "llm call failed",
					Error:   err,
				}
				return
			}

			// assistant 响应推入消息历史。
			messages = append(messages, llm.ChatMessage{
				Role:      "assistant",
				Content:   resp.Content,
				ToolCalls: resp.ToolCalls,
			})

			// 没有工具调用 → 任务完成，yield 最终回答。
			if resp.Finish {
				events <- QueryEvent{
					Type:      QueryEventFinal,
					Content:   resp.Content,
					Iteration: iter + 1,
				}
				return
			}

			// ── 有工具调用，执行工具 ──

			// yield: 工具调用请求（上层可以显示工具调用 UI）。
			events <- QueryEvent{
				Type:      QueryEventToolCall,
				ToolCalls: resp.ToolCalls,
				Iteration: iter + 1,
			}

			// 生成本次工具调用的签名（工具名+参数），用于重复检测。
			currentToolCall := toolCallSignature(resp.ToolCalls)
			isDuplicate := currentToolCall != "" && seenToolCalls[currentToolCall]
			if currentToolCall != "" {
				seenToolCalls[currentToolCall] = true
			}

			// 逐个执行工具调用。
			for _, tc := range resp.ToolCalls {
				// 查找工具。
				t := toolRegistry.Get(tc.Name)
				if t == nil {
					errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
					// yield: 工具错误结果。
					events <- QueryEvent{
						Type:       QueryEventToolResult,
						ToolName:   tc.Name,
						ToolResult: errMsg,
						IsError:    true,
						Iteration:  iter + 1,
					}
					// 错误结果推入消息历史。
					messages = append(messages, llm.ChatMessage{
						Role:       "tool",
						Content:    errMsg,
						ToolCallID: tc.ID,
					})
					continue
				}

				// 执行工具。
				result, execErr := t.Execute(tc.Arguments)
				if execErr != nil {
					result = fmt.Sprintf("工具执行出错: %v\n请尝试其他方案，不要重复相同的命令。", execErr)
				}

				// yield: 工具执行结果（上层可以显示结果 UI）。
				events <- QueryEvent{
					Type:       QueryEventToolResult,
					ToolName:   tc.Name,
					ToolResult: result,
					IsError:    execErr != nil,
					Iteration:  iter + 1,
				}

				// 工具结果推入消息历史（OpenAI API 要求使用 tool role）。
				messages = append(messages, llm.ChatMessage{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
				})
			}

			// 重复调用检测：如果调用了相同的工具+参数，强制 LLM 总结。
			if isDuplicate {
				messages = append(messages, llm.ChatMessage{
					Role:    "user",
					Content: "你已经调用过相同的工具并获得了相同的结果。请根据已有信息直接给出最终回答，不要再调用任何工具。",
				})
			}

			// yield: 继续推理（上层可以显示继续动画）。
			events <- QueryEvent{
				Type:      QueryEventContinue,
				Iteration: iter + 1,
			}
		}

		// ── 达到最大迭代次数，让 LLM 做最终总结 ──
		messages = append(messages, llm.ChatMessage{
			Role:    "user",
			Content: "你已经尝试了多次工具调用。请根据已有信息直接给出回答，不要再调用工具。",
		})
		finalResp, err := llmClient.ChatWithTools(ctx, messages, tools)
		if err != nil {
			events <- QueryEvent{
				Type:    QueryEventError,
				Content: fmt.Sprintf("reached max iterations (%d) without final answer", maxIter),
				Error:   err,
			}
			return
		}
		events <- QueryEvent{
			Type:      QueryEventFinal,
			Content:   finalResp.Content,
			Iteration: maxIter,
		}
	}()

	return events
}

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
