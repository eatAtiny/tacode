package prompt

import (
	"fmt"
	"time"

	"agentic/internal/llm"
)

// ──────────────────────────────────────────────────────────
// Builder — 提示词消息构建器
// ──────────────────────────────────────────────────────────

// BuildOptions 表示构建消息所需的每轮参数。
//
// agent 循环只负责提供数据，不关心消息内部结构。
// Builder 封装了 3 段消息结构的组装逻辑。
type BuildOptions struct {
	Round         int       // 当前轮次号
	UserInput     string    // 用户原始输入
	ContextDigest string    // 记忆上下文（空表示无上下文，跳过 system-reminder）
	WorkDir       string    // 当前工作目录
	SessionStart  time.Time // 会话开始时间
}

// Builder 预构建 system prompt 并封装消息组装逻辑。
//
// 设计意图：
//   - agent 循环不再关心消息结构（3 段还是 4 段、静态还是动态）
//   - system prompt 构造一次、复用整个会话（缓存友好）
//   - 调用方只需传业务数据，Builder 负责正确性
type Builder struct {
	systemPrompt string // 预构建的完整 system prompt（静态 + 边界 + 动态）
}

// NewBuilder 创建 Builder 并预构建 system prompt。
//
// toolDescriptions 和 guides 在 Builder 生命周期内不应变化
// （对应当前会话加载的工具集）。
func NewBuilder(toolDescriptions string, guides []ToolGuide) *Builder {
	return &Builder{
		systemPrompt: BuildReActSystemPrompt(toolDescriptions, guides),
	}
}

// BuildMessages 返回完整的初始消息数组（3 段结构）。
//
// 返回值直接作为 queryLoop 的初始 messages 传入。
// 消息结构：
//
//	messages[0] system  = 静态段(可缓存) + 动态段(会话稳定)
//	messages[1] user    = <system-reminder> 会话环境 + 记忆上下文  (contextDigest 非空时)
//	messages[2] user    = 轮次 + 用户任务
//
// 缓存行为：
//   - messages[0]: 全局缓存命中（所有用户相同）
//   - messages[1]: 会话内大概率命中（工作目录、开始时间不变）
//   - messages[2]: 每轮变化但极简，断点代价最小
func (b *Builder) BuildMessages(opts BuildOptions) []llm.ChatMessage {
	messages := []llm.ChatMessage{
		{Role: "system", Content: b.systemPrompt},
	}

	if opts.ContextDigest != "" {
		reminder := fmt.Sprintf(
			"会话环境:\n- 工作目录: %s\n- 会话开始时间: %s\n\n上下文生成时间: %s\n\n%s",
			opts.WorkDir,
			opts.SessionStart.Format("2006-01-02 15:04:05"),
			time.Now().Format("2006-01-02 15:04:05"),
			opts.ContextDigest,
		)
		messages = append(messages, llm.ChatMessage{
			Role:    "user",
			Content: BuildSystemReminder(reminder),
		})
	}

	messages = append(messages, llm.ChatMessage{
		Role:    "user",
		Content: BuildUserTask(opts.Round, opts.UserInput),
	})

	return messages
}

// ExtendContext 在已有会话上下文基础上追加新轮次的消息。
//
// 与 BuildMessages 的区别：
//   - BuildMessages 创建全新的 [system, reminder, task] 消息数组
//   - ExtendContext 保留 system prompt + 历史对话，只替换 per-turn 消息
//
// existing: 之前保存的 SessionContext.Messages（context.json 中的快照）
// 结构假设：[0]=system, [1]=old_reminder, [2]=old_task, [3+]=conversation history
//
// 首次查询应使用 BuildMessages；后续查询应使用 ExtendContext 以上下文延续。
func (b *Builder) ExtendContext(existing []llm.ChatMessage, opts BuildOptions) []llm.ChatMessage {
	fresh := b.BuildMessages(opts) // [system, reminder, task]

	// 保留上次的对话历史（asst/tool 消息，从索引 3 开始）。
	if len(existing) > 3 {
		fresh = append(fresh, existing[3:]...)
	}

	return fresh
}
