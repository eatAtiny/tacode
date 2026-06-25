package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentic/internal/llm"
)

// 默认配置。
const (
	defaultSummaryCount = 10  // 默认加载最近 10 条摘要
	maxMemoryEntries    = 10  // 默认最多加载 10 条记忆
	minImportance       = 2   // 默认最低重要性
	compressThreshold   = 0.8 // token 使用率超过 80% 触发压缩
)

// Retriever 负责从三层存储中检索信息，构建注入 prompt 的上下文。
type Retriever struct {
	history  *HistoryStore
	summary  *SummaryStore
	memory   *MemoryStore
	events   *EventStore
}

// NewRetriever 构造 Retriever。
func NewRetriever(history *HistoryStore, summary *SummaryStore, memory *MemoryStore, events *EventStore) *Retriever {
	return &Retriever{
		history: history,
		summary: summary,
		memory:  memory,
		events:  events,
	}
}

// BuildContext 构建注入 prompt 的上下文文本。
// 优先使用 L2 摘要，L2 为空时降级到 L1 原始日志摘要。
// 始终包含 L3 记忆索引和高重要性记忆。
func (r *Retriever) BuildContext(query string) (string, error) {
	var parts []string

	// 1. 加载 L3 记忆索引。
	index := r.memory.LoadIndex()
	if index != "" {
		parts = append(parts, "## 记忆索引\n"+index)
	}

	// 2. 加载 L3 高重要性记忆内容。
	memories, err := r.memory.FormatForPrompt(maxMemoryEntries, minImportance)
	if err == nil && memories != "" {
		parts = append(parts, "## 重要记忆\n"+memories)
	}

	// 3. 加载 L2 最近摘要。
	summaryText, err := r.summary.FormatRecent(defaultSummaryCount)
	if err == nil && summaryText != "" {
		parts = append(parts, "## 最近对话摘要\n"+summaryText)
	} else {
		// 降级：优先从事件日志生成摘要，再降级到原始日志。
		if r.events != nil {
			eventDigest := r.events.Digest(defaultSummaryCount)
			if eventDigest != "" && eventDigest != "(无历史记录)" {
				parts = append(parts, "## 最近对话记录\n"+eventDigest)
			}
		} else {
			historyDigest := r.history.Digest(defaultSummaryCount)
			if historyDigest != "" && historyDigest != "(无历史记录)" {
				parts = append(parts, "## 最近对话记录\n"+historyDigest)
			}
		}
	}

	if len(parts) == 0 {
		return "", nil
	}
	return strings.Join(parts, "\n\n"), nil
}

// CheckAndCompress 检查是否需要压缩，超过阈值时自动压缩 L2 摘要。
// tokenLimit 是模型的上下文窗口大小，currentUsage 是当前已用 token。
func (r *Retriever) CheckAndCompress(ctx context.Context, client *llm.OpenAIClient, tokenLimit, currentUsage int) (bool, error) {
	if tokenLimit <= 0 {
		return false, nil
	}

	usageRatio := float64(currentUsage) / float64(tokenLimit)
	if usageRatio < compressThreshold {
		return false, nil
	}

	return true, r.CompressSummaries(ctx, client)
}

// CompressSummaries 使用 LLM 合并旧摘要，保留最近几条不动。
func (r *Retriever) CompressSummaries(ctx context.Context, client *llm.OpenAIClient) error {
	all, err := r.summary.LoadAll()
	if err != nil {
		return fmt.Errorf("load summaries failed: %w", err)
	}
	if len(all) <= 3 {
		return nil // 太少不需要压缩
	}

	// 保留最近 3 条不动，压缩其余的。
	keepRecent := 3
	toCompress := all[:len(all)-keepRecent]
	recent := all[len(all)-keepRecent:]

	// 构建压缩 prompt。
	var oldSummaries strings.Builder
	for _, s := range toCompress {
		fmt.Fprintf(&oldSummaries, "- [轮次 %d] %s\n", s.Round, s.Summary)
	}

	compressPrompt := fmt.Sprintf(`请将以下对话摘要合并为一段简洁的综合摘要（3-5句话），保留关键信息，去除重复内容。

## 原始摘要
%s

## 要求
- 用中文
- 保留重要的用户偏好、决策、项目信息
- 去除已过时或重复的内容
- 只输出摘要文本，不要其他内容`, oldSummaries.String())

	compressed, err := client.Chat(ctx, "你是一个摘要压缩器。", compressPrompt)
	if err != nil {
		return fmt.Errorf("compress llm call failed: %w", err)
	}

	// 用压缩后的摘要替换旧的。
	compressedSummary := Summary{
		Round:     toCompress[len(toCompress)-1].Round,
		Timestamp: time.Now().Format(time.RFC3339),
		Summary:   fmt.Sprintf("[压缩摘要] %s", strings.TrimSpace(compressed)),
	}

	newSummaries := append([]Summary{compressedSummary}, recent...)
	return r.summary.ReplaceAll(newSummaries)
}

// EstimateTokens 粗略估算文本的 token 数。
// ASCII 文本约 4 字符/token，中文约 2 字符/token。
func EstimateTokens(text string) int {
	asciiCount := 0
	cjkCount := 0
	for _, r := range text {
		if r <= 127 {
			asciiCount++
		} else {
			cjkCount++
		}
	}
	return asciiCount/4 + cjkCount/2
}
