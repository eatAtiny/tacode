package agent

import (
	"context"
	"fmt"
	"strings"

	"agentic/internal/memory"
)

// ──────────────────────────────────────────────────────────
// 记忆管理函数
// ──────────────────────────────────────────────────────────

// extractMemory 调用 LLM 提取摘要和结构化记忆。
//
// 职责：
//   - 调用 extractor 提取摘要和记忆
//   - 保存 L2 摘要
//   - 保存 L3 结构化记忆（创建/更新/删除）
//
// 参数：
//   - ctx: 上下文
//   - round: 当前轮次号
//   - userInput: 用户输入
//   - assistantOutput: 助手回答
func (r *Runner) extractMemory(ctx context.Context, round int, userInput, assistantOutput string) {
	result, err := r.extractor.Extract(ctx, userInput, assistantOutput)
	if err != nil {
		fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 记忆提取失败: %v", err)))
		return
	}

	// 保存 L2 摘要。
	if result.Summary != "" {
		if err := r.summary.Append(round, result.Summary); err != nil {
			fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 保存摘要失败: %v", err)))
		}
	}

	// 保存 L3 记忆。
	for _, action := range result.Memories {
		switch action.Action {
		case "create", "update":
			entry := memory.MemoryEntry{
				Name:        action.Name,
				Description: action.Description,
				Type:        action.Type,
				Importance:  action.Importance,
				Tags:        action.Tags,
				Content:     action.Content,
			}
			if err := r.memStore.SaveEntry(entry); err != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 保存记忆失败: %v", err)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("💾 记忆已保存: %s", action.Description)))
			}
		case "delete":
			if err := r.memStore.DeleteEntry(action.Name); err != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 删除记忆失败: %v", err)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("🗑️ 记忆已删除: %s", action.Name)))
			}
		}
	}
}

// handleCompress 手动触发摘要压缩。
func (r *Runner) handleCompress() {
	fmt.Printf("\n%s\n", memoryBoxStyle.Render("🗜️ 正在压缩摘要..."))

	// 使用一个简单的上下文。
	ctx := context.Background()
	err := r.retriever.CompressSummaries(ctx, r.llm)
	if err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("压缩失败: %v", err)))
		return
	}

	count, _ := r.summary.Count()
	fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 压缩完成，当前 %d 条摘要", count)))
}

// handleMemoryCommand 处理 /memory 子命令。
//
// 子命令：
//   - list: 列出所有记忆
//   - add <内容>: 手动添加一条记忆
//   - rm <name>: 删除一条记忆
func (r *Runner) handleMemoryCommand(parts []string) {
	if len(parts) < 2 {
		// 默认列出所有记忆。
		r.listMemories()
		return
	}

	sub := strings.ToLower(parts[1])
	switch sub {
	case "list":
		r.listMemories()
	case "add":
		if len(parts) < 3 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /memory add <内容>"))
			return
		}
		content := strings.Join(parts[2:], " ")
		r.addMemory(content)
	case "rm", "delete":
		if len(parts) < 3 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /memory rm <name>"))
			return
		}
		name := parts[2]
		r.deleteMemory(name)
	default:
		fmt.Printf("\n%s\n", errorStyle.Render("用法: /memory [list|add|rm]"))
	}
}

// listMemories 列出所有记忆。
func (r *Runner) listMemories() {
	entries, err := r.memStore.ListEntries()
	if err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("读取记忆失败: %v", err)))
		return
	}
	if len(entries) == 0 {
		fmt.Printf("\n%s\n", mutedStyle.Render("  (暂无记忆)"))
		return
	}

	fmt.Printf("\n%s\n", memoryBoxStyle.Render(fmt.Sprintf("🧠 共 %d 条记忆:", len(entries))))
	for _, e := range entries {
		importanceIcon := strings.Repeat("⭐", e.Importance)
		fmt.Printf("  %s %s\n", mutedStyle.Render(fmt.Sprintf("[%s]", e.Type)), e.Description)
		fmt.Printf("    %s name=%s\n", mutedStyle.Render(importanceIcon), e.Name)
	}
}

// addMemory 手动添加一条记忆。
func (r *Runner) addMemory(content string) {
	// 生成一个简单的 name。
	name := fmt.Sprintf("manual-%s", strings.ReplaceAll(strings.ToLower(content[:min(20, len(content))]), " ", "-"))
	name = strings.TrimRight(name, "-")

	entry := memory.MemoryEntry{
		Name:        name,
		Description: content,
		Type:        "user",
		Importance:  3,
		Tags:        []string{"manual"},
		Content:     content,
	}
	if err := r.memStore.SaveEntry(entry); err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("保存记忆失败: %v", err)))
		return
	}
	fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 记忆已保存: %s", content)))
}

// deleteMemory 删除一条记忆。
func (r *Runner) deleteMemory(name string) {
	if err := r.memStore.DeleteEntry(name); err != nil {
		fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("删除记忆失败: %v", err)))
		return
	}
	fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 记忆已删除: %s", name)))
}
