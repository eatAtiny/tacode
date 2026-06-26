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
// ──────────────────────────────────────────────────────────

// switchSession 切换会话时重新初始化所有 store 路径。
func (r *Runner) switchSession() {
	activeDir := r.sessions.ActiveSessionDir()
	os.MkdirAll(activeDir, 0o755)
	r.history.SetPath(activeDir)
	r.summary.SetPath(activeDir)
	r.memStore.SetPath(activeDir)
	r.events.SetPath(activeDir)
}

// printSessionHistory 读取并展示指定会话的历史记录。
func (r *Runner) printSessionHistory() {
	events, err := r.events.ReadAll()
	if err != nil || len(events) == 0 {
		fmt.Printf("\n%s\n", mutedStyle.Render("  (无历史记录)"))
		return
	}

	// 按轮次分组展示 user 和 assistant 事件。
	fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("  📜 共 %d 条事件:", len(events))))
	currentRound := 0
	for _, e := range events {
		switch e.Type {
		case memory.EventUser:
			if e.Round != currentRound {
				currentRound = e.Round
				fmt.Printf("  %s\n", mutedStyle.Render(fmt.Sprintf("Round %d:", e.Round)))
			}
			userLine := e.Content
			if len([]rune(userLine)) > 60 {
				userLine = string([]rune(userLine)[:60]) + "..."
			}
			fmt.Printf("    %s\n", promptStyle.Render("You> ")+userLine)
		case memory.EventAssistant:
			assistantLine := e.Content
			if idx := strings.IndexByte(assistantLine, '\n'); idx >= 0 {
				assistantLine = assistantLine[:idx]
			}
			if len([]rune(assistantLine)) > 80 {
				assistantLine = string([]rune(assistantLine)[:80]) + "..."
			}
			fmt.Printf("    %s\n", answerLabelStyle.Render("Agent> ")+assistantLine)
		case memory.EventToolUse:
			for _, tc := range e.ToolCalls {
				fmt.Printf("    %s\n", mutedStyle.Render(fmt.Sprintf("🔧 %s(%s)", tc.Name, trimArgs(tc.Arguments))))
			}
		}
	}
}

