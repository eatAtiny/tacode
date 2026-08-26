package agent

import (
	"context"
	"fmt"
	"strings"

	"agentic/internal/memory"
)

// ──────────────────────────────────────────────────────────
// 记忆管理函数
//
// 调用链（每轮对话完成后，在后台 goroutine 中执行）：
//
//	Runner.Run() → 分支 B: 收到查询结果
//	  └─ go func() {
//	       └─ history.Append()          ← 步骤 7a: 保存 L1 原始对话
//	       └─ extractMemory()           ← 步骤 7b: 提取 L3 记忆
//	            └─ extractor.Extract()   ← 一次 LLM 调用，产出记忆操作
//	            └─ memStore.SaveEntry()  ← 保存/更新 L3 记忆
//	            └─ memStore.DeleteEntry()← 删除 L3 记忆
//	       └─ ensurePersisted()         ← 步骤 8: 临时会话落盘
//	     }()
// ──────────────────────────────────────────────────────────

// extractMemory 调用 LLM 提取结构化记忆（L3）。
//
// 流程（每次对话后执行一次）：
//   1. 调用 extractor.Extract(userInput, assistantOutput)
//      → LLM 分析对话，返回 ExtractionResult{Summary, Memories[]}
//   2. 处理 L3 记忆操作：
//      - create/update → memStore.SaveEntry()（写入 .md 文件 + 更新 MEMORY.md）
//      - delete → memStore.DeleteEntry()（删除 .md 文件 + 更新 MEMORY.md）
//
// 不再保存 L2 摘要：跨轮累积架构下对话细节由累积 messages 承载，
// L2 只在压缩（compactHistory）时作为兜底写入。
//
// 这是后台操作，不阻塞主循环。失败时通过 UI.OnMessage 提示警告，
// 不会中断 Agent 运行。
func (r *Runner) extractMemory(ctx context.Context, round int, userInput, assistantOutput string) {
	result, err := r.extractor.Extract(ctx, userInput, assistantOutput)
	if err != nil {
		r.ui.OnMessage(fmt.Sprintf("⚠️ 记忆提取失败: %v", err))
		return
	}

	// ── 步骤 7b-2: 处理 L3 记忆操作 ──
	// 三级记忆分流：
	//   - project / reference → 项目级 store（跨会话，data/project-memory/）
	//   - user / feedback     → 全局 store（跨项目，data/global-memory/）
	// 会话级不再落 L3（对话细节靠 L2 摘要 + EventStore）。
	// 对应 store 未启用时回退到会话 store（保持旧行为）。
	for _, action := range result.Memories {
		// 选择目标 store。
		store := r.memStore
		switch action.Type {
		case "project", "reference":
			if r.projectMem != nil {
				store = r.projectMem
			}
		case "user", "feedback":
			if r.globalMem != nil {
				store = r.globalMem
			}
		}

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
			if err := store.SaveEntry(entry); err != nil {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 保存记忆失败: %v", err))
			}
			// 成功不提示：记忆保存是后台副作用，每轮刷屏会覆盖用户正在输入的输入框。
			// 异常（失败）才值得打断用户。
		case "delete":
			if err := store.DeleteEntry(action.Name); err != nil {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 删除记忆失败: %v", err))
			}
			// 成功不提示（同上：后台操作，避免刷屏）。
		}
	}
}

// handleCompress 手动触发摘要压缩（/compress 命令）。
//
// 流程：
//   1. 调用 Retriever.CompressSummaries()
//   2. LLM 合并旧摘要 → 保留最近 3 条 + 1 条综合摘要
//   3. 显示压缩后的摘要数量
func (r *Runner) handleCompress() {
	r.ui.OnMessage("🗜️ 正在压缩摘要...")

	ctx := context.Background()
	err := r.retriever.CompressSummaries(ctx, r.llm)
	if err != nil {
		r.ui.OnError(fmt.Errorf("压缩失败: %v", err))
		return
	}

	count, _ := r.summary.Count()
	r.ui.OnMessage(fmt.Sprintf("✅ 压缩完成，当前 %d 条摘要", count))
}

// handleMemoryCommand 处理 /memory 子命令。
//
// 子命令：
//   - /memory              → list（默认列出所有记忆）
//   - /memory list         → 列出所有 L3 记忆（按重要性降序）
//   - /memory add <内容>    → 手动添加一条记忆（type=user, importance=3）
//   - /memory rm <name>    → 删除指定记忆
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
			r.ui.OnError(fmt.Errorf("用法: /memory add <内容>"))
			return
		}
		content := strings.Join(parts[2:], " ")
		r.addMemory(content)
	case "rm", "delete":
		if len(parts) < 3 {
			r.ui.OnError(fmt.Errorf("用法: /memory rm <name>"))
			return
		}
		name := parts[2]
		r.deleteMemory(name)
	default:
		r.ui.OnError(fmt.Errorf("用法: /memory [list|add|rm]"))
	}
}

// listMemories 列出三级 L3 记忆（全局 → 项目 → 会话，按重要性降序）。
func (r *Runner) listMemories() {
	// 辅助：列出单个 store 的记忆。
	listStore := func(title string, store *memory.MemoryStore) {
		if store == nil {
			return
		}
		entries, err := store.ListEntries()
		if err != nil {
			r.ui.OnError(fmt.Errorf("读取记忆失败: %v", err))
			return
		}
		if len(entries) == 0 {
			return
		}
		r.ui.OnMessage(fmt.Sprintf("🌐 %s（%d 条）:", title, len(entries)))
		for _, e := range entries {
			importanceIcon := strings.Repeat("⭐", e.Importance)
			r.ui.OnMessage(fmt.Sprintf("  [%s] %s", e.Type, e.Description))
			r.ui.OnMessage(fmt.Sprintf("    %s name=%s", importanceIcon, e.Name))
		}
	}

	listStore("全局记忆", r.globalMem)
	listStore("项目记忆", r.projectMem)
	listStore("会话记忆", r.memStore)

	if r.globalMem == nil && r.projectMem == nil {
		// 无全局/项目 store（未启用）时只显示会话记忆，保持旧行为。
		entries, err := r.memStore.ListEntries()
		if err != nil {
			return
		}
		if len(entries) == 0 {
			r.ui.OnMessage("  (暂无记忆)")
		}
	}
}

// addMemory 手动添加一条 L3 记忆。
// name 自动生成：manual-<内容前20字符的kebab-case>
func (r *Runner) addMemory(content string) {
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
		r.ui.OnError(fmt.Errorf("保存记忆失败: %v", err))
		return
	}
	r.ui.OnMessage(fmt.Sprintf("✅ 记忆已保存: %s", content))
}

// deleteMemory 删除一条 L3 记忆（按 name 匹配）。
func (r *Runner) deleteMemory(name string) {
	if err := r.memStore.DeleteEntry(name); err != nil {
		r.ui.OnError(fmt.Errorf("删除记忆失败: %v", err))
		return
	}
	r.ui.OnMessage(fmt.Sprintf("✅ 记忆已删除: %s", name))
}
