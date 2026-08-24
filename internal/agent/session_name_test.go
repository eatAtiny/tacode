package agent

import (
	"os"
	"strings"
	"testing"

	"agentic/internal/memory"
	"agentic/internal/session"
)

// ──────────────────────────────────────────────────────────
// 首条对话自动命名会话
//
// ensurePersisted 在首次对话落盘时，用首条用户输入生成会话名
// （而非固定"新会话"）。deriveSessionName 负责清洗：
//   - 去换行、压缩连续空白
//   - 截断到 maxSessionNameLen runes（加省略号）
//   - 空输入/纯空白 → 兜底"新会话"
// ──────────────────────────────────────────────────────────

func TestDeriveSessionName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"normal", "帮我看看这个代码怎么优化", "帮我看看这个代码怎么优化"},
		{"empty", "", "新会话"},
		{"whitespace", "   \n\t  ", "新会话"},
		{"newline collapsed", "第一行\n第二行", "第一行 第二行"},
		{"multi-space collapsed", "a    b\t\tc", "a b c"},
		{"truncated", strings.Repeat("很长的输入内容", 20), string([]rune(strings.Repeat("很长的输入内容", 20))[:maxSessionNameLen]) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveSessionName(tt.in); got != tt.want {
				t.Errorf("deriveSessionName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEnsurePersisted_NamesFromFirstInput(t *testing.T) {
	sessionsDir := t.TempDir()
	sessions, err := session.NewSessionManager(sessionsDir)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟临时会话（Runner.initTempSession 后的状态）。
	tempID, _ := session.GenerateID()
	tempDir := sessions.SessionDir(tempID)
	os.MkdirAll(tempDir, 0o755)

	history, _ := memory.NewHistoryStore(tempDir)
	summary, _ := memory.NewSummaryStore(tempDir)
	memStore, _ := memory.NewMemoryStore(tempDir)
	events := memory.NewEventStore(tempDir)

	r := &Runner{
		history:  history,
		summary:  summary,
		memStore: memStore,
		events:   events,
		sessions: sessions,
		isTemporary: true,
		tempID:   tempID,
	}

	// 首条输入：多行 + 长内容，应被清洗并截断。
	firstInput := "帮我看看这个代码\n怎么优化 性能\n" + strings.Repeat("超长内容", 10)
	if err := r.ensurePersisted(firstInput); err != nil {
		t.Fatalf("ensurePersisted failed: %v", err)
	}

	// 会话应已创建，名字来自首条输入（清洗 + 截断）。
	activeID := sessions.ActiveID()
	meta := sessions.FindMeta(activeID)
	if meta == nil {
		t.Fatal("session should exist after ensurePersisted")
	}
	if meta.Name == "新会话" {
		t.Error("session name should be derived from first input, not default")
	}
	if !strings.Contains(meta.Name, "帮我看看这个代码") {
		t.Errorf("session name should contain first input content, got %q", meta.Name)
	}
	if len([]rune(meta.Name)) > maxSessionNameLen+1 { // +1 省略号
		t.Errorf("session name too long: %d runes", len([]rune(meta.Name)))
	}
	// 换行应被压缩为空格。
	if strings.Contains(meta.Name, "\n") {
		t.Errorf("session name should not contain newline, got %q", meta.Name)
	}
}