// handleSessionCommand 处理 / 开头的会话管理命令。
// 返回值：新的轮次号（切换会话时重置为 1），是否已处理。
func (r *Runner) handleSessionCommand(input string) (int, bool) {
	// 记录系统命令事件。
	r.events.Append(memory.Event{
		Type:    memory.EventSystem,
		Command: input,
		Content: input,
	})

	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	switch cmd {
	case "/new":
		name := ""
		if len(parts) > 1 {
			name = strings.Join(parts[1:], " ")
		}
		if r.isTemporary {
			// 临时模式下 /new：清理可能已创建的临时目录，重置临时状态。
			// 不立即创建正式会话，延迟到首次对话后 ensurePersisted。
			os.RemoveAll(r.sessions.SessionDir(r.tempID))
			newTempID, err := session.GenerateID()
			if err != nil {
				fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("生成会话 ID 失败: %v", err)))
				return 0, true
			}
			r.tempID = newTempID
			tempDir := r.sessions.SessionDir(newTempID)
			r.history.SetPath(tempDir)
			r.summary.SetPath(tempDir)
			r.memStore.SetPath(tempDir)
			r.events.SetPath(tempDir)
			// 记住用户指定的会话名，ensurePersisted 时使用。
			r.pendingSessionName = name
			displayName := "新会话"
			if name != "" {
				displayName = name
			}
			fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已切换到新会话: %s（对话后自动保存）", displayName)))
			return 1, true
		}
		id, err := r.sessions.Create(name)
		if err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("创建会话失败: %v", err)))
			return 0, true
		}
		r.switchSession()
		meta := r.sessions.FindMeta(id)
		displayName := id
		if meta != nil {
			displayName = meta.Name
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已创建并切换到新会话: %s", displayName)))
		return 1, true

	case "/list":
		selected, err := session.RunSessionPicker(r.sessions.List(), r.sessions.ActiveID())
		if err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("选择器错误: %v", err)))
			return 0, true
		}
		if selected == "" {
			// 用户取消
			return 0, true
		}
		// 用户选中了一个会话，执行切换
		if err := r.sessions.Switch(selected); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("切换失败: %v", err)))
			return 0, true
		}
		r.isTemporary = false // 切换到已持久化会话
		r.switchSession()
		meta := r.sessions.FindMeta(r.sessions.ActiveID())
		displayName := r.sessions.ActiveID()
		if meta != nil {
			displayName = meta.Name
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已切换到会话: %s", displayName)))
		r.printSessionHistory()
		return 1, true

	case "/switch":
		if len(parts) < 2 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /switch <会话ID>"))
			return 0, true
		}
		id := parts[1]
		if err := r.sessions.Switch(id); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("切换失败: %v", err)))
			return 0, true
		}
		r.isTemporary = false // 切换到已持久化会话
		r.switchSession()
		meta := r.sessions.FindMeta(r.sessions.ActiveID())
		displayName := r.sessions.ActiveID()
		if meta != nil {
			displayName = meta.Name
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 已切换到会话: %s", displayName)))
		r.printSessionHistory()
		return 1, true

	case "/delete":
		if len(parts) < 2 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /delete <会话ID>"))
			return 0, true
		}
		id := parts[1]
		if err := r.sessions.Delete(id); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("删除失败: %v", err)))
			return 0, true
		}
		fmt.Printf("\n%s\n", successStyle.Render("✅ 会话已删除"))
		return 0, true

	case "/rename":
		if len(parts) < 2 {
			fmt.Printf("\n%s\n", errorStyle.Render("用法: /rename <新名称>"))
			return 0, true
		}
		name := strings.Join(parts[1:], " ")
		activeID := r.sessions.ActiveID()
		if err := r.sessions.Rename(activeID, name); err != nil {
			fmt.Printf("\n%s\n", errorStyle.Render(fmt.Sprintf("重命名失败: %v", err)))
			return 0, true
		}
		fmt.Printf("\n%s\n", successStyle.Render(fmt.Sprintf("✅ 会话已重命名为: %s", name)))
		return 0, true

	case "/current":
		if r.isTemporary {
			fmt.Printf("\n%s\n", mutedStyle.Render("当前会话: (临时会话，对话后自动保存)"))
		} else {
			activeID := r.sessions.ActiveID()
			meta := r.sessions.FindMeta(activeID)
			if meta != nil {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("当前会话: %s (%s)", meta.Name, meta.ID)))
			} else {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("当前会话: %s", activeID)))
			}
		}
		return 0, true

	case "/compress":
		r.handleCompress()
		return 0, true

	case "/memory":
		r.handleMemoryCommand(parts)
		return 0, true

	default:
		return 0, false
	}
}

// ensurePersisted 将临时会话持久化到 manifest。
// 首次对话完成后调用：创建正式会话，将临时文件移动到正式目录。
func (r *Runner) ensurePersisted() error {
	// 记住临时目录路径。
	tempDir := r.sessions.SessionDir(r.tempID)

	// 创建正式会话（会自动设为 Active）。
	sessionName := r.pendingSessionName
	if sessionName == "" {
		sessionName = "新会话"
	}
	r.pendingSessionName = ""
	realID, err := r.sessions.Create(sessionName)
	if err != nil {
		return err
	}
	realDir := r.sessions.SessionDir(realID)

	// 将临时目录下的文件移动到正式目录。
	// events.jsonl
	tempEvents := tempDir + "/events.jsonl"
	realEvents := realDir + "/events.jsonl"
	if _, err := os.Stat(tempEvents); err == nil {
		os.MkdirAll(realDir, 0o755)
		if err := os.Rename(tempEvents, realEvents); err != nil {
			if !os.IsNotExist(err) {
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 临时事件文件迁移失败: %v", err)))
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
				fmt.Printf("\n%s\n", mutedStyle.Render(fmt.Sprintf("⚠️ 临时历史文件迁移失败: %v", err)))
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

	// 清理临时目录。
	os.RemoveAll(tempDir)

	// 更新所有 store 的路径。
	r.history.SetPath(realDir)
	r.summary.SetPath(realDir)
	r.memStore.SetPath(realDir)
	r.events.SetPath(realDir)
	r.isTemporary = false
	r.tempID = ""
	fmt.Printf("\n%s\n", mutedStyle.Render("💾 会话已保存"))
	return nil
}

// cleanOrphanTempDirs 清理不在 manifest 中的孤立会话目录。
// 上次异常退出时临时目录可能未被清理。
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
