// Package prompt 提供 ReAct Agent 的提示词模板。
//
// 调用链中的角色：
//   QueryEngine（步骤 3）
//     → BuildReActSystemPrompt(toolDescriptions, guides)  ← 构建 System Prompt
//     → BuildReActUserPrompt(round, context, input) ← 构建 User Prompt
//     → 组合为 messages[0] = system, messages[1] = user
//     → 传入 queryLoop()
package prompt

import (
	"fmt"
	"strings"
)

// RoundPrompt 表示单轮请求发给模型的提示词结构。
type RoundPrompt struct {
	System string
	User   string
}

// BuildRoundPrompt 组装每轮提示词（普通对话模式，已较少使用）。
func BuildRoundPrompt(round int, contextDigest, userInput string) RoundPrompt {
	system := "你是一个有帮助的 AI 助手。请用简洁自然的中文回答用户的问题。"
	user := fmt.Sprintf("轮次: %d\n记忆上下文:\n%s\n\n用户输入:\n%s", round, contextDigest, userInput)
	return RoundPrompt{System: system, User: user}
}

// ──────────────────────────────────────────────────────────
// ToolGuide — 工具 Prompt 自引导
// ──────────────────────────────────────────────────────────

// ToolGuide 表示单个工具向 system prompt 注入的使用引导。
//
// 来源：Tool.PromptGuide() 方法。
// 每个工具通过 ToolGuide 向 LLM 提供：
//   - Name: 工具名称
//   - Guide: 使用指引（如何正确使用、注意事项、与其他工具的协作方式）
type ToolGuide struct {
	Name  string // 工具名称
	Guide string // 使用引导文本（来自 Tool.PromptGuide()，空字符串表示无引导）
}

// ──────────────────────────────────────────────────────────
// BuildReActSystemPrompt
// ──────────────────────────────────────────────────────────

// BuildReActSystemPrompt 构建 ReAct 模式的 system prompt。
//
// 这是 queryLoop 收到的第一条消息（messages[0]）。
// 告诉 LLM：
//  1. 它具备工具调用能力
//  2. 有哪些可用工具（toolDescriptions 由 Registry.Descriptions() 生成）
//  3. 工作方式（分析 → 调用工具 → 根据结果继续 → 给出答案）
//  4. 注意事项（简单闲聊直接回答、优先用专用工具、出错换方案）
//  5. 各工具的使用指南（从 Tool.PromptGuide() 收集）
//
// toolDescriptions 格式示例：
//
//	- shell: 执行 bash 命令
//	  参数: {"command": "string"}
//	- file: 读写文件
//	  参数: {"action": "read|write", "path": "string", ...}
func BuildReActSystemPrompt(toolDescriptions string, guides []ToolGuide) string {
	var guideSection string
	if len(guides) > 0 {
		var sb strings.Builder
		sb.WriteString("\n## 工具使用指南\n\n")
		hasContent := false
		for _, g := range guides {
			if g.Guide != "" {
				sb.WriteString(fmt.Sprintf("- **%s**: %s\n", g.Name, g.Guide))
				hasContent = true
			}
		}
		if hasContent {
			guideSection = sb.String()
		}
	}

	return fmt.Sprintf(`你是一个具备工具调用能力的 AI 助手。你可以通过调用工具来完成用户的任务。

## 可用工具
%s

## 工作方式
1. 分析用户任务，判断是否需要使用工具
2. 如果需要工具，调用合适的工具获取信息或执行操作
3. 根据工具返回的结果继续思考，必要时再次调用工具
4. 当你有了足够的信息，直接给出最终答案
%s
## 注意事项
- 如果用户的问题不需要工具（如简单闲聊），直接回答即可
- 优先使用专用工具（grep、list、edit）而非执行 shell 命令
- 可以同时调用多个工具，只要它们之间没有依赖关系（如 list 两个不同目录、grep + file read）
- 收到工具结果后，判断信息是否足够回答用户：
  - 足够 → 立即给出最终回答，不要再调用任何工具
  - 不足 → 调用一个不同的工具（不要重复调用同一个工具）
- 工具调用失败时，换一种方式尝试，不要重复相同的命令
- 最终回答要简洁明了，用中文回复`, toolDescriptions, guideSection)
}

// BuildReActUserPrompt 构建 ReAct 模式的 user prompt。
//
// 这是 queryLoop 收到的第二条消息（messages[1]）。
// 包含：
//   - round: 当前轮次号
//   - contextDigest: 由 Retriever.BuildContext() 构建的三层记忆上下文
//   - userInput: 用户原始输入
func BuildReActUserPrompt(round int, contextDigest, userInput string) string {
	return fmt.Sprintf("轮次: %d\n记忆上下文:\n%s\n\n用户任务:\n%s", round, contextDigest, userInput)
}
