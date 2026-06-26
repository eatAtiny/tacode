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

// OpenAIClient 对 go-openai 做一层轻量封装。
type OpenAIClient struct {
	client       *openai.Client
	model        string
	contextLimit int // 模型上下文窗口大小（token）
}

// NewOpenAIClientFromEnv 从环境变量初始化客户端。
// 必需：OPENAI_API_KEY
// 可选：OPENAI_BASE_URL, OPENAI_MODEL
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

	// 推断上下文窗口大小。
	contextLimit := inferContextLimit(model)

	return &OpenAIClient{
		client:       openai.NewClientWithConfig(config),
		model:        model,
		contextLimit: contextLimit,
	}, nil
}

// Chat 执行一次最小对话请求（system + user）。
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

// ChatMessage 表示一条对话消息，用于 ReAct 多轮交互。
type ChatMessage struct {
	Role       string // "system", "user", "assistant", "tool"
	Content    string
	ToolCallID string // tool 消息对应哪个 tool_call
	ToolCalls  []ToolCall
}

// ToolCall 表示 LLM 请求调用一个工具。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

// ChatResponse 表示 LLM 的响应。
type ChatResponse struct {
	Content   string     // 文本内容（最终回答时非空）
	ToolCalls []ToolCall // 工具调用请求（需要执行工具时非空）
	Finish    bool       // true 表示不需要再调用工具，本轮结束
}

// StreamEventType 流式事件类型。
type StreamEventType string

const (
	StreamEventDelta StreamEventType = "delta" // 增量文本
	StreamEventTool  StreamEventType = "tool"  // 工具调用
	StreamEventDone  StreamEventType = "done"  // 完成
	StreamEventError StreamEventType = "error" // 错误
)

// StreamEvent 表示流式响应的事件。
type StreamEvent struct {
	Type      StreamEventType // 事件类型
	Content   string          // 增量文本（delta 类型）
	ToolCalls []ToolCall      // 工具调用（tool 类型）
	Error     error           // 错误信息（error 类型）

	// Token 追踪
	InputTokens  int // 输入 token 数
	OutputTokens int // 输出 token 数
}

// ChatWithTools 支持 Function Calling 的多轮对话。
// messages: 完整的消息历史；tools: 工具定义列表。
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
		Model:      c.model,
		Messages:   msgs,
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
// messages: 完整的消息历史；tools: 工具定义列表。
// 返回一个只读 channel，上层可以实时读取流式事件。
//
// 事件类型：
//   - delta: 增量文本
//   - tool: 工具调用请求
//   - done: 完成
//   - error: 错误
//
// 使用示例：
//
//	streamChan := client.ChatWithToolsStream(ctx, messages, tools)
//	for event := range streamChan {
//	    switch event.Type {
//	    case StreamEventDelta:
//	        fmt.Print(event.Content)
//	    case StreamEventTool:
//	        fmt.Printf("工具调用: %v\n", event.ToolCalls)
//	    case StreamEventDone:
//	        fmt.Println("完成")
//	    case StreamEventError:
//	        fmt.Printf("错误: %v\n", event.Error)
//	    }
//	}
func (c *OpenAIClient) ChatWithToolsStream(ctx context.Context, messages []ChatMessage, tools []openai.Tool) <-chan StreamEvent {
	events := make(chan StreamEvent)

	go func() {
		defer close(events)

		// 转换消息格式。
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

		// 创建流式请求。
		req := openai.ChatCompletionRequest{
			Model:      c.model,
			Messages:   msgs,
			Temperature: 0.2,
			Stream:      true, // 启用流式
		}
		if len(tools) > 0 {
			req.Tools = tools
		}

		// 创建流式响应。
		stream, err := c.client.CreateChatCompletionStream(ctx, req)
		if err != nil {
			events <- StreamEvent{
				Type:  StreamEventError,
				Error: fmt.Errorf("create stream failed: %w", err),
			}
			return
		}
		defer stream.Close()

		// 读取流式响应。
		var fullContent string
		var toolCalls []ToolCall
		var inputTokens, outputTokens int
		var finishReason string

		for {
			response, err := stream.Recv()
			if err != nil {
				if err.Error() == "EOF" {
					// 流结束。
					break
				}
				events <- StreamEvent{
					Type:  StreamEventError,
					Error: fmt.Errorf("receive stream failed: %w", err),
				}
				return
			}

			// 处理 usage 信息。
			if response.Usage != nil {
				inputTokens = response.Usage.PromptTokens
				outputTokens = response.Usage.CompletionTokens
			}

			// 处理增量内容。
			if len(response.Choices) > 0 {
				choice := response.Choices[0]

				// 记录完成原因。
				if choice.FinishReason != "" {
					finishReason = string(choice.FinishReason)
				}

				// 增量文本。
				if choice.Delta.Content != "" {
					fullContent += choice.Delta.Content
					events <- StreamEvent{
						Type:         StreamEventDelta,
						Content:      choice.Delta.Content,
						InputTokens:  inputTokens,
						OutputTokens: outputTokens,
					}
				}

				// 工具调用（增量累加）。
				if len(choice.Delta.ToolCalls) > 0 {
					for _, tc := range choice.Delta.ToolCalls {
						// 使用索引来查找或创建工具调用。
						// OpenAI 流式响应中，后续分片的 ID 可能为空，但 Index 是正确的。
						idx := 0
						if tc.Index != nil {
							idx = *tc.Index
						}
						if idx >= len(toolCalls) {
							// 扩展切片。
							for len(toolCalls) <= idx {
								toolCalls = append(toolCalls, ToolCall{})
							}
						}

						// 更新工具调用。
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

		// 根据完成原因发送最终事件。
		if finishReason == "tool_calls" && len(toolCalls) > 0 {
			// 工具调用完成。
			events <- StreamEvent{
				Type:         StreamEventDone,
				ToolCalls:    toolCalls,
				InputTokens:  inputTokens,
				OutputTokens: outputTokens,
			}
		} else {
			// 普通文本完成。
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
func (c *OpenAIClient) ContextLimit() int {
	return c.contextLimit
}

// inferContextLimit 根据模型名称推断上下文窗口大小。
// 支持通过 OPENAI_CONTEXT_LIMIT 环境变量覆盖。
// 未识别的模型默认使用 128k。
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
