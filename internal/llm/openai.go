package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"agentic/internal/config"

	openai "github.com/sashabaranov/go-openai"
)

// 默认模型，可通过 OPENAI_MODEL 覆盖。
const defaultModel = openai.GPT4oMini

// defaultTemperature 默认采样温度（可被 config 覆盖）。
const defaultTemperature = 0.2

// unknownModelContextLimit 未识别模型的保守上下文窗口默认值（32k）。
// 保守值避免误判大窗口导致压缩过晚（128k 乐观假设的风险）。
const unknownModelContextLimit = 32_000

// ──────────────────────────────────────────────────────────
// OpenAIClient — OpenAI API 封装
//
// 调用链中的角色：
//   queryLoop.callLLMStream()
//     → llmClient.ChatWithToolsStream(ctx, messages, tools)
//       → 返回 <-chan StreamEvent（流式事件 channel）
//       → 内部 goroutine 读取 OpenAI 流式响应并 yield 事件
//
//   Extractor.Extract() / Retriever.CompressSummaries() / Compactor 压缩摘要
//     → llmClient.Chat(ctx, systemPrompt, userPrompt)
//       → 返回完整文本（非流式，用于记忆提取/摘要压缩）
// ──────────────────────────────────────────────────────────

// OpenAIClient 对 go-openai 做一层轻量封装。
type OpenAIClient struct {
	client       *openai.Client // go-openai 原始客户端
	model        string         // 模型名称（如 gpt-4o-mini）
	apiKey       string         // API key（余额查询复用）
	baseURL      string         // API Base URL（余额查询 host 推导）
	contextLimit int            // 模型上下文窗口大小（token 数）。预留：当前无消费方（压缩判断已改用 compactor 字符口径）
	temperature  float64        // 采样温度，默认 0.2（可由 config 覆盖）
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
	// 去掉尾部斜杠后同时用于 API 网关与余额查询 host 推导
	// （空表示未配置自定义网关，余额查询走官方地址）。
	config := openai.DefaultConfig(apiKey)
	baseURL := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL"))
	if baseURL != "" {
		baseURL = strings.TrimRight(baseURL, "/")
		config.BaseURL = baseURL
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
		apiKey:       apiKey,
		baseURL:      baseURL,
		contextLimit: contextLimit,
		temperature:  defaultTemperature,
	}, nil
}

// ──────────────────────────────────────────────────────────
// 非流式对话（用于记忆提取等简单场景）
// ──────────────────────────────────────────────────────────

// Chat 执行一次最小对话请求（system + user），返回完整文本。
// temperature 默认 0.2（可由 config 覆盖）。用于 Extractor/Retriever/Compactor 的 LLM 调用。
func (c *OpenAIClient) Chat(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	// 带重试的对话请求：429/5xx/网络错误自动重试（最多 defaultMaxRetries 次）。
	resp, err := withRetry(ctx, defaultMaxRetries, func() (openai.ChatCompletionResponse, error) {
		return c.client.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
			Model: c.model,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: userPrompt},
			},
			Temperature: float32(c.temperature),
		})
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
	Role       string     `json:"role"`                   // "system" | "user" | "assistant" | "tool"
	Content    string     `json:"content"`                // 消息文本内容
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool 消息对应哪个 tool_call（仅 Role="tool" 时使用）
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // assistant 消息关联的工具调用列表
}

// ToolCall 表示 LLM 请求调用一个工具。
type ToolCall struct {
	ID        string `json:"id"`        // OpenAI 生成的 tool_call ID（用于关联 tool 消息）
	Name      string `json:"name"`      // 工具名称（如 "shell"、"file"）
	Arguments string `json:"arguments"` // JSON 格式的调用参数
}

// StreamEventType 流式事件类型。
type StreamEventType string

