package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/session"
	"agentic/internal/tool"
	"agentic/internal/ui/text"
)

// newTestRunner 构造一个测试用 Runner（TextUI + 临时 sessions 目录）。
//
// llm 注入空客户端（&llm.OpenAIClient{}）：本测试只验证 RunOnce 的
// 空输入校验和 initTempSession 的目录行为，不触及 llm 字段——
// 空客户端仅提供零值 model，调用 Model() 返回空字符串，安全无副作用。
func newTestRunner(t *testing.T) *Runner {
	t.Helper()
	sessionsDir := t.TempDir()
	sessions, err := session.NewSessionManager(sessionsDir)
	if err != nil {
		t.Fatalf("NewSessionManager failed: %v", err)
	}

	// store 初始路径指向会话管理器默认目录（initTempSession 会重定向）。
	activeDir := sessions.ActiveSessionDir()
	history, err := memory.NewHistoryStore(activeDir)
	if err != nil {
		t.Fatalf("NewHistoryStore failed: %v", err)
	}
	summary, err := memory.NewSummaryStore(activeDir)
	if err != nil {
		t.Fatalf("NewSummaryStore failed: %v", err)
	}
	memStore, err := memory.NewMemoryStore(activeDir)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	events := memory.NewEventStore(activeDir)

	return &Runner{
		llm:       &llm.OpenAIClient{}, // 空客户端：仅本文件测试使用，不读字段，Model() 返回零值
		history:   history,
		summary:   summary,
		memStore:  memStore,
		events:    events,
		sessions:  sessions,
		ui:        text.NewTextUI(),
		tools:     tool.NewRegistry(),
	}
}

func TestRunOnce_EmptyInput(t *testing.T) {
	r := newTestRunner(t)

	_, err := r.RunOnce(context.Background(), "   ")
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !strings.Contains(err.Error(), "输入为空") {
		t.Errorf("error should mention empty input, got: %v", err)
	}
}

func TestInitTempSession_SetsPaths(t *testing.T) {
	r := newTestRunner(t)

	if err := r.initTempSession(); err != nil {
		t.Fatalf("initTempSession failed: %v", err)
	}

	if !r.isTemporary {
		t.Error("isTemporary should be true after initTempSession")
	}
	if r.tempID == "" {
		t.Error("tempID should be set")
	}

	// 临时会话目录（预期惰性创建，initTempSession 不显式 MkdirAll）。
	tempDir := r.sessions.SessionDir(r.tempID)

	// events 路径应指向临时目录（其余 store 共用同一 SetPath 代码路径）。
	eventsPath := filepath.Dir(r.events.Path())
	if eventsPath != tempDir {
		t.Errorf("events path should be in tempDir, got %q want %q", eventsPath, tempDir)
	}
}

func TestTextUI_PermissionAutoApprove(t *testing.T) {
	ui := text.NewTextUI()

	// TextUI.ConfirmPermission 不读 inputForward，直接返回 true。
	approved, err := ui.ConfirmPermission("shell", `{"command":"ls"}`, "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approved {
		t.Error("TextUI should auto-approve permissions")
	}
}
