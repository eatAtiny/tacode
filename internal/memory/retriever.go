package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentic/internal/llm"
)

// 默认配置。
const (
	defaultSummaryCount = 10  // 默认加载最近 10 条摘要
	maxMemoryEntries    = 10  // 默认最多加载 10 条记忆
	minImportance       = 2   // 默认最低重要性（1-5，>=2 才会注入 prompt）
	compressThreshold   = 0.8 // token 使用率超过 80% 触发压缩
)

// ──────────────────────────────────────────────────────────
// Retriever — 三层记忆检索器
//
// 调用链中的角色：
//   QueryEngine（步骤 1）
//     → Retriever.BuildContext(query)
//       → 返回上下文文本（注入 system/user prompt）
//
//   QueryEngine（步骤 2）
//     → Retriever.CheckAndCompress(ctx, client, tokenLimit, currentUsage)
//       → 超过 80% 阈值时调用 CompressSummaries()
//
// 检索顺序（优先级从高到低）：
//   1. L3 记忆索引（MEMORY.md）—— 所有记忆的目录
//   2. L3 高重要性记忆内容（importance >= 2，最多 10 条）
//   3. L2 最近摘要（最近 10 条，LLM 提取的对话摘要）
//   4. 降级方案：L2 为空时 → EventStore 摘要 → HistoryStore 摘要
// ──────────────────────────────────────────────────────────

// Retriever 负责从三层存储中检索信息，构建注入 prompt 的上下文。
type Retriever struct {
	history         *HistoryStore
	summary         *SummaryStore
	memory          *MemoryStore
	events          *EventStore
	projectInstr    string // 项目指令内容（缓存），空 = 未加载
	projectInstrSrc string // 来源文件路径（调试/显示用）
}

// NewRetriever 构造 Retriever，组合三层存储。
func NewRetriever(history *HistoryStore, summary *SummaryStore, memory *MemoryStore, events *EventStore) *Retriever {
	return &Retriever{
		history: history,
		summary: summary,
		memory:  memory,
		events:  events,
	}
}

// projectInstrMaxSize 项目指令文件最大读取大小（64KB）。
// 超过此大小截断，避免注入过大内容占用上下文。
const projectInstrMaxSize = 64 * 1024

// LoadProjectInstructions 从进程工作目录向上查找 AGENTS.md / AGENTS 文件并缓存。
//
// 查找规则：
//   - 候选文件名：AGENTS.md、AGENTS（无扩展名）
//   - 从 os.Getwd() 逐级向上，到包含 .git 的目录（仓库根）停止
//   - 找到即停（不合并多级，只取最近一级）
//
// 失败时返回错误（如无文件），但不清空已有缓存（保留旧内容）。
func (r *Retriever) LoadProjectInstructions() error {
	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working dir failed: %w", err)
	}
	path, content, err := findProjectInstructions(startDir)
	if err != nil {
		return err
	}
	if path == "" {
		return nil // 未找到，保持空缓存
	}
	r.projectInstr = content
	r.projectInstrSrc = path
	return nil
}

// ClearProjectInstructions 清空项目指令缓存。
// 配合 LoadProjectInstructions 实现手动刷新（/reload 命令）。
func (r *Retriever) ClearProjectInstructions() {
	r.projectInstr = ""
	r.projectInstrSrc = ""
}

