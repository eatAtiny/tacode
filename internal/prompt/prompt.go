package prompt

import "fmt"

// RoundPrompt 表示单轮请求发给模型的提示词结构。
type RoundPrompt struct {
	System string
	User   string
}

// BuildRoundPrompt 组装每轮提示词（普通对话模式）。
func BuildRoundPrompt(round int, memoryDigest, userInput string) RoundPrompt {
	system := "你是一个有帮助的 AI 助手。请用简洁自然的中文回答用户的问题。"
	user := fmt.Sprintf("轮次: %d\n最近对话记录:\n%s\n\n用户输入:\n%s", round, memoryDigest, userInput)
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
- 工具调用失败时，分析原因并尝试其他方案
- 最终回答要简洁明了，用中文回复`, toolDescriptions)
}

// BuildReActUserPrompt 构建 ReAct 模式的 user prompt。
func BuildReActUserPrompt(round int, memoryDigest, userInput string) string {
	return fmt.Sprintf("轮次: %d\n最近对话记录:\n%s\n\n用户任务:\n%s", round, memoryDigest, userInput)
}

// BuildPlanPrompt 构建规划阶段的 system prompt。
// LLM 在此阶段不调用工具，只需输出结构化的 todo 列表。
func BuildPlanPrompt(toolDescriptions string) string {
	return fmt.Sprintf(`你是一个任务规划器。请分析用户的任务，将其拆解为有序的执行步骤。

## 可用工具
%s

## 输出格式
严格输出 JSON 数组，不要输出其他任何内容（不要 markdown 代码块、不要解释）：
[{"id":1,"content":"步骤描述"}, {"id":2,"content":"步骤描述"}]

## 规则
- 每个步骤应该是单一、明确的操作
- 步骤之间有逻辑顺序
- 涉及工具的步骤，在 content 中说明要用什么工具、做什么
- 如果任务简单不需要拆解，返回 [{"id":1,"content":"直接完成任务"}]
- 用中文描述步骤`, toolDescriptions)
}

// BuildPlanUserPrompt 构建规划阶段的 user prompt。
func BuildPlanUserPrompt(round int, memoryDigest, userInput string) string {
	return fmt.Sprintf("轮次: %d\n最近对话记录:\n%s\n\n请为以下任务制定执行计划:\n%s", round, memoryDigest, userInput)
}

// BuildExecPrompt 构建执行阶段的 system prompt。
// todosText 是格式化后的 todo 列表（含状态标记），currentStep 是当前步骤描述，
// doneSummary 是已完成步骤的结果摘要。
func BuildExecPrompt(todosText, currentStep, doneSummary string) string {
	return fmt.Sprintf(`你是一个任务执行器。请根据计划执行当前步骤。

## 当前计划
%s

## 当前步骤
%s

## 已完成步骤的结果
%s

## 说明
- 专注于执行当前步骤，不要跳到其他步骤
- 如果需要调用工具来完成当前步骤，请调用
- 执行完成后，简要说明结果（2-3 句话即可）
- 用中文回复`, todosText, currentStep, doneSummary)
}