const (
	StreamEventDelta StreamEventType = "delta" // 增量文本（实时输出）
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

// Model 返回当前使用的模型名称。
func (c *OpenAIClient) Model() string {
	return c.model
}

// APIKey 返回 API key（余额查询等场景复用）。
func (c *OpenAIClient) APIKey() string { return c.apiKey }

// BaseURL 返回 API Base URL（余额查询 host 推导用）。
func (c *OpenAIClient) BaseURL() string { return c.baseURL }

// ChatWithToolsStream 支持 Function Calling 的流式对话。
//
// 这是 queryLoop 调用的核心接口：实时通过 channel 推送增量事件，
// 不等待完整响应。
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
			Temperature: float32(c.temperature),
			Stream:      true, // 启用流式
			// 流式响应默认不返回 usage 字段（token 统计恒 0）。
			// include_usage=true 让最后一个 chunk 携带完整 token 统计，
			// 供 queryLoop 累计输入/输出 token 与精确压缩判断。
			StreamOptions: &openai.StreamOptions{IncludeUsage: true},
		}
		if len(tools) > 0 {
			req.Tools = tools
		}

		// ── 步骤 3: 创建流式连接（带重试） ──
		// 只重试 stream 创建前；流建立后的 Recv 中断不重试（避免重复 tool_calls 副作用）。
		stream, err := withRetry(ctx, defaultMaxRetries, func() (*openai.ChatCompletionStream, error) {
			return c.client.CreateChatCompletionStream(ctx, req)
		})
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
// 预留接口：当前无消费方（压缩判断已改用 compactor 字符口径）。
func (c *OpenAIClient) ContextLimit() int {
	return c.contextLimit
}

// SetConfig 应用 config 中的 LLM 相关设置。
// 仅覆盖显式设置的字段；nil / 零值保持当前值不变。
func (c *OpenAIClient) SetConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	if cfg.ContextLimit != nil {
		c.contextLimit = *cfg.ContextLimit
	}
	if cfg.Temperature != nil {
		c.temperature = *cfg.Temperature
	}
}

// inferContextLimit 根据模型名称推断上下文窗口大小。
//
// 支持 OPENAI_CONTEXT_LIMIT 环境变量覆盖（优先级最高）。
// 未识别的模型使用保守默认值（32k）——乐观假设大窗口会导致压缩过晚，
// 保守值牺牲一点容量换取安全（配合 config 的 context_limit 可精确覆盖）。
//
// 支持的模型家族（子串匹配，具体型号由同族分支覆盖）：
//   - MiMo (小米): mimo-v2.5* / mimo-v2-pro → 1M, 其余 mimo → 256k
//   - OpenAI: gpt-4o* → 128k, gpt-4-turbo → 128k, gpt-4-32k → 32k,
//     gpt-4 → 8k, gpt-3.5-turbo* → 16k
//   - Anthropic: claude* → 200k
//   - DeepSeek: deepseek-v4-flash → 1M, 其余 deepseek → 128k
func inferContextLimit(model string) int {
	// 环境变量覆盖优先。
	if v := strings.TrimSpace(os.Getenv("OPENAI_CONTEXT_LIMIT")); v != "" {
		var limit int
		if _, err := fmt.Sscanf(v, "%d", &limit); err == nil && limit > 0 {
			return limit
		}
	}

	model = strings.ToLower(model)

	// MiMo 模型（小米）。注意 mimo-v2-pro 不含 "mimo-v2.5" 子串，需单列。
	switch {
	case strings.Contains(model, "mimo-v2-pro"):
		return 1_000_000 // 1M
	case strings.Contains(model, "mimo-v2.5"):
		return 1_000_000 // 1M
	case strings.Contains(model, "mimo"):
		return 256_000

	// OpenAI 模型。
	case strings.Contains(model, "gpt-4o"):
		return 128_000
	case strings.Contains(model, "gpt-4-turbo"):
		return 128_000
	case strings.Contains(model, "gpt-4-32k"):
		return 32_000
	case strings.Contains(model, "gpt-4"):
		return 8_192
	case strings.Contains(model, "gpt-3.5-turbo"):
		return 16_384

	// Anthropic 模型。
	case strings.Contains(model, "claude"):
		return 200_000

	// DeepSeek 模型。
	case strings.Contains(model, "deepseek-v4-flash"):
		return 1_000_000 // v4-flash 上下文窗口 1M
	case strings.Contains(model, "deepseek"):
		return 128_000

	default:
		return unknownModelContextLimit
	}
}
