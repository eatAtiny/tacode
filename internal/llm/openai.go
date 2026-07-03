package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	openai "github.com/sashabaranov/go-openai"
)

// 默认模型，可通过 OPENAI_MODEL 覆盖。
const defaultModel = openai.GPT4oMini

// ──────────────────────────────────────────────────────────
// OpenAIClient — OpenAI API 封装
//
// 调用链中的角色：
//   queryLoop.callLLMStream()
//     → llmClient.ChatWithToolsStream(ctx, messages, tools)
//       → 返回 <-chan StreamEvent（流式事件 channel）
//       → 内部 goroutine 读取 OpenAI 流式响应并 yield 事件
//
//   Extractor.Extract()
//     → llmClient.Chat(ctx, systemPrompt, userPrompt)
//       → 返回完整文本（非流式，用于记忆提取）
//
//   Retriever.CompressSummaries()
//     → llmClient.Chat(ctx, systemPrompt, userPrompt)
//       → 返回压缩后的摘要文本
// ──────────────────────────────────────────────────────────

// OpenAIClient 对 go-openai 做一层轻量封装。
type OpenAIClient struct {
	client       *openai.Client // go-openai 原始客户端
	model        string         // 模型名称（如 gpt-4o-mini）
	contextLimit int            // 模型上下文窗口大小（token 数），用于压缩判断
}

// NewOpenAIClientFromEnv 从环境变量初始化客户端。
// 必需：OPENAI_API_KEY
// 可选：OPENAI_BASE_URL（自定义网关）、OPENAI_MODEL（模型名）、OPENAI_CONTEXT_LIMIT（上下文窗口）
func NewOpenAIClientFromEnv() (*OpenAIClient, error) {
	apiKey := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}

	// 默认走官方地址；如果配置了代理地址则替换。
	config := openai.DefaultConfig(apiKey)
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL != "" {
		config.BaseURL = strings.TrimRight(baseURL, "/")
	}

	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = defaultModel
	}

	// 根据模型名称推断上下文窗口大小（支持 OPENAI_CONTEXT_LIMIT 覆盖）。
	contextLimit := inferContextLimit(model)

	return &OpenAIClient{
		client:       openai.NewClientWithConfig(config),
		model:        model,
		contextLimit: contextLimit,
	}, nil
}

// ──────────────────────────────────────────────────────────
// 非流式对话（用于记忆提取等简单场景）
// ──────────────────────────────────────────────────────────

// Chat 执行一次最小对话请求（system + user），返回完整文本。
// temperature 固定 0.2。用于 Extractor 和 Retriever 的 LLM 调用。
func (c *OpenAIClient) Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	resp, err := c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model: c.model,
		Messages: []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
			{Role: openai.ChatMessageRoleUser, Content: userPrompt},
		},
		Temperature: 0.2,
	})
	if err != nil {
		return "", fmt.Errorf("openai chat completion failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("empty choices")
	}

	content := strings.TrimSpace(resp.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("empty content")
	}
	return content, nil
}

// ──────────────────────────────────────────────────────────
// 类型定义
// ──────────────────────────────────────────────────────────

// ChatMessage 表示一条对话消息，用于 ReAct 多轮交互。
type ChatMessage struct {
	Role       string     // "system" | "user" | "assistant" | "tool"
	Content    string     // 消息文本内容
	ToolCallID string     // tool 消息对应哪个 tool_call（仅 Role="tool" 时使用）
	ToolCalls  []ToolCall // assistant 消息关联的工具调用列表
}

// ToolCall 表示 LLM 请求调用一个工具。
type ToolCall struct {
	ID        string // OpenAI 生成的 tool_call ID（用于关联 tool 消息）
	Name      string // 工具名称（如 "shell"、"file"）
	Arguments string // JSON 格式的调用参数
}

