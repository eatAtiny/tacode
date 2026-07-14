package agent

import (
	"context"
	"fmt"
	"strings"

	"agentic/internal/memory"
	"agentic/internal/prompt"
)

// ──────────────────────────────────────────────────────────
// 记忆管理函数
//
// 调用链（每轮对话完成后，在后台 goroutine 中执行）：
//
//	Runner.Run() → 分支 B: 收到查询结果
//	  └─ go func() {
//	       └─ history.Append()          ← 步骤 7a: 保存 L1 原始对话
//	       └─ extractMemory()           ← 步骤 7b: 提取 L2 摘要 + L3 记忆
//	            └─ extractor.Extract()   ← 一次 LLM 调用，同时产出摘要和记忆操作
//	            └─ summary.Append()      ← 保存 L2 摘要
//	            └─ memStore.SaveEntry()  ← 保存/更新 L3 记忆
//	            └─ memStore.DeleteEntry()← 删除 L3 记忆
//	       └─ ensurePersisted()         ← 步骤 8: 临时会话落盘
//	     }()
// ──────────────────────────────────────────────────────────

// extractMemory 调用 LLM 提取摘要和结构化记忆。
//
// 流程（每次对话后执行一次）：
//   1. 调用 extractor.Extract(userInput, assistantOutput)
//      → LLM 分析对话，返回 ExtractionResult{Summary, Memories[]}
//   2. 保存 L2 摘要（追加到 summaries.jsonl）
//   3. 处理 L3 记忆操作：
//      - create/update → memStore.SaveEntry()（写入 .md 文件 + 更新 MEMORY.md）
//      - delete → memStore.DeleteEntry()（删除 .md 文件 + 更新 MEMORY.md）
//
// 这是后台操作，不阻塞主循环。失败时通过 UI.OnMessage 提示警告，
// 不会中断 Agent 运行。
func (r *Runner) extractMemory(ctx context.Context, round int, userInput, assistantOutput string) {
	result, err := r.extractor.Extract(ctx, userInput, assistantOutput)
	if err != nil {
		r.ui.OnMessage(fmt.Sprintf("⚠️ 记忆提取失败: %v", err))
		return
	}

	// ── 步骤 7b-1: 保存 L2 摘要 ──
	if result.Summary != "" {
		if err := r.summary.Append(round, result.Summary); err != nil {
			r.ui.OnMessage(fmt.Sprintf("⚠️ 保存摘要失败: %v", err))
		}
	}

	// ── 步骤 7b-2: 处理 L3 记忆操作 ──
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
				r.ui.OnMessage(fmt.Sprintf("⚠️ 保存记忆失败: %v", err))
			}
		case "delete":
			if err := r.memStore.DeleteEntry(action.Name); err != nil {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 删除记忆失败: %v", err))
			} else {
				r.ui.OnMessage(fmt.Sprintf("🗑️ 记忆已删除: %s", action.Name))
			}
		}
	}
}

