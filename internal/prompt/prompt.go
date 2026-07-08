// Package prompt 提供 ReAct Agent 的提示词模板。
//
// 调用链中的角色：
//   QueryEngine（步骤 3）
//     → BuildReActSystemPrompt(toolDescriptions, guides)  ← 构建 System Prompt
//     → BuildReActUserPrompt(round, context, input) ← 构建 User Prompt
//     → 组合为 messages[0] = system, messages[1] = user
//     → 传入 queryLoop()
//
// 动静分离设计（v2）：
//
//   messages[0] system:
//     ┌─ Static (全局可缓存) ────────────────────────────┐
//     │ 核心指令 + 工具 Schema + 工作方式 + 注意事项       │
//     │ <system-reminder> 标签说明                        │
//     └──────────────────────────────────────────────────┘
//     ──── StaticDynamicBoundary ────────────────────────
//     ┌─ Dynamic (会话级稳定) ───────────────────────────┐
//     │ 工具使用指南 (PromptGuide)                        │
//     └──────────────────────────────────────────────────┘
//
//   messages[1] user (system-reminder):
//     <system-reminder> 记忆上下文 </system-reminder>
//
//   messages[2] user (task):
//     环境元数据（工作目录/时间）+ 轮次: N + 用户任务
//
// 前缀缓存命中：messages[0] 对所有用户相同→始终命中；
// messages[1] 会话级稳定→同会话内命中。
package prompt

import (
	"fmt"
	"strings"
)

// ──────────────────────────────────────────────────────────
// 动静分离哨兵
// ──────────────────────────────────────────────────────────

// StaticDynamicBoundary 分隔 system prompt 的静态段和动态段。
//
// 边界以上（静态段）：对所有用户相同 → 全局前缀缓存命中。
// 边界以下（动态段）：会话级别稳定（工具使用指南），同会话内缓存命中。
//
// 此哨兵写入 system prompt 中，不参与实际推理，仅作为结构标记。
const StaticDynamicBoundary = "__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__"

// ──────────────────────────────────────────────────────────
// 向后兼容的类型和函数
// ──────────────────────────────────────────────────────────

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
// BuildReActStaticPrompt — 静态段（全局可缓存）
// ──────────────────────────────────────────────────────────

// BuildReActStaticPrompt 构建 system prompt 的静态段。
//
// 静态段对所有用户、所有会话完全相同，是前缀缓存的核心命中区。
// 包含：核心指令 + 可用工具 Schema + 工作方式 + 注意事项 + system-reminder 说明。
//
// 工具使用指南（PromptGuide）不在此段中——它在动态段，
// 因为不同项目/会话可能加载不同的工具集。
func BuildReActStaticPrompt(toolDescriptions string) string {
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
- 优先使用专用工具（grep、list、edit）而非执行 shell 命令
- 可以同时调用多个工具，只要它们之间没有依赖关系
- 收到工具结果后，判断信息是否足够回答用户：
  - 足够 → 立即给出最终回答，不要再调用任何工具
  - 不足 → 调用一个不同的工具（不要重复调用同一个工具）
- 工具调用失败时，换一种方式尝试，不要重复相同的命令
- 最终回答要简洁明了，用中文回复

工具结果和用户消息可能包含 <system-reminder> 标签。
其中的内容是系统自动注入的上下文信息，与你当前的具体任务可能无关，
仅供参考。

## 工具结果说明
- 工具结果可能因内容过长而被截断。截断时保留头部和尾部，并标明原始总长度。
- 工具结果末尾出现"💾 完整结果已保存到: <路径>"时，说明完整内容已持久化到磁盘，
  你可以使用 file read 读取该路径获取完整内容。
- 如果截断后的信息不足以回答问题，先尝试用更精细的工具获取补充信息
  （如 grep 搜索特定内容、file read 读取保存的完整结果），
  而不是重复调用同一个已截断的工具。`, toolDescriptions)
}

// ──────────────────────────────────────────────────────────
// BuildReActDynamicPrompt — 动态段（会话级稳定）
// ──────────────────────────────────────────────────────────

// BuildReActDynamicPrompt 构建 system prompt 的动态段。
//
// 动态段包含工具使用指南（从 Tool.PromptGuide() 收集）。
// 这些指南在会话级别是稳定的（工具集不会在会话中途变化），
// 但不能全局缓存（不同项目可能加载不同工具集或不同版本的指南）。
//
// guides 为空时返回空字符串。
func BuildReActDynamicPrompt(guides []ToolGuide) string {
	if len(guides) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 工具使用指南\n\n")
	hasContent := false
	for _, g := range guides {
		if g.Guide != "" {
			sb.WriteString(fmt.Sprintf("- **%s**: %s\n", g.Name, g.Guide))
			hasContent = true
		}
	}
	if !hasContent {
		return ""
	}
	return sb.String()
}

// ──────────────────────────────────────────────────────────
// BuildReActSystemPrompt — 组合静态+动态（向后兼容）
// ──────────────────────────────────────────────────────────

// BuildReActSystemPrompt 构建完整的 ReAct system prompt。
//
// 内部调用 BuildReActStaticPrompt + BuildReActDynamicPrompt，
// 用 StaticDynamicBoundary 分隔两者。
//
// 这是 queryLoop 收到的第一条消息（messages[0]）。
func BuildReActSystemPrompt(toolDescriptions string, guides []ToolGuide) string {
	static := BuildReActStaticPrompt(toolDescriptions)
	dynamic := BuildReActDynamicPrompt(guides)

	if dynamic == "" {
		return static
	}

	return static + "\n" + StaticDynamicBoundary + "\n" + dynamic
}

// ──────────────────────────────────────────────────────────
// BuildSystemReminder — 记忆上下文注入包装
// ──────────────────────────────────────────────────────────

// BuildSystemReminder 将记忆上下文包装为 <system-reminder> 格式。
//
// 用于 messages[1]（user 角色），将三层记忆上下文注入为独立的 user 消息。
// 这样 system prompt 可以保持稳定（缓存友好），而变化的上下文作为独立消息。
//
// contextDigest 为空时返回空字符串（调用方应跳过此消息）。
func BuildSystemReminder(contextDigest string) string {
	if contextDigest == "" {
		return ""
	}
	return "<system-reminder>\n" + contextDigest + "\n</system-reminder>"
}

// ──────────────────────────────────────────────────────────
// BuildUserTask — 用户任务消息
// ──────────────────────────────────────────────────────────

// BuildUserTask 构建用户任务消息。
//
// 用于 messages[2]（user 角色），只包含轮次号和用户输入。
// 环境元数据（工作目录、会话时间）已移到 system-reminder（messages[1]），
// 此处保持极简以最小化每轮变化的缓存断点。
func BuildUserTask(round int, userInput string) string {
	return fmt.Sprintf("轮次: %d\n\n用户任务:\n%s", round, userInput)
}

// ──────────────────────────────────────────────────────────
// BuildReActUserPrompt — 向后兼容
// ──────────────────────────────────────────────────────────

// BuildReActUserPrompt 构建 ReAct 模式的 user prompt（向后兼容）。
//
// 内部调用 BuildSystemReminder + BuildUserTask 并拼接。
// 新代码推荐分别使用 BuildSystemReminder 和 BuildUserTask，
// 将其作为独立消息发送以获得更好的缓存行为。
func BuildReActUserPrompt(round int, contextDigest, userInput string) string {
	var parts []string

	if contextDigest != "" {
		parts = append(parts, BuildSystemReminder(contextDigest))
	}
	parts = append(parts, BuildUserTask(round, userInput))

	return strings.Join(parts, "\n\n")
}