// ChatResponse 表示 LLM 的非流式响应（ChatWithTools 返回）。
type ChatResponse struct {
	Content   string     // 文本内容（最终回答时非空）
	ToolCalls []ToolCall // 工具调用请求（需要执行工具时非空）
	Finish    bool       // true 表示不需要再调用工具，本轮结束
}

// StreamEventType 流式事件类型。
type StreamEventType string

const (
	StreamEventDelta StreamEventType = "delta" // 增量文本（实时输出）
	StreamEventTool  StreamEventType = "tool"  // 工具调用（预留）
	StreamEventDone  StreamEventType = "done"  // 流式完成（含完整 toolCalls 和 token 统计）
	StreamEventError StreamEventType = "error" // 流式错误
)

// StreamEvent 表示流式响应的事件。
type StreamEvent struct {
	Type      StreamEventType // 事件类型
	Content   string          // 增量文本（delta 类型）
	ToolCalls []ToolCall      // 完整的工具调用列表（done 类型，tool_calls finish reason）
	Error     error           // 错误信息（error 类型）

	// Token 追踪（来自 OpenAI usage 字段）
	InputTokens  int // 输入 token 数
	OutputTokens int // 输出 token 数
}

// ──────────────────────────────────────────────────────────
// 流式对话（核心接口，queryLoop 使用）
// ──────────────────────────────────────────────────────────