// handleContextCommand 展示当前提示词各部分的大小组成（v2 动静分离结构）。
//
// 输出 3 段消息结构 + 会话上下文快照：
//   messages[0] system  = 静态段(全局可缓存) + 动态段(会话稳定)
//   messages[1] user    = <system-reminder> 记忆上下文
//   messages[2] user    = 轮次 + 用户任务
//   context.json        = 会话上下文快照（跨查询复用）
func (r *Runner) handleContextCommand() {
	// ── messages[0] system: 静态段 ──
	staticPrompt := prompt.BuildReActStaticPrompt("")
	staticTokens := memory.EstimateTokens(staticPrompt)

	// 工具列表（嵌入在静态段中）。
	toolDescs := r.tools.Descriptions()
	toolDescsTokens := memory.EstimateTokens(toolDescs)

	// ── messages[0] system: 动态段（工具使用指南） ──
	var guides []prompt.ToolGuide
	for _, name := range r.tools.Names() {
		t := r.tools.Get(name)
		if t == nil {
			continue
		}
		guides = append(guides, prompt.ToolGuide{
			Name:  t.Name(),
			Guide: t.PromptGuide(),
		})
	}
	dynamicPrompt := prompt.BuildReActDynamicPrompt(guides)
	dynamicTokens := memory.EstimateTokens(dynamicPrompt)

	fullSystem := prompt.BuildReActSystemPrompt(toolDescs, guides)
	systemTokens := memory.EstimateTokens(fullSystem)

	// ── messages[1] user: system-reminder (记忆上下文) ──
	contextDigest, _ := r.retriever.BuildContext("")
	contextTokens := memory.EstimateTokens(contextDigest)

	index := r.memStore.LoadIndex()
	indexTokens := memory.EstimateTokens(index)

	memories, _ := r.memStore.FormatForPrompt(10, 2)
	memoriesTokens := memory.EstimateTokens(memories)

	summaries, _ := r.summary.FormatRecent(10)
	summariesTokens := memory.EstimateTokens(summaries)

	// system-reminder 包装开销（XML 标签）。
	wrapperTokens := memory.EstimateTokens(prompt.BuildSystemReminder("")) + 1

	// ── messages[2] user: 用户任务 ──
	taskTokens := memory.EstimateTokens(prompt.BuildUserTask(0, "")) + 10

	// ── 会话上下文快照 (context.json) ──
	sessCtx, _ := r.contextStore.Load()
	var ctxMsgCount, ctxTokens int
	var ctxUpdated string
	if sessCtx != nil {
		ctxMsgCount = len(sessCtx.Messages)
		ctxTokens = memory.EstimateTokens(fmt.Sprintf("%v", sessCtx.Messages))
		ctxUpdated = sessCtx.UpdatedAt.Format("15:04:05")
	}

	// ── 合计 ──
	contextLimit := r.llm.ContextLimit()
	totalTokens := systemTokens + contextTokens + taskTokens + ctxTokens
	pct := float64(totalTokens) / float64(contextLimit) * 100

	r.ui.OnMessage(fmt.Sprintf(
		"═══════════════════════════════════════\n"+
			"  提示词组成（3 段消息结构）\n"+
			"═══════════════════════════════════════\n\n"+
			"▸ messages[0] system  %d tokens\n"+
			"  · 静态段 (全局可缓存): 框架 + 工具列表 + 注意事项  ~%d tokens\n"+
			"    其中工具 Schema                    ~%d tokens\n"+
			"  · 动态段 (会话稳定): 工具使用指南                ~%d tokens\n\n"+
			"▸ messages[1] user (system-reminder)  %d tokens\n"+
			"  · L3 记忆索引 (MEMORY.md)           ~%d tokens\n"+
			"  · L3 重要记忆                       ~%d tokens\n"+
			"  · L2 最近摘要                       ~%d tokens\n"+
			"  · XML 标签开销                       ~%d tokens\n\n"+
			"▸ messages[2] user (task)             ~%d tokens\n"+
			"  · 轮次 + 用户任务（输入时确定）       ~%d tokens\n\n"+
			"▸ 会话上下文 (context.json)            ~%d tokens\n"+
			"  · 消息数: %d, 最后更新: %s\n\n"+
			"───────────────────────────────────────\n"+
			"  合计预估   ~%d / %d tokens (%.1f%%)\n"+
			"═══════════════════════════════════════",
		systemTokens,
		staticTokens+toolDescsTokens,
		toolDescsTokens,
		dynamicTokens,
		contextTokens+wrapperTokens,
		indexTokens,
		memoriesTokens,
		summariesTokens,
		wrapperTokens,
		taskTokens,
		taskTokens,
		ctxTokens, ctxMsgCount, ctxUpdated,
		totalTokens, contextLimit, pct,
	))
}

// handleCompress 手动触发摘要压缩（/compress 命令）。
//
// 流程：
//  1. 调用 Retriever.CompressSummaries()
//  2. LLM 合并旧摘要 → 保留最近 3 条 + 1 条综合摘要
//  3. 显示压缩后的摘要数量
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

// listMemories 列出所有 L3 记忆（按重要性降序，带 ⭐ 重要度图标）。
func (r *Runner) listMemories() {
	entries, err := r.memStore.ListEntries()
	if err != nil {
		r.ui.OnError(fmt.Errorf("读取记忆失败: %v", err))
		return
	}
	if len(entries) == 0 {
		r.ui.OnMessage("  (暂无记忆)")
		return
	}

	r.ui.OnMessage(fmt.Sprintf("🧠 共 %d 条记忆:", len(entries)))
	for _, e := range entries {
		importanceIcon := strings.Repeat("⭐", e.Importance)
		r.ui.OnMessage(fmt.Sprintf("  [%s] %s", e.Type, e.Description))
		r.ui.OnMessage(fmt.Sprintf("    %s name=%s", importanceIcon, e.Name))
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
