package prompt

import "fmt"

// RoundPrompt 表示单轮请求发给模型的提示词结构。
type RoundPrompt struct {
	System string
	User   string
}

// BuildRoundPrompt 组装每轮提示词：
// 1) system 约束 agent 风格
// 2) user 包含轮次、记忆摘要和本轮输入
func BuildRoundPrompt(round int, memoryDigest, userInput string) RoundPrompt {
	system := "你是一个有帮助的 AI 助手。请用简洁自然的中文回答用户的问题。"
	user := fmt.Sprintf("轮次: %d\n最近对话记录:\n%s\n\n用户输入:\n%s", round, memoryDigest, userInput)
	return RoundPrompt{System: system, User: user}
}