// ChatWithTools 支持 Function Calling 的多轮对话（非流式，已较少使用）。
//
// messages: 完整的消息历史（system + user + assistant + tool）
// tools: 工具定义列表（OpenAI function calling 格式）
func (c *OpenAIClient) ChatWithTools(ctx context.Context, messages []ChatMessage, tools []openai.Tool) (*ChatResponse, error) {
	msgs := make([]openai.ChatCompletionMessage, len(messages))
	for i, m := range messages {
		msgs[i] = openai.ChatCompletionMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		if len(m.ToolCalls) > 0 {
			msgs[i].ToolCalls = make([]openai.ToolCall, len(m.ToolCalls))
			for j, tc := range m.ToolCalls {
				msgs[i].ToolCalls[j] = openai.ToolCall{
					ID:   tc.ID,
					Type: openai.ToolTypeFunction,
					Function: openai.FunctionCall{
						Name:      tc.Name,
						Arguments: tc.Arguments,
					},
				}
			}
		}
	}

	req := openai.ChatCompletionRequest{
		Model:       c.model,
		Messages:    msgs,
		Temperature: 0.2,
	}
	if len(tools) > 0 {
		req.Tools = tools
	}

	resp, err := c.client.CreateChatCompletion(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("openai chat completion failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("empty choices")
	}

	choice := resp.Choices[0]
	result := &ChatResponse{
		Content: strings.TrimSpace(choice.Message.Content),
	}

	if len(choice.Message.ToolCalls) > 0 {
		result.ToolCalls = make([]ToolCall, len(choice.Message.ToolCalls))
		for i, tc := range choice.Message.ToolCalls {
			result.ToolCalls[i] = ToolCall{
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			}
		}
		result.Finish = false
	} else {
		result.Finish = true
	}

	return result, nil
}

// Model 返回当前使用的模型名称。
func (c *OpenAIClient) Model() string {
	return c.model
}

// ChatWithToolsStream 支持 Function Calling 的流式对话。
//
// 这是 queryLoop 调用的核心接口。与 ChatWithTools 的区别：
//   - ChatWithTools：等待完整响应后一次性返回
//   - ChatWithToolsStream：实时通过 channel 推送增量事件（流式）
//
// 调用链：
//
//	queryLoop.callLLMStream()
//	  → llmClient.ChatWithToolsStream(ctx, messages, tools)
//	    → 返回 <-chan StreamEvent
//	    → 内部 goroutine:
//	        1. 转换消息格式（ChatMessage → OpenAI ChatCompletionMessage）
//	        2. 创建流式请求（Stream: true）
//	        3. 循环读取流式响应：
//	           - Delta.Content → yield StreamEventDelta（增量文本）
//	           - Delta.ToolCalls → 累加组装 toolCalls（流式分片到达）
//	           - Usage → 提取 token 统计
//	        4. 流结束（EOF）：
//	           - finishReason == "tool_calls" → yield StreamEventDone with ToolCalls
//	           - 其他 → yield StreamEventDone with Content
//	        5. 关闭 channel
//
// 事件类型：
//   - delta: 增量文本（每收到一个 token 就推送一次）
//   - done: 流式完成（含 toolCalls 或 finalContent + token 统计）
//   - error: 流式错误（创建流失败、接收中断等）
func (c *OpenAIClient) ChatWithToolsStream(ctx context.Context, messages []ChatMessage, tools []openai.Tool) <-chan StreamEvent {
	events := make(chan StreamEvent)

	go func() {
		defer close(events)

		// ── 步骤 1: 转换消息格式 ──
		msgs := make([]openai.ChatCompletionMessage, len(messages))
		for i, m := range messages {
			msgs[i] = openai.ChatCompletionMessage{
				Role:       m.Role,
				Content:    m.Content,
				ToolCallID: m.ToolCallID,
			}
			if len(m.ToolCalls) > 0 {
				msgs[i].ToolCalls = make([]openai.ToolCall, len(m.ToolCalls))
				for j, tc := range m.ToolCalls {
					msgs[i].ToolCalls[j] = openai.ToolCall{
						ID:   tc.ID,
						Type: openai.ToolTypeFunction,
						Function: openai.FunctionCall{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					}
				}
			}
		}

		// ── 步骤 2: 创建流式请求 ──
		req := openai.ChatCompletionRequest{
			Model:       c.model,
			Messages:    msgs,
			Temperature: 0.2,
			Stream:      true, // 启用流式
		}
		if len(tools) > 0 {
			req.Tools = tools
		}

		// ── 步骤 3: 创建流式连接 ──
		stream, err := c.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			events <- StreamEvent{
				Type:  StreamEventError,
				Error: fmt.Errorf("create stream failed: %w", err),
			}
			return
		}
		defer stream.Close()

		// ── 步骤 4: 逐条读取流式响应 ──
		var fullContent string
		var toolCalls []ToolCall
		var inputTokens, outputTokens int
		var finishReason string

		for {
			response, err := stream.Recv()
			if err != nil {
				if err.Error() == "EOF" {
					// 流正常结束。
					break
				}
				events <- StreamEvent{
					Type:  StreamEventError,
					Error: fmt.Errorf("receive stream failed: %w", err),
				}
				return
			}

			// ── 提取 usage 信息（token 统计） ──
			if response.Usage != nil {
				inputTokens = response.Usage.PromptTokens
				outputTokens = response.Usage.CompletionTokens
			}

			// ── 处理增量内容 ──
			if len(response.Choices) > 0 {
				choice := response.Choices[0]

				// 记录完成原因（"stop" 或 "tool_calls"）。
				if choice.FinishReason != "" {
					finishReason = string(choice.FinishReason)
				}

				// ── 增量文本 → yield Delta 事件 ──
				if choice.Delta.Content != "" {
					fullContent += choice.Delta.Content
					events <- StreamEvent{
						Type:         StreamEventDelta,
						Content:      choice.Delta.Content,
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
					}
				}

				// ── 工具调用 → 增量累加组装 ──
				// OpenAI 流式返回 tool_calls 时是分片到达的：
				//   第 1 片: {Index: 0, ID: "call_xxx", Function.Name: "shell"}
				//   第 2 片: {Index: 0, Function.Arguments: "{\"com"}
				//   第 3 片: {Index: 0, Function.Arguments: "mand\":\"ls\"}"}
				// 需要按 Index 累加 Arguments 字符串。
				if len(choice.Delta.ToolCalls) > 0 {
					for _, tc := range choice.Delta.ToolCalls {
						// 使用 Index 来查找或创建工具调用槽位。
						idx := 0
						if tc.Index != nil {
							idx = *tc.Index
						}
						if idx >= len(toolCalls) {
							// 扩展切片到足够的槽位。
							for len(toolCalls) <= idx {
								toolCalls = append(toolCalls, ToolCall{})
							}
						}

						// 更新工具调用字段（按分片累加）。
						if tc.ID != "" {
							toolCalls[idx].ID = tc.ID
						}
						if tc.Function.Name != "" {
							toolCalls[idx].Name = tc.Function.Name
						}
						if tc.Function.Arguments != "" {
							toolCalls[idx].Arguments += tc.Function.Arguments
						}
					}
				}
			}
		}

		// ── 步骤 5: 发送最终事件 ──
		if finishReason == "tool_calls" && len(toolCalls) > 0 {
			// 流结束原因是 tool_calls → LLM 请求调用工具。
			events <- StreamEvent{
				Type:         StreamEventDone,
				ToolCalls:    toolCalls,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}
		} else {
			// 流结束原因是 stop → LLM 直接给出文本回答。
			events <- StreamEvent{
				Type:         StreamEventDone,
				Content:      fullContent,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}
		}
	}()

	return events
}

// ContextLimit 返回模型的上下文窗口大小（token 数）。
// 用于 queryLoop 的上下文压缩判断（80% 阈值）。
func (c *OpenAIClient) ContextLimit() int {
	return c.contextLimit
}

// inferContextLimit 根据模型名称推断上下文窗口大小。
//
// 支持 OPENAI_CONTEXT_LIMIT 环境变量覆盖（优先级最高）。
// 未识别的模型默认使用 128k。
//
// 支持的模型家族：
//   - MiMo (小米): mimo-v2.5-pro → 1M, mimo-v2-omni → 256k
//   - OpenAI: gpt-4o-mini → 128k, gpt-4-turbo → 128k, gpt-4 → 8k
//   - Anthropic: claude-sonnet-4 → 200k, claude-opus-4 → 200k
func inferContextLimit(model string) int {
	// 环境变量覆盖优先。
	if v := strings.TrimSpace(os.Getenv("OPENAI_CONTEXT_LIMIT")); v != "" {
		var limit int
		if _, err := fmt.Sscanf(v, "%d", &limit); err == nil && limit > 0 {
			return limit
		}
	}

	model = strings.ToLower(model)

	// MiMo 模型（小米）。
	switch {
	case strings.Contains(model, "mimo-v2.5-pro"), strings.Contains(model, "mimo-v2-pro"):
		return 1_000_000 // 1M
	case strings.Contains(model, "mimo-v2.5"):
		return 1_000_000 // 1M
	case strings.Contains(model, "mimo-v2-omni"):
		return 256_000
	case strings.Contains(model, "mimo-v2-flash"):
		return 256_000
	case strings.Contains(model, "mimo"):
		return 256_000

	// OpenAI 模型。
	case strings.Contains(model, "gpt-4o-mini"):
		return 128_000
	case strings.Contains(model, "gpt-4o"):
		return 128_000
	case strings.Contains(model, "gpt-4-turbo"):
		return 128_000
	case strings.Contains(model, "gpt-4-32k"):
		return 32_000
	case strings.Contains(model, "gpt-4"):
		return 8_192
	case strings.Contains(model, "gpt-3.5-turbo-16k"):
		return 16_384
	case strings.Contains(model, "gpt-3.5-turbo"):
		return 16_384

	// Anthropic 模型。
	case strings.Contains(model, "claude-3.5-sonnet"), strings.Contains(model, "claude-sonnet-4"):
		return 200_000
	case strings.Contains(model, "claude-3-opus"), strings.Contains(model, "claude-opus-4"):
		return 200_000
	case strings.Contains(model, "claude-3-haiku"), strings.Contains(model, "claude-haiku-4"):
		return 200_000
	case strings.Contains(model, "claude"):
		return 200_000

	default:
		return 128_000
	}
}
