package agent

import (
	"path/filepath"
	"testing"

	"agentic/internal/session"
)

// TestSyncCompactorPaths_SessionSwitch 验证切换会话后 Compactor 路径跟随更新。
//
// 回归背景（路径绑定 bug）：Compactor 的 transcriptDir/toolResultsDir 在
// 构造时绑定启动会话目录，/new、/switch、/list、ensurePersisted 后不跟随，
// 导致归档/转存累积到旧会话目录。
func TestSyncCompactorPaths_SessionSwitch(t *testing.T) {
	root := t.TempDir()
	sm, err := session.NewSessionManager(root)
	if err != nil {
		t.Fatalf("NewSessionManager failed: %v", err)
	}

	// 创建两个会话，切换到一个。
	id1, err := sm.Create("s1")
	if err != nil {
		t.Fatalf("create s1 failed: %v", err)
	}
	_, _ = sm.Create("s2")
	if err := sm.Switch(id1); err != nil {
		t.Fatalf("switch failed: %v", err)
	}

	compactor := NewCompactor(nil, "/old/transcripts", "/old/tool-results")
	// switchSession 会调用 history/summary/memStore/events.SetPath，
	// 这些在测试中为 nil。用最小 stub 或直接调用 syncCompactorPaths。
	r := &Runner{sessions: sm, compactor: compactor}

	// 验证构造路径是旧的。
	if compactor.transcriptDir != "/old/transcripts" {
		t.Fatalf("initial transcriptDir mismatch: %q", compactor.transcriptDir)
	}

	// 直接调用 syncCompactorPaths（等价于 switchSession 内对 compactor 的同步逻辑）。
	r.syncCompactorPaths(sm.ActiveSessionDir())
	wantTranscripts := filepath.Join(sm.ActiveSessionDir(), "transcripts")
	wantToolResults := filepath.Join(sm.ActiveSessionDir(), "tool-results")
	if compactor.transcriptDir != wantTranscripts {
		t.Fatalf("transcriptDir should follow session: got %q want %q", compactor.transcriptDir, wantTranscripts)
	}
	if compactor.toolResultsDir != wantToolResults {
		t.Fatalf("toolResultsDir should follow session: got %q want %q", compactor.toolResultsDir, wantToolResults)
	}
}

// TestSyncCompactorPaths_NoCompactorNoPanic 验证 compactor 为 nil 时同步不 panic。
func TestSyncCompactorPaths_NoCompactorNoPanic(t *testing.T) {
	r := &Runner{}
	r.syncCompactorPaths(t.TempDir()) // 不应 panic
}
