package agent

import (
	"strings"
	"testing"

	"tacode/internal/memory"
	"tacode/internal/ui/text"
)

// ──────────────────────────────────────────────────────────
// /memory 命令的三级展示
//
// listMemories 应依次展示：全局记忆 → 项目记忆 → 会话记忆。
// 用 TextUI 捕获 OnMessage 输出验证。
// ──────────────────────────────────────────────────────────

// captureUI 包装 TextUI，收集 OnMessage 输出。
type captureUI struct {
	*text.TextUI
	msgs []string
}

func newCaptureUI() *captureUI {
	return &captureUI{TextUI: text.NewTextUI()}
}

func (c *captureUI) OnMessage(msg string) {
	c.msgs = append(c.msgs, msg)
}

func TestListMemories_ShowsThreeTiers(t *testing.T) {
	ui := newCaptureUI()

	// 三个 store 各放一条记忆。
	globalMem := memory.NewMemoryStore(t.TempDir())
	_ = globalMem.SaveEntry(memory.MemoryEntry{Name: "g1", Description: "用户偏好", Type: "user", Importance: 4, Content: "x"})
	projectMem := memory.NewMemoryStore(t.TempDir())
	_ = projectMem.SaveEntry(memory.MemoryEntry{Name: "p1", Description: "项目约定", Type: "project", Importance: 3, Content: "y"})
	sessionMem := memory.NewMemoryStore(t.TempDir())
	_ = sessionMem.SaveEntry(memory.MemoryEntry{Name: "s1", Description: "会话细节", Type: "feedback", Importance: 2, Content: "z"})

	r := &Runner{
		globalMem:  globalMem,
		projectMem: projectMem,
		memStore:   sessionMem,
		ui:         ui,
	}

	r.listMemories()

	joined := strings.Join(ui.msgs, "\n")
	if !strings.Contains(joined, "全局记忆") {
		t.Errorf("should list global memory, got:\n%s", joined)
	}
	if !strings.Contains(joined, "项目记忆") {
		t.Errorf("should list project memory, got:\n%s", joined)
	}
	if !strings.Contains(joined, "会话记忆") {
		t.Errorf("should list session memory, got:\n%s", joined)
	}
	// 三级内容都应出现。
	if !strings.Contains(joined, "用户偏好") || !strings.Contains(joined, "项目约定") || !strings.Contains(joined, "会话细节") {
		t.Errorf("all three tiers content should appear, got:\n%s", joined)
	}
	// 顺序：全局在前，项目其次，会话最后。
	gi := strings.Index(joined, "全局记忆")
	pi := strings.Index(joined, "项目记忆")
	si := strings.Index(joined, "会话记忆")
	if !(gi < pi && pi < si) {
		t.Errorf("tier order should be global < project < session, got %d %d %d", gi, pi, si)
	}
}

func TestListMemories_NoStores_ShowsEmptyHint(t *testing.T) {
	ui := newCaptureUI()
	r := &Runner{
		memStore: memory.NewMemoryStore(t.TempDir()),
		ui:       ui,
	}
	r.listMemories()
	joined := strings.Join(ui.msgs, "\n")
	if !strings.Contains(joined, "(暂无记忆)") {
		t.Errorf("empty memory should show hint, got:\n%s", joined)
	}
}
