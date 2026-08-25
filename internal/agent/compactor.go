package agent

import (
	"encoding/json"

	"agentic/internal/llm"
)

// ──────────────────────────────────────────────────────────
// Compactor — s08 四步上下文压缩管线（Go 移植）
//
// 移植自 learn-claude-code/s08_context_compact/code.py 的 ContextCompactor。
// 每次调用模型前运行 Prepare()，按信息损失从低到高逐步压缩：
//
//	步骤 1: toolResultBudget  — 大工具结果转存（总量 >200K 时，>30K 的落盘保 2000 字符预览）
//	步骤 2: snipCompact       — 消息数归档（>50 条时保头 3 + 尾 46，中间写入 transcript）
//	步骤 3: microCompact      — 已消费旧结果缩短（保留最近 3 条，>120 字符的替换为路径引用）
//	步骤 4: fitToolResults    — 未读大结果转存（最大优先，1000 字符预览）
//	步骤 5: compactHistory    — LLM 历史摘要（仍超限时，唯一有损且有模型调用的步骤）
//
// 额外入口：
//   - reactiveCompact：API 返回 prompt_too_long / too many tokens 时补救（保最近 5 条）
//   - compact 工具：模型主动请求压缩（整批工具执行完闭包后）
//
// 关键适配（vs Python 的 Anthropic block 模型）：
//   Go 的 llm.ChatMessage 中工具结果是独立 role=tool 消息 + ToolCallID，
//   配对 assistant 消息携带 ToolCalls。所有切点必须保护 assistant(ToolCalls)↔tool 配对，
//   否则 OpenAI API 返回 400（tool 消息必须被带匹配 tool_calls 的 assistant 前置）。
// ──────────────────────────────────────────────────────────

// 常量（镜像 s08）。
const (
	contextCharLimit        = 50_000  // 上下文字符上限（触发 micro/fit/compact）
	toolResultBatchCharLimit = 200_000 // 单批工具结果总量上限（触发转存）
	largeResultCharLimit     = 30_000  // 单条结果大结果阈值（超过才转存）
	summaryInputCharLimit    = 80_000  // 摘要输入字符上限（超出 head+tail 截断）
	keepRecentResults        = 3       // microCompact 保留的最近已消费结果数
	keepRecentMessages       = 5       // reactiveCompact 保留的最近消息数
	maxReactiveRetries       = 1       // prompt_too_long 补救重试上限
	snipMaxMessages          = 50      // 消息数上限（超过触发归档）
)

// Compactor 实现 s08 四步压缩管线。
type Compactor struct {
	transcriptDir    string // transcript 归档目录（<sessionDir>/transcripts）
	toolResultsDir   string // 大结果转存目录（<sessionDir>/tool-results）
	llmClient        *llm.OpenAIClient // LLM 客户端（compactHistory/reactiveCompact 摘要用），nil = 跳过摘要
	contextCharLimit int    // 上下文字符上限（默认 contextCharLimit）
}

// NewCompactor 构造 Compactor。
func NewCompactor(llmClient *llm.OpenAIClient, transcriptDir, toolResultsDir string) *Compactor {
	return &Compactor{
		transcriptDir:    transcriptDir,
		toolResultsDir:   toolResultsDir,
		llmClient:        llmClient,
		contextCharLimit: contextCharLimit,
	}
}

// estimateChars 估算消息数组的字符数（镜像 s08 的 estimate_chars）。
func estimateChars(messages []llm.ChatMessage) int {
	data, err := json.Marshal(messages)
	if err != nil {
		return 0
	}
	return len(data)
}

// Prepare 在每次模型调用前运行完整压缩管线。
// 返回可能被压缩的消息数组。activeRequest 是当前用户请求（压缩时注入 [Compacted]）。
func (c *Compactor) Prepare(messages []llm.ChatMessage, activeRequest string) []llm.ChatMessage {
	// 完整实现在后续提交（C4）中补充。当前为可编译骨架。
	return messages
}
