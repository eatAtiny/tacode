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
// 职责：
//   - 构建上下文（记忆检索、自动压缩）
//   - 构建系统提示和用户提示
//   - 从 queryLoop 的 channel 实时读取事件并显示
//   - 事件记录（工具调用、工具结果）
//   - 返回最终结果
//
// 设计：
//   - 分离关注点，queryLoop 可独立测试和复用
//   - QueryEngine 负责所有上层逻辑（UI、事件、记忆）
//   - queryLoop 只负责核心循环逻辑
//
// 参数：
//   - ctx: 上下文，用于取消和超时控制
//   - round: 当前轮次号（从 1 开始）
//   - userInput: 用户输入的原始文本
//
// 返回：
//   - string: 最终回答文本
//   - error: 错误信息
//
// 流程：
//  1. 构建上下文（从三层记忆中检索）
//  2. 自动压缩检查（接近 token 上限时压缩摘要）
//  3. 构建系统提示和用户提示
//  4. 调用 queryLoop 获取事件 channel
//  5. 从 channel 实时读取事件并处理
//  6. 返回最终回答
func (r *Runner) queryEngine(ctx context.Context, round int, userInput string) (string, error) {
	// ── 步骤 1: 构建上下文（从三层记忆中检索） ──
	contextDigest, err := r.retriever.BuildContext(userInput)
	if err != nil {
		// 降级：从事件日志生成简易摘要。
		contextDigest = r.events.Digest(10)
	}

	// ── 步骤 2: 自动压缩检查 ──
	// 估算上下文 token 用量，接近阈值时压缩 L2 摘要。
	if contextDigest != "" {
		totalTokens := memory.EstimateTokens(contextDigest + userInput + r.tools.Descriptions())
		limit := r.llm.ContextLimit()
		compressed, compErr := r.retriever.CheckAndCompress(ctx, r.llm, limit, totalTokens)
		if compressed {
			if compErr != nil {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 自动压缩失败: %v", compErr))
			} else {
				r.ui.OnMessage("🗜️ 上下文接近上限，已自动压缩摘要")
				// 压缩后重新构建上下文。
				if newCtx, err := r.retriever.BuildContext(userInput); err == nil {
					contextDigest = newCtx
				}
			}
		}
	}

	// ── 步骤 3: 构建系统提示和用户提示 ──
	systemPrompt := prompt.BuildReActSystemPrompt(r.tools.Descriptions())
	userPrompt := prompt.BuildReActUserPrompt(round, contextDigest, userInput)

	// 初始化消息数组。
	messages := []llm.ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}

	// 获取工具定义。
	tools := r.tools.FunctionDefinitions()

	// ── 步骤 4: 调用 queryLoop 获取事件 channel ──
	contextLimit := r.llm.ContextLimit()
	eventChan := queryLoop(ctx, r.llm, messages, tools, r.tools, maxIterations, contextLimit)

	// ── 步骤 5: 从 channel 实时读取事件并处理 ──
	var finalAnswer string
	var finalIteration int

	for event := range eventChan {
		switch event.Type {
		case QueryEventThink:
			r.ui.OnThink(event.Iteration)

		case QueryEventDelta:
			r.ui.OnDelta(event.Content)

		case QueryEventToolCall:
			// 工具调用请求，记录事件并通知 UI。
			toolCallEvents := make([]memory.ToolCallEvent, len(event.ToolCalls))
			for i, tc := range event.ToolCalls {
				toolCallEvents[i] = memory.ToolCallEvent{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments}
			}
			r.events.Append(memory.Event{
				Type:      memory.EventToolUse,
				Round:     round,
				ToolCalls: toolCallEvents,
			})
			// 通知 UI 显示工具调用。
			for _, tc := range event.ToolCalls {
				r.ui.OnToolCall(tc.Name, tc.Arguments)
			}

		case QueryEventToolResult:
			// 工具执行结果，记录事件并通知 UI。
			r.events.Append(memory.Event{
				Type:       memory.EventToolResult,
				ToolCallID: "",
				ToolName:   event.ToolName,
				ToolResult: event.ToolResult,
				IsError:    event.IsError,
			})
			r.ui.OnToolResult(event.ToolName, event.ToolResult, event.IsError)

		case QueryEventPermission:
			// 权限确认：调用 UI，结果写回 channel。
			approved, _ := r.ui.ConfirmPermission(event.PermissionTool, event.PermissionArgs)
			if event.PermissionCh != nil {
				event.PermissionCh <- approved
			}

		case QueryEventContinue:
			r.ui.OnContinue(event.Iteration)

		case QueryEventFinal:
			// 最终回答，保存结果并通知 UI。
			finalAnswer = event.Content
			finalIteration = event.Iteration
			r.ui.OnFinal(finalAnswer)

		case QueryEventError:
			// 错误，通知 UI 并返回错误信息。
			r.ui.OnError(event.Error)
			return "", fmt.Errorf("query loop error: %w", event.Error)
		}
	}

	// ── 步骤 6: 返回最终结果 ──
	_ = finalIteration
	return finalAnswer, nil
}
