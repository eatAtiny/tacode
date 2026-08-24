package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// ──────────────────────────────────────────────────────────
// 三级记忆迁移测试
//
// MigrateLegacyMemory 把旧版散落的 L3 记忆按 type 归位：
//   - user/feedback → 全局 store
//   - project/reference → 项目 store
// 幂等：重复运行无副作用。
// ──────────────────────────────────────────────────────────

func TestMigrateLegacyMemory_FromSessionDirs(t *testing.T) {
	dataRoot := t.TempDir()
	sessionsRoot := filepath.Join(dataRoot, "sessions")

	// 构造一个带旧记忆的会话目录。
	sessionDir := filepath.Join(sessionsRoot, "sess-1")
	os.MkdirAll(filepath.Join(sessionDir, "memory"), 0o755)
	sessStore, err := NewMemoryStore(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	// project 类 + user 类各一条。
	if err := sessStore.SaveEntry(MemoryEntry{
		Name: "old-project-mem", Type: "project", Importance: 3, Content: "p",
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessStore.SaveEntry(MemoryEntry{
		Name: "old-user-mem", Type: "user", Importance: 3, Content: "u",
	}); err != nil {
		t.Fatal(err)
	}

	globalStore, _ := NewMemoryStore(filepath.Join(dataRoot, "global-memory"))
	projectStore, _ := NewMemoryStore(filepath.Join(dataRoot, "project-memory"))

	n := MigrateLegacyMemory(globalStore, projectStore, sessionsRoot)
	if n != 2 {
		t.Fatalf("expected 2 migrations, got %d", n)
	}

	// user 类 → 全局，project 类 → 项目。
	if globalStore.GetEntry("old-user-mem") == nil {
		t.Error("user memory should be migrated to global store")
	}
	if projectStore.GetEntry("old-project-mem") == nil {
		t.Error("project memory should be migrated to project store")
	}
	// 源文件已删除。
	if sessStore.GetEntry("old-user-mem") != nil || sessStore.GetEntry("old-project-mem") != nil {
		t.Error("session memory should be cleared after migration")
	}
}

func TestMigrateLegacyMemory_FromLegacyGlobalDir(t *testing.T) {
	dataRoot := t.TempDir()
	sessionsRoot := filepath.Join(dataRoot, "sessions")
	os.MkdirAll(sessionsRoot, 0o755)

	// 旧全局目录里混着 project 类（应移到项目级）。
	globalStore, _ := NewMemoryStore(filepath.Join(dataRoot, "global-memory"))
	if err := globalStore.SaveEntry(MemoryEntry{
		Name: "legacy-project", Type: "project", Importance: 3, Content: "p",
	}); err != nil {
		t.Fatal(err)
	}

	projectStore, _ := NewMemoryStore(filepath.Join(dataRoot, "project-memory"))

	n := MigrateLegacyMemory(globalStore, projectStore, sessionsRoot)
	if n != 1 {
		t.Fatalf("expected 1 migration, got %d", n)
	}
	if projectStore.GetEntry("legacy-project") == nil {
		t.Error("project memory from legacy global dir should migrate to project store")
	}
	if globalStore.GetEntry("legacy-project") != nil {
		t.Error("legacy global dir should not keep project memory")
	}
}

func TestMigrateLegacyMemory_Idempotent(t *testing.T) {
	dataRoot := t.TempDir()
	sessionsRoot := filepath.Join(dataRoot, "sessions")

	sessionDir := filepath.Join(sessionsRoot, "sess-1")
	os.MkdirAll(filepath.Join(sessionDir, "memory"), 0o755)
	sessStore, _ := NewMemoryStore(sessionDir)
	_ = sessStore.SaveEntry(MemoryEntry{Name: "m1", Type: "project", Importance: 3, Content: "x"})

	globalStore, _ := NewMemoryStore(filepath.Join(dataRoot, "global-memory"))
	projectStore, _ := NewMemoryStore(filepath.Join(dataRoot, "project-memory"))

	// 第一次迁移。
	n1 := MigrateLegacyMemory(globalStore, projectStore, sessionsRoot)
	// 第二次：无新文件可迁。
	n2 := MigrateLegacyMemory(globalStore, projectStore, sessionsRoot)

	if n1 != 1 || n2 != 0 {
		t.Errorf("expected 1 then 0 migrations, got %d then %d", n1, n2)
	}
	if projectStore.GetEntry("m1") == nil {
		t.Error("project memory should persist after idempotent re-run")
	}
}