// BuildContext 构建注入 prompt 的上下文文本。
//
// 这是每轮查询前调用的核心方法（QueryEngine 步骤 1）。
//
// 检索流程（优先级从高到低）：
//
//	步骤 1.1: 加载 L3 记忆索引（MEMORY.md）
//	步骤 1.2: 加载 L3 高重要性记忆内容（importance >= 2，最多 10 条）
//	步骤 1.3: 加载 L2 最近摘要（最近 10 条）
//	步骤 1.4: 降级方案
//	          L2 为空 → EventStore.Digest()（从事件日志生成文本摘要）
//	          EventStore 为空 → HistoryStore.Digest()（从原始日志生成摘要）
//
// 返回：
//   - string: 拼接后的上下文文本（可直接注入 prompt）
//   - error: 读取错误（降级方案也会尝试，尽最大努力返回可用上下文）
func (r *Retriever) BuildContext(query string) (string, error) {
	var parts []string

	// ── 步骤 0: 项目指令（AGENTS.md） ──
	// 仓库级约定注入到上下文最前（优先级高于会话记忆）。
	// 未加载时静默跳过，不影响现有行为。
	if r.projectInstr != "" {
		parts = append(parts, "## 项目指令\n"+r.projectInstr)
	}

	// ── 步骤 1.1: L3 记忆索引 ──
	index := r.memory.LoadIndex()
	if index != "" {
		parts = append(parts, "## 记忆索引\n"+index)
	}

	// ── 步骤 1.2: L3 高重要性记忆内容 ──
	memories, err := r.memory.FormatForPrompt(maxMemoryEntries, minImportance)
	if err == nil && memories != "" {
		parts = append(parts, "## 重要记忆\n"+memories)
	}

	// ── 步骤 1.3: L2 最近摘要 ──
	summaryText, err := r.summary.FormatRecent(defaultSummaryCount)
	if err == nil && summaryText != "" {
		parts = append(parts, "## 最近对话摘要\n"+summaryText)
	} else {
		// ── 步骤 1.4: 降级方案 ──
		// 优先从事件日志生成摘要（更完整），再降级到原始日志。
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
//
// 调用时机：QueryEngine 步骤 2（构建上下文后、构建 prompt 前）。
//
// 判断逻辑：
//   currentUsage / tokenLimit > 80% → 触发压缩
//
// tokenLimit 是模型的上下文窗口大小，currentUsage 是当前已用 token。
// 返回 true 表示已触发压缩，上层应重新构建上下文。
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
//
// 流程：
//   1. 加载所有 L2 摘要
//   2. 如果 <= 3 条，无需压缩
//   3. 保留最近 3 条不动，压缩其余的（toCompress）
//   4. 构建压缩 prompt：将 toCompress 格式化为列表
//   5. 调用 LLM 合并为一段综合摘要
//   6. 用压缩后的摘要替换旧的（1 条综合摘要 + 3 条最近摘要）
//
// 压缩后的摘要前缀 "[压缩摘要]" 标记。
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

	// 构建压缩 prompt（旧摘要列表）。
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
// 启发式算法：ASCII 文本约 4 字符/token，CJK 字符约 2 字符/token。
// 这不是精确计算（精确计算需要 tokenizer），但对压缩判断足够。
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

// findProjectInstructions 从 startDir 向上查找 AGENTS.md / AGENTS 文件。
//
// 候选文件名按优先级：AGENTS.md、AGENTS。
// 从 startDir 逐级向上，到包含 .git 的目录（仓库根）停止——包括该目录本身。
// 找到第一个存在的候选文件即返回（不合并多级）。
// 若一直未遇到 .git，到文件系统根停止。
//
// 返回：文件路径（未找到为空字符串）、内容（截断后）、错误。
func findProjectInstructions(startDir string) (string, string, error) {
	dir := startDir
	for {
		for _, name := range []string{"AGENTS.md", "AGENTS"} {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err == nil {
				return path, truncateProjectInstr(string(data)), nil
			}
			if !os.IsNotExist(err) {
				// 非"不存在"错误（权限等）——跳过该文件继续找。
				continue
			}
		}

		// 到达仓库根（含 .git）停止。
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", "", nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", nil // 到达文件系统根
		}
		dir = parent
	}
}

// truncateProjectInstr 截断过大的项目指令内容。
// 保留 head + tail（各占约一半），中间标记截断信息。
// memory 包内自实现，避免引入 tool 依赖（破坏 leaf 独立性）。
func truncateProjectInstr(s string) string {
	if len(s) <= projectInstrMaxSize {
		return s
	}
	headLen := projectInstrMaxSize / 2
	tailLen := projectInstrMaxSize - headLen
	note := fmt.Sprintf("\n\n…(项目指令过大，已截断，原 %d 字符)…\n\n", len(s))
	// 扣除提示信息长度。
	for headLen+tailLen+len(note) > projectInstrMaxSize && headLen > 0 {
		headLen--
	}
	return s[:headLen] + note + s[len(s)-tailLen:]
}
