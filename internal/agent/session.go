package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"agentic/internal/memory"
	"agentic/internal/session"
)

// ──────────────────────────────────────────────────────────
// 会话管理函数
//
// 会话生命周期：
//
//	启动 → 临时会话（不在 manifest 中）
//	  ├─ /new     → 清理临时目录，重置临时状态
//	  ├─ /list    → 选择已持久化会话 → Switch → isTemporary=false
//	  ├─ /switch  → 切换到已持久化会话 → isTemporary=false
//	  └─ 首次对话 → ensurePersisted() → 创建正式会话 → 移动文件 → isTemporary=false
//
// 错误恢复：
//	每次启动时 cleanOrphanTempDirs() 清理上次异常退出遗留的临时目录。
// ──────────────────────────────────────────────────────────

// maxSessionNameLen 会话名最大长度（runes），超出截断加省略号。
const maxSessionNameLen = 40

// deriveSessionName 从首条用户输入生成会话名。
//
// 规则：
//   - 空输入 / 纯空白 → 兜底 "新会话"
//   - 换行替换为空格、连续空白压缩为单个空格
//   - 超过 maxSessionNameLen runes 截断并加省略号 "…"
func deriveSessionName(input string) string {
	cleaned := strings.Join(strings.Fields(input), " ")
	if cleaned == "" {
		return "新会话"
	}
	runes := []rune(cleaned)
	if len(runes) > maxSessionNameLen {
		return string(runes[:maxSessionNameLen]) + "…"
	}
	return cleaned
}

// switchSession 切换会话时重新初始化所有 store 路径。
//
// 所有 memory store 都有 SetPath 方法，切换会话时只需修改底层文件路径，
// 不需要重新创建 store 实例。
//
// 调用时机：
//   - /switch 命令
//   - /list 选中会话
//   - ensurePersisted()（临时 → 正式）
func (r *Runner) switchSession() {
	activeDir := r.sessions.ActiveSessionDir()
	os.MkdirAll(activeDir, 0o755)
	r.history.SetPath(activeDir)
	r.summary.SetPath(activeDir)
	r.memStore.SetPath(activeDir)
	r.events.SetPath(activeDir)
	// 切换会话：清空跨轮累积的对话消息与记忆 preamble 缓存。
	// 下一轮查询将基于新会话的记忆重新构建（preamble + 记忆兜底）。
	r.messages = nil
	r.memoryPreamble = ""
	r.syncCompactorPaths(activeDir)
}

// syncCompactorPaths 同步 Compactor 的 transcript/tool-results 目录到当前会话。
//
// Compactor 目录在构造时绑定一次，会话切换后必须跟随更新，
// 否则归档/转存会累积到启动时的旧会话目录（路径绑定 bug）。
func (r *Runner) syncCompactorPaths(dir string) {
	if r.compactor != nil {
		r.compactor.SetPaths(
			filepath.Join(dir, "transcripts"),
			filepath.Join(dir, "tool-results"),
		)
	}
}

// printSessionHistory 读取并展示指定会话的历史记录。
//
// 按轮次分组展示：
//   - EventUser: "You> ..."（截断到 60 runes）
//   - EventAssistant: "Agent> ..."（取首行，截断到 80 runes）
//   - EventToolUse: "🔧 tool(args)"（参数截断到 60 字符）
func (r *Runner) printSessionHistory() {
	events, err := r.events.ReadAll()
	if err != nil || len(events) == 0 {
		r.ui.OnMessage("  (无历史记录)")
		return
	}

	r.ui.OnMessage(fmt.Sprintf("  📜 共 %d 条事件:", len(events)))
	currentRound := 0
	for _, e := range events {
		switch e.Type {
		case memory.EventUser:
			if e.Round != currentRound {
				currentRound = e.Round
				r.ui.OnMessage(fmt.Sprintf("  Round %d:", e.Round))
			}
			userLine := e.Content
			if len([]rune(userLine)) > 60 {
				userLine = string([]rune(userLine)[:60]) + "..."
			}
			r.ui.OnMessage(fmt.Sprintf("    You> %s", userLine))
		case memory.EventAssistant:
			assistantLine := e.Content
			if idx := strings.IndexByte(assistantLine, '\n'); idx >= 0 {
				assistantLine = assistantLine[:idx]
			}
			if len([]rune(assistantLine)) > 80 {
				assistantLine = string([]rune(assistantLine)[:80]) + "..."
			}
			r.ui.OnMessage(fmt.Sprintf("    Agent> %s", assistantLine))
		case memory.EventToolUse:
			for _, tc := range e.ToolCalls {
				r.ui.OnMessage(fmt.Sprintf("    🔧 %s(%s)", tc.Name, trimArgs(tc.Arguments)))
			}
		}
	}
}

