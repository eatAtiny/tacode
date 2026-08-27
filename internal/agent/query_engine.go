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
//	步骤 1: 组装消息 — 从累积对话（baseMessages）+ 记忆 preamble 组装
//	步骤 2: 自动压缩检查 — 上下文超过阈值时压缩旧消息
//	步骤 3: 构建 System/User Prompt — 组合 ReAct 提示词 + 工具描述
//	步骤 4: 启动 queryLoop — 获取 event channel，开始异步生成器
//	步骤 5: 消费事件 — 从 channel 实时读取事件并转发到 UI + EventStore
//	步骤 6: 返回最终结果 — 将 final answer + 完整消息数组返回给 Runner
//
// 设计：
//   - 分离关注点：queryLoop 可独立测试和复用
//   - QueryEngine 负责所有上层逻辑（UI、事件、记忆）
//   - queryLoop 只负责核心循环逻辑（LLM 调用、工具执行、压缩）
//   - messages 跨轮累积：baseMessages 是上一轮完成后的完整消息数组，
//     本轮在其基础上追加用户任务（跨轮累积架构的核心）
//
// 参数：
//   - ctx: 上下文，用于取消和超时控制（/stop 通过 cancel 实现）
//   - round: 当前轮次号（从 1 开始）
//   - userInput: 用户输入的原始文本
//   - inputForward: 输入转发 channel（权限确认时从 Runner 转发用户输入到此）
//   - baseMessages: 跨轮累积的对话消息（nil 时从记忆构建）
//
// 返回：
//   - string: 最终回答文本（LLM 的完整回复）
//   - []llm.ChatMessage: 查询结束后的完整消息数组（供 Runner 跨轮累积）
//   - error: 错误信息
func (r *Runner) queryEngine(ctx context.Context, round int, userInput string, inputForward <-chan string, baseMessages []llm.ChatMessage) (string, []llm.ChatMessage, error) {
	// ═══════════════════════════════════════════════════════
	// 步骤 1: 组装消息（跨轮累积 + 记忆 preamble）
	// ═══════════════════════════════════════════════════════
	// 消息结构（前缀缓存优化）：
	//   messages[0] = system（全静态，工具描述 + 指南，跨轮不变）
	//   messages[1] = preamble（<system-reminder> 记忆参考，会话内字节稳定）
	//   messages[2..] = 累积对话（baseMessages）
	//   messages[last] = 用户任务（轮次 + 输入，唯一每轮变化的消息）
	//
	// 静态内容前置、可变内容后置，最大化前缀缓存命中。

	// 系统消息：全静态（工具描述 + 使用指南，无 round/记忆/时间戳）。
	var guides []prompt.ToolGuide
	for _, name := range r.tools.Names() {
		t := r.tools.Get(name)
		guides = append(guides, prompt.ToolGuide{
			Name:  t.Name(),
			Guide: t.PromptGuide(),
		})
	}
	systemPrompt := prompt.BuildReActSystemPrompt(r.tools.Descriptions(), guides)

	// 记忆 preamble：仅当无累积对话（首次查询/会话切换后）时注入。
	// 缓存在 Runner 上，会话内字节稳定，前缀缓存可命中。
	var messages []llm.ChatMessage
	if len(baseMessages) == 0 && r.memoryPreamble == "" {
		if digest, err := r.retriever.BuildContextFallback(userInput); err == nil && digest != "" {
			r.memoryPreamble = digest
		}
	}

	messages = append(messages, llm.ChatMessage{Role: "system", Content: systemPrompt})
	if r.memoryPreamble != "" {
		messages = append(messages, llm.ChatMessage{
			Role:    "user",
			Content: prompt.BuildSystemReminder(r.memoryPreamble),
		})
	}
	// 累积对话（不含 system/preamble，仅累积对话本身）。
	messages = append(messages, baseMessages...)
	// 用户任务：每轮唯一变化的消息，放末尾。
	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: prompt.BuildUserTask(round, userInput),
	})

	// ═══════════════════════════════════════════════════════
	// 步骤 2: 自动压缩检查（s08 四步管线）
	// ═══════════════════════════════════════════════════════
	// 每轮组装完成后运行 compactor.Prepare：
	//   大结果转存 → 消息数归档 → 微压缩 → 历史摘要（逐步升级，只有超限才进入有损步骤）。
	// queryLoop 内每轮迭代前也会运行（reactive_compact 兜底 API 拒绝）。
	if r.compactor != nil {
		messages = r.compactor.Prepare(messages, userInput)
	}

	// ═══════════════════════════════════════════════════════
	// 步骤 3: 构建 System/User Prompt（已并入步骤 1 组装）
	// ═══════════════════════════════════════════════════════

	// 获取 OpenAI function calling 格式的工具定义。
	tools := r.tools.FunctionDefinitions()

	// ═══════════════════════════════════════════════════════
	// 步骤 4: 启动 queryLoop（异步生成器模式）
	// ═══════════════════════════════════════════════════════
	// queryLoop 返回一个只读 channel，内部 goroutine 持续 yield 事件。
	// 上层通过 range channel 实时消费事件，无需轮询。
	eventChan := queryLoop(ctx, r.llm, messages, tools, r.tools, r.maxIter(), queryLoopOptions{
		ResultLimit:   r.resultLimit(),
		InputForward:  inputForward,
		ActiveRequest: userInput,
		Compactor:     r.compactor,
	})

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
	var finalMessages []llm.ChatMessage // 查询结束后的完整消息数组（跨轮累积）

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
				ToolName:   event.ToolName,
				ToolResult: event.ToolResult,
				IsError:    event.IsError,
			})
			r.ui.OnToolResult(event.ToolName, event.ToolResult, event.IsError)

		case QueryEventPermission:
			// 权限确认：调用 UI 获取用户决策，结果写回 channel。
			// queryLoop 内部阻塞等待此 channel，实现同步确认。
			// 设置 permWaiting：主循环据此把输入转发给 ConfirmPermission
			// （而非排队），确认完成后清除。
			r.permWaiting.Store(true)
			approved, _ := r.ui.ConfirmPermission(event.PermissionTool, event.PermissionArgs, event.PermissionReason, inputForward)
			r.permWaiting.Store(false)
			if event.PermissionCh != nil {
				event.PermissionCh <- approved
			}

		case QueryEventContinue:
			// 工具执行完毕，继续下一轮推理。
			r.ui.OnContinue(event.Iteration)

		case QueryEventFinal:
			// 最终回答：保存结果，通知 UI 渲染 Markdown + 本轮 token 统计。
			finalAnswer = event.Content
			finalMessages = event.Messages

			// OnFinal 的 token 行直接读取 Final 事件携带的精确累计值
			// （queryLoop 内部多次 LLM 调用已累加，Final 是最终值）。
			r.ui.OnFinal(finalAnswer, event.InputTokens, event.OutputTokens, event.TotalTokens)

			// 上下文占用更新（footer 状态栏常驻显示）。
			// used = 当前消息数组估算字符数，limit = 压缩触发的字符上限
			// （与 Compactor 的 context_char_limit 同一口径，避免 token/字符口径不一致）。
			if r.compactor != nil {
				r.ui.UpdateContext(r.compactor.EstimateMessagesChars(finalMessages), r.compactor.Limit())
			}

			// 每轮结束更新余额（每轮自动查询，失败静默；连续失败达到阈值时提示一次）。
			go r.queryBalanceWith(r.queryBalance)

		case QueryEventError:
			// 错误处理：区分「主动取消」与「真实错误」。
			// /stop（或 /interrupt）主动取消时 ctx 已取消，queryLoop 的流式
			// 读取会因连接中断返回错误（receive stream failed 等）——
			// 这是取消的预期副作用，静默处理（不显示错误、不返回 error），
			// 由 Runner 的 /stop 分支已给出「⏹️ 已停止」反馈。
			if ctx.Err() != nil {
				return "", nil, nil
			}
			// 真实错误：通知 UI 并返回错误信息。
			r.ui.OnError(event.Error)
			return "", nil, fmt.Errorf("query loop error: %w", event.Error)
		}
	}

	// ═══════════════════════════════════════════════════════
	// 步骤 6: 返回最终结果
	// ═══════════════════════════════════════════════════════
	return finalAnswer, finalMessages, nil
}
