package prompt

import "fmt"

// RoundPrompt 表示单轮请求发给模型的提示词结构。
type RoundPrompt struct {
	System string
	User   string
}

// BuildRoundPrompt 组装每轮提示词（普通对话模式）。
func BuildRoundPrompt(round int, contextDigest, userInput string) RoundPrompt {
	system := "你是一个有帮助的 AI 助手。请用简洁自然的中文回答用户的问题。"
	user := fmt.Sprintf("轮次: %d\n记忆上下文:\n%s\n\n用户输入:\n%s", round, contextDigest, userInput)
	return RoundPrompt{System: system, User: user}
}

// BuildReActSystemPrompt 构建 ReAct 模式的 system prompt。
// toolDescriptions 是所有可用工具的描述文本。
func BuildReActSystemPrompt(toolDescriptions string) string {
	return fmt.Sprintf(`你是一个具备工具调用能力的 AI 助手。你可以通过调用工具来完成用户的任务。

## 可用工具
%s

## 工作方式
1. 分析用户任务，判断是否需要使用工具
2. 如果需要工具，调用合适的工具获取信息或执行操作
3. 根据工具返回的结果继续思考，必要时再次调用工具
4. 当你有了足够的信息，直接给出最终答案

## 注意事项
- 如果用户的问题不需要工具（如简单闲聊），直接回答即可
- 每次只调用一个工具
- 收到工具结果后，判断信息是否足够回答用户：
  - 足够 → 立即给出最终回答，不要再调用任何工具
  - 不足 → 调用一个不同的工具（不要重复调用同一个工具）
- 工具调用失败时，换一种方式尝试，不要重复相同的命令
- 最终回答要简洁明了，用中文回复`, toolDescriptions)
}

// BuildReActUserPrompt 构建 ReAct 模式的 user prompt。
func BuildReActUserPrompt(round int, contextDigest, userInput string) string {
	return fmt.Sprintf("轮次: %d\n记忆上下文:\n%s\n\n用户任务:\n%s", round, contextDigest, userInput)
}