// handleSessionCommand 处理 / 开头的会话管理命令。
//
// 命令路由：
//   /new [name]    → 创建新会话（临时模式：重置临时状态；持久化模式：Create + Switch）
//   /list          → 交互式会话选择器（Bubble Tea）
//   /switch <id>   → 切换到指定会话
//   /delete <id>   → 删除会话（不能删除当前活跃的）
//   /rename <name> → 重命名当前会话
//   /current       → 显示当前会话信息
//   /compress      → 手动压缩 L2 摘要
//   /reload        → 重新加载项目指令（AGENTS.md）
//   /memory [...]  → L3 记忆管理（list/add/rm）
//
// 返回值：新的轮次号（切换会话时重置为 1），是否已处理。
func (r *Runner) handleSessionCommand(input string) (int, bool) {
	// 记录系统命令事件（/new, /rename 等也记录到事件日志）。
	r.events.Append(memory.Event{
		Type:    memory.EventSystem,
		Command: input,
		Content: input,
	})

	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	// ── /new [name] ──
	// 临时模式：清理临时目录，重置临时状态（不立即创建正式会话）。
	// 持久化模式：创建正式会话，切换过去。
	case "/new":
		name := ""
		if len(parts) > 1 {
			name = strings.Join(parts[1:], " ")
		}
		if r.isTemporary {
			// 临时模式：清理可能已创建的临时目录，重置临时状态。
			// 不立即创建正式会话，延迟到首次对话后 ensurePersisted。
			os.RemoveAll(r.sessions.SessionDir(r.tempID))
			newTempID, err := session.GenerateID()
			if err != nil {
				r.ui.OnError(fmt.Errorf("生成会话 ID 失败: %v", err))
				return 0, true
			}
			r.tempID = newTempID
			tempDir := r.sessions.SessionDir(newTempID)
			r.history.SetPath(tempDir)
			r.summary.SetPath(tempDir)
			r.memStore.SetPath(tempDir)
			r.events.SetPath(tempDir)
			r.messages = nil // 新会话：清空跨轮累积
			r.syncCompactorPaths(tempDir)
			// 记住用户指定的会话名，ensurePersisted 时使用。
			r.pendingSessionName = name
			displayName := "新会话"
			if name != "" {
				displayName = name
			}
			r.ui.OnMessage(fmt.Sprintf("✅ 已切换到新会话: %s（对话后自动保存）", displayName))
			return 1, true
		}
		id, err := r.sessions.Create(name)
		if err != nil {
			r.ui.OnError(fmt.Errorf("创建会话失败: %v", err))
			return 0, true
		}
		r.switchSession()
		meta := r.sessions.FindMeta(id)
		displayName := id
		if meta != nil {
			displayName = meta.Name
		}
		r.ui.OnMessage(fmt.Sprintf("✅ 已创建并切换到新会话: %s", displayName))
		return 1, true

	// ── /list ──
	// 启动 Bubble Tea 会话选择器，阻塞等待用户选择。
	case "/list":
		return r.handleListCommand()

	// ── /switch <id> ──
	// 按 ID 前缀匹配切换会话。切换后显示历史记录。
	case "/switch":
		if len(parts) < 2 {
			r.ui.OnError(fmt.Errorf("用法: /switch <会话ID>"))
			return 0, true
		}
		id := parts[1]
		if err := r.sessions.Switch(id); err != nil {
			r.ui.OnError(fmt.Errorf("切换失败: %v", err))
			return 0, true
		}
		r.isTemporary = false // 切换到已持久化会话
		r.switchSession()
		meta := r.sessions.FindMeta(r.sessions.ActiveID())
		displayName := r.sessions.ActiveID()
		if meta != nil {
			displayName = meta.Name
		}
		r.ui.OnMessage(fmt.Sprintf("✅ 已切换到会话: %s", displayName))
		r.printSessionHistory()
		return 1, true

	// ── /delete <id> ──
	// 不能删除当前活跃的会话。
	case "/delete":
		if len(parts) < 2 {
			r.ui.OnError(fmt.Errorf("用法: /delete <会话ID>"))
			return 0, true
		}
		id := parts[1]
		if err := r.sessions.Delete(id); err != nil {
			r.ui.OnError(fmt.Errorf("删除失败: %v", err))
			return 0, true
		}
		r.ui.OnMessage("✅ 会话已删除")
		return 0, true

	// ── /rename <name> ──
	case "/rename":
		if len(parts) < 2 {
			r.ui.OnError(fmt.Errorf("用法: /rename <新名称>"))
			return 0, true
		}
		name := strings.Join(parts[1:], " ")
		activeID := r.sessions.ActiveID()
		if err := r.sessions.Rename(activeID, name); err != nil {
			r.ui.OnError(fmt.Errorf("重命名失败: %v", err))
			return 0, true
		}
		r.ui.OnMessage(fmt.Sprintf("✅ 会话已重命名为: %s", name))
		return 0, true

	// ── /current ──
	// 临时会话显示 "(临时会话，对话后自动保存)"。
	case "/current":
		if r.isTemporary {
			r.ui.OnMessage("当前会话: (临时会话，对话后自动保存)")
		} else {
			activeID := r.sessions.ActiveID()
			meta := r.sessions.FindMeta(activeID)
			if meta != nil {
				r.ui.OnMessage(fmt.Sprintf("当前会话: %s (%s)", meta.Name, meta.ID))
			} else {
				r.ui.OnMessage(fmt.Sprintf("当前会话: %s", activeID))
			}
		}
		return 0, true

	// ── /balance ──
	// 手动查询余额；成功后开启每轮结束展示。
	case "/balance":
		r.handleBalanceCommand()
		return 0, true

	// ── /compress ──
	// 手动触发 L2 摘要压缩（LLM 合并旧摘要）。
	case "/compress":
		r.handleCompress()
		return 0, true

	// ── /reload ──
	// 重新加载项目指令（AGENTS.md），用于指令文件变更后手动刷新。
	// 先清空再加载：用户显式触发刷新，加载失败时保留空缓存（而非旧内容），
	// 失败信息已通过 OnError 展示。
	case "/reload":
		r.retriever.ClearProjectInstructions()
		if err := r.retriever.LoadProjectInstructions(); err != nil {
			r.ui.OnError(fmt.Errorf("重新加载项目指令失败: %v", err))
			return 0, true
		}
		r.ui.OnMessage("✅ 已重新加载项目指令")
		return 0, true

	// ── /memory [list|add|rm] ──
	case "/memory":
		r.handleMemoryCommand(parts)
		return 0, true

	default:
		return 0, false
	}
}

