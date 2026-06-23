package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agentic/internal/llm"
)

// Extractor 使用 LLM 从对话中提取摘要和结构化记忆。
type Extractor struct {
	llm *llm.OpenAIClient
}

// NewExtractor 构造 Extractor。
func NewExtractor(client *llm.OpenAIClient) *Extractor {
	return &Extractor{llm: client}
}

// Extract 一次 LLM 调用，同时提取对话摘要和记忆操作。
func (e *Extractor) Extract(ctx context.Context, userInput, assistantOutput string) (*ExtractionResult, error) {
	systemPrompt := buildExtractPrompt()
	userPrompt := fmt.Sprintf("用户输入:\n%s\n\n助手回复:\n%s", userInput, assistantOutput)

	result, err := e.llm.Chat(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("extract llm call failed: %w", err)
	}

	// 尝试解析 JSON。
	var extraction ExtractionResult
	cleaned := cleanJSONResponse(result)
	if err := json.Unmarshal([]byte(cleaned), &extraction); err != nil {
		// 解析失败，降级为只生成摘要。
		return &ExtractionResult{
			Summary:  truncateForSummary(userInput, assistantOutput),
			Memories: nil,
		}, nil
	}

	// 确保 summary 不为空。
	if extraction.Summary == "" {
		extraction.Summary = truncateForSummary(userInput, assistantOutput)
	}

	return &extraction, nil
}

// buildExtractPrompt 构建记忆提取的 system prompt。
func buildExtractPrompt() string {
	return `你是一个记忆提取器。从对话中提取值得长期记住的信息。

## 输出格式
严格输出 JSON，不要输出其他任何内容（不要 markdown 代码块、不要解释）：

{
  "summary": "本轮对话的简要摘要（1-2句话）",
  "memories": [
    {
      "action": "create",
      "name": "short-kebab-name",
      "description": "一句话描述",
      "type": "user",
      "importance": 3,
      "tags": ["tag1", "tag2"],
      "content": "详细内容"
    }
  ]
}

## 记忆类型
- user: 用户偏好、习惯、背景信息
- feedback: 用户对你的反馈（纠正、表扬、要求）
- project: 项目相关信息、技术决策、架构选择
- reference: 外部资源、文档、链接

## 规则
- 只提取值得长期记住的信息，不要记流水账
- importance: 1=可丢弃, 2=一般, 3=有用, 4=重要, 5=关键
- name 使用简短的英文 kebab-case（如 "preference-go-lang"）
- 如果没有值得提取的，memories 为空数组 []
- summary 始终不为空，用中文
- content 用中文
- action 可以是 create（新建）、update（更新已有记忆）、delete（删除过时记忆）`
}

// cleanJSONResponse 清理 LLM 返回的 JSON，去除可能的 markdown 代码块标记。
func cleanJSONResponse(s string) string {
	s = strings.TrimSpace(s)
	// 去掉 ```json ... ``` 包裹。
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		if len(lines) >= 3 {
			// 去掉首行和末行。
			lines = lines[1:]
			// 找到最后一个 ```。
			for i := len(lines) - 1; i >= 0; i-- {
				if strings.TrimSpace(lines[i]) == "```" {
					lines = lines[:i]
					break
				}
			}
			s = strings.Join(lines, "\n")
		}
	}
	return strings.TrimSpace(s)
}

// truncateForSummary 降级方案：从原始对话生成简易摘要。
func truncateForSummary(userInput, assistantOutput string) string {
	user := strings.TrimSpace(strings.ReplaceAll(userInput, "\n", " "))
	if len([]rune(user)) > 60 {
		user = string([]rune(user)[:60]) + "..."
	}
	assistant := strings.TrimSpace(strings.ReplaceAll(assistantOutput, "\n", " "))
	if len([]rune(assistant)) > 80 {
		assistant = string([]rune(assistant)[:80]) + "..."
	}
	return fmt.Sprintf("用户问: %s | 回复: %s", user, assistant)
}
