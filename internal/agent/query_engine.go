package agent

import (
	"context"
	"fmt"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/prompt"
)

// ──────────────────────────────────────────────────────────
// QueryEngine 层（上层协调）
// ──────────────────────────────────────────────────────────

// queryEngine 是 QueryEngine 层，负责与上层对接。
//
// 这是 Runner.Run() → runQueryAsync() 调用的核心方法。
// 职责：上下文构建 → 提示词组装 → 启动 queryLoop → 消费事件 → 返回结果。
//
// 执行流程（6 个步骤）：
//
//	步骤 1: 构建上下文 — 从三层记忆（L3 记忆 + L2 摘要 + 降级 L1）检索
//	步骤 2: 自动压缩检查 — 上下文 token 用量超过模型窗口 80% 时触发压缩
//	步骤 3: 构建 System/User Prompt — 组合 ReAct 提示词 + 工具描述
//	步骤 4: 启动 queryLoop — 获取 event channel，开始异步生成器
//	步骤 5: 消费事件 — 从 channel 实时读取事件并转发到 UI + EventStore
//	步骤 6: 返回最终结果 — 将 final answer 返回给 Runner
//
// 设计：
//   - 分离关注点：queryLoop 可独立测试和复用
//   - QueryEngine 负责所有上层逻辑（UI、事件、记忆）
//   - queryLoop 只负责核心循环逻辑（LLM 调用、工具执行、压缩）
//
// 参数：
//   - ctx: 上下文，用于取消和超时控制（/stop 通过 cancel 实现）
//   - round: 当前轮次号（从 1 开始）
//   - userInput: 用户输入的原始文本
//   - inputForward: 输入转发 channel（权限确认时从 Runner 转发用户输入到此）
//
// 返回：
//   - string: 最终回答文本（LLM 的完整回复）
//   - error: 错误信息
func (r *Runner) queryEngine(ctx context.Context, round int, userInput string, inputForward <-chan string) (string, error) {
	// ═══════════════════════════════════════════════════════
	// 步骤 1: 构建上下文（从三层记忆中检索）
	// ═══════════════════════════════════════════════════════
	// BuildContext 的检索顺序：
	//   1) L3 记忆索引（MEMORY.md）
	//   2) L3 高重要性记忆内容（importance >= 2，最多 10 条）
	//   3) L2 最近摘要（最近 10 条）
	//   4) L2 为空时降级 → EventStore 摘要 → HistoryStore 摘要
	contextDigest, err := r.retriever.BuildContext(userInput)
	if err != nil {
		// 降级：从事件日志生成简易摘要。
		contextDigest = r.events.Digest(10)
	}

	// ═══════════════════════════════════════════════════════
	// 步骤 2: 自动压缩检查
	// ═══════════════════════════════════════════════════════
	// 估算上下文 token 用量，接近模型窗口 80% 阈值时触发 L2 摘要压缩。
	// 压缩策略：LLM 合并旧摘要为一段综合摘要，保留最近 3 条不动。
	if contextDigest != "" {
		totalTokens := memory.EstimateTokens(contextDigest + userInput + r.tools.Descriptions())
		limit := r.llm.ContextLimit()
		compressed, compErr := r.retriever.CheckAndCompress(ctx, r.llm, limit, totalTokens)
		if compressed {
			if compErr != nil {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 自动压缩失败: %v", compErr))
			} else {
				r.ui.OnMessage("🗜️ 上下文接近上限，已自动压缩摘要")
				// 压缩后重新构建上下文（用新的压缩后摘要）。
				if newCtx, err := r.retriever.BuildContext(userInput); err == nil {
					contextDigest = newCtx
				}
			}
		}
	}

	// ═══════════════════════════════════════════════════════
	// 步骤 3: 构建 System/User Prompt
	// ═══════════════════════════════════════════════════════
	// System Prompt: ReAct 工作方式 + 可用工具列表 + 注意事项
	// User Prompt: 轮次号 + 记忆上下文 + 用户任务
	systemPrompt := prompt.BuildReActSystemPrompt(r.tools.Descriptions())
	userPrompt := prompt.BuildReActUserPrompt(round, contextDigest, userInput)

	// 初始化消息数组（作为 queryLoop 的初始输入）。
	messages := []llm.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// 获取 OpenAI function calling 格式的工具定义。
	tools := r.tools.FunctionDefinitions()

	// ═══════════════════════════════════════════════════════
	// 步骤 4: 启动 queryLoop（异步生成器模式）
	// ═══════════════════════════════════════════════════════
	// queryLoop 返回一个只读 channel，内部 goroutine 持续 yield 事件。
	// 上层通过 range channel 实时消费事件，无需轮询。
	contextLimit := r.llm.ContextLimit()
	eventChan := queryLoop(ctx, r.llm, messages, tools, r.tools, maxIterations, contextLimit)

	// ═══════════════════════════════════════════════════════
	// 步骤 5: 消费事件（实时转发到 UI + EventStore）
	// ═══════════════════════════════════════════════════════
	// 事件类型和对应的处理：
	//   - think      → UI.OnThink()      显示思考状态
	//   - delta      → UI.OnDelta()      流式输出增量文本
	//   - tool_call  → EventStore + UI   记录工具调用 + 显示框线
	//   - tool_result→ EventStore + UI   记录执行结果 + 显示结果框
	//   - permission → UI.ConfirmPermission()  阻塞等待用户确认
	//   - continue   → UI.OnContinue()   显示继续推理
	//   - final      → UI.OnFinal()      渲染最终回答（Markdown）
	//   - error      → UI.OnError()      显示错误并返回
	var finalAnswer string
	var finalIteration int

	for event := range eventChan {
		switch event.Type {
		case QueryEventThink:
			// LLM 开始新一轮思考。
			r.ui.OnThink(event.Iteration)

		case QueryEventDelta:
			// 流式增量文本（实时输出，不换行）。
			r.ui.OnDelta(event.Content)

		case QueryEventToolCall:
			// LLM 请求工具调用：先记录到事件日志（真相源），再通知 UI。
			toolCallEvents := make([]memory.ToolCallEvent, len(event.ToolCalls))
			for i, tc := range event.ToolCalls {
				toolCallEvents[i] = memory.ToolCallEvent{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			}
			r.events.Append(memory.Event{
				Type:      memory.EventToolUse,
				Round:     round,
				ToolCalls: toolCallEvents,
			})
			// 逐个通知 UI（每个工具调用绘制一个框线）。
			for _, tc := range event.ToolCalls {
				r.ui.OnToolCall(tc.Name, tc.Arguments)
			}

		case QueryEventToolResult:
			// 工具执行结果：记录到事件日志并通知 UI。
			r.events.Append(memory.Event{
				Type:       memory.EventToolResult,
				ToolCallID: "",
				ToolName:   event.ToolName,
				ToolResult: event.ToolResult,
				IsError:    event.IsError,
			})
			r.ui.OnToolResult(event.ToolName, event.ToolResult, event.IsError)

		case QueryEventPermission:
			// 权限确认：调用 UI 获取用户决策，结果写回 channel。
			// queryLoop 内部阻塞等待此 channel，实现同步确认。
			approved, _ := r.ui.ConfirmPermission(event.PermissionTool, event.PermissionArgs, inputForward)
			if event.PermissionCh != nil {
				event.PermissionCh <- approved
			}

		case QueryEventContinue:
			// 工具执行完毕，继续下一轮推理。
			r.ui.OnContinue(event.Iteration)

		case QueryEventFinal:
			// 最终回答：保存结果，通知 UI 渲染 Markdown。
			finalAnswer = event.Content
			finalIteration = event.Iteration
			r.ui.OnFinal(finalAnswer)

		case QueryEventError:
			// 错误：通知 UI 并返回错误信息。
			r.ui.OnError(event.Error)
			return "", fmt.Errorf("query loop error: %w", event.Error)
		}
	}

	// ═══════════════════════════════════════════════════════
	// 步骤 6: 返回最终结果
	// ═══════════════════════════════════════════════════════
	_ = finalIteration
	return finalAnswer, nil
}