// ensurePersisted 将临时会话持久化到 manifest。
//
// 流程（首次对话完成后调用）：
//   1. 记录临时目录路径
//   2. 调用 sessions.Create() 创建正式会话（加入 manifest + 设为 Active）
//      会话名：用户通过 /new <name> 指定的名字优先；
//      否则用首条用户输入（firstInput）自动生成；再否则兜底 "新会话"。
//   3. 将临时目录下的文件移动到正式目录：
//      - events.jsonl（全量事件日志）
//      - history.jsonl（L1 原始对话）
//      - summaries.jsonl（L2 摘要）
//      - memory/ 目录（L3 结构化记忆）
//   4. 清理临时目录
//   5. 更新所有 store 的路径指向正式目录
//   6. 设置 isTemporary = false
//
// 错误处理：单个文件移动失败不中断（仅输出警告），尽量迁移所有文件。
func (r *Runner) ensurePersisted(firstInput string) error {
	// ── 步骤 1: 记录临时目录路径 ──
	tempDir := r.sessions.SessionDir(r.tempID)

	// ── 步骤 2: 创建正式会话 ──
	sessionName := r.pendingSessionName
	if sessionName == "" {
		sessionName = deriveSessionName(firstInput)
	}
	r.pendingSessionName = ""
	realID, err := r.sessions.Create(sessionName)
	if err != nil {
		return err
	}
	realDir := r.sessions.SessionDir(realID)

	// ── 步骤 3: 迁移文件 ──
	// events.jsonl
	tempEvents := tempDir + "/events.jsonl"
	realEvents := realDir + "/events.jsonl"
	if _, err := os.Stat(tempEvents); err == nil {
		os.MkdirAll(realDir, 0o755)
		if err := os.Rename(tempEvents, realEvents); err != nil {
			if !os.IsNotExist(err) {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 临时事件文件迁移失败: %v", err))
			}
		}
	}
	// history.jsonl
	tempHistory := tempDir + "/history.jsonl"
	realHistory := realDir + "/history.jsonl"
	if _, err := os.Stat(tempHistory); err == nil {
		os.MkdirAll(realDir, 0o755)
		if err := os.Rename(tempHistory, realHistory); err != nil {
			if !os.IsNotExist(err) {
				r.ui.OnMessage(fmt.Sprintf("⚠️ 临时历史文件迁移失败: %v", err))
			}
		}
	}
	// summaries.jsonl
	tempSummary := tempDir + "/summaries.jsonl"
	realSummary := realDir + "/summaries.jsonl"
	if _, err := os.Stat(tempSummary); err == nil {
		os.MkdirAll(realDir, 0o755)
		os.Rename(tempSummary, realSummary)
	}
	// memory/ 目录
	tempMemory := tempDir + "/memory"
	realMemory := realDir + "/memory"
	if _, err := os.Stat(tempMemory); err == nil {
		os.MkdirAll(realDir, 0o755)
		os.Rename(tempMemory, realMemory)
	}

	// ── 步骤 4: 清理临时目录 ──
	os.RemoveAll(tempDir)

	// ── 步骤 5: 更新 store 路径 ──
	r.history.SetPath(realDir)
	r.summary.SetPath(realDir)
	r.memStore.SetPath(realDir)
	r.events.SetPath(realDir)
	r.syncCompactorPaths(realDir)

	// ── 步骤 6: 标记为非临时 ──
	r.isTemporary = false
	r.tempID = ""
	return nil
}

// cleanOrphanTempDirs 清理不在 manifest 中的孤立会话目录。
//
// 调用时机：每次启动时。
// 场景：上次异常退出（kill -9、崩溃等）时临时目录未被清理。
// 检测方式：遍历 sessions 目录，删除不在 manifest 中的子目录。
func (r *Runner) cleanOrphanTempDirs() {
	entries, err := os.ReadDir(r.sessions.Dir())
	if err != nil {
		return
	}
	known := make(map[string]bool)
	for _, s := range r.sessions.List() {
		known[s.ID] = true
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !known[e.Name()] {
			os.RemoveAll(filepath.Join(r.sessions.Dir(), e.Name()))
		}
	}
}
