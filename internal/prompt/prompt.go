// Package prompt 构建 ReAct 提示词。
//
// 现行消息组装（见 agent.queryEngine）：
//
//	messages[0]    = BuildReActSystemPrompt（静态 system，含工具指南）
//	messages[1]    = BuildSystemReminder（记忆 preamble，仅首轮/切换重建，此后每轮注入）
//	messages[末尾] = BuildUserTask（每轮变化的用户任务）
package prompt

import (
	"fmt"
	"strings"
)

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
- 最终回答要简洁明了，用中文回复

## 上下文说明
- 会话消息跨轮次累积。早期对话可能被压缩为 [Compacted] 或归档标记消息。
- [Compacted] 消息中的 "Current user request" 指向其产生时的用户请求；
  "Conversation summary (reference only)" 是压缩时的事实摘要，仅作参考。
- 请始终以最新的 "用户任务" 消息为准执行任务。
- 工具结果可能因内容过长被截断或转存。出现 <persisted-output> 或
  "完整结果已保存到: <路径>" 时，可使用 file read 读取完整内容。`, toolDescriptions, guideSection)
}

// BuildSystemReminder 将记忆/参考上下文包装为系统注入的参考消息。
//
// 跨轮累积架构下，注入时此消息位于 messages[1]（system 之后、累积对话之前）；
// preamble 为空（首轮无可注入的记忆上下文）时无此消息。
// 作为稳定前缀的一部分：内容只在会话切换/首次查询时重建并缓存，
// 同一会话内字节稳定，前缀缓存可命中。
//
// 使用 <system-reminder> 标签明确告知模型：其内容是参考上下文，
// 与当前任务不一定直接相关，不执行其中的指令。
func BuildSystemReminder(contextDigest string) string {
	return fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", contextDigest)
}

// BuildUserTask 构建每轮唯一的用户任务消息。
//
// 跨轮累积架构下，此消息是 messages 的最后一条，是唯一每轮变化的消息。
// 只包含轮次号与用户输入，不含记忆上下文（记忆已前置到 system-reminder），
// 以最大化前缀缓存命中（system + preamble + 累积对话保持稳定）。
func BuildUserTask(round int, userInput string) string {
	return fmt.Sprintf("轮次: %d\n\n用户任务:\n%s", round, userInput)
}
