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
	client *openai.Client
	model  string
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

	return &OpenAIClient{
		client: openai.NewClientWithConfig(config),
		model:  model,
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
