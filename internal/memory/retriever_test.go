package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// mkdirGitRoot 创建带 .git 的目录结构。
func mkdirGitRoot(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFindProjectInstructions_Nearest(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "AGENTS.md"), []byte("sub instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("sub", "AGENTS.md")) {
		t.Errorf("should find sub/AGENTS.md, got %s", path)
	}
	if content != "sub instructions" {
		t.Errorf("content mismatch, got %q", content)
	}
}

func TestFindProjectInstructions_GitRoot(t *testing.T) {
	outer := t.TempDir()
	root := filepath.Join(outer, "repo")
	mkdirGitRoot(t, root)
	// 仓库根（含 .git）之上的 AGENTS.md 是哨兵文件：
	// 若 findProjectInstructions 忽略 .git 继续向上查找，就会读到它。
	// 正确行为是在含 .git 的仓库根处停止，返回空。
	// 注意：仓库根处不放 AGENTS.md，否则会在检查 .git 之前提前返回，测不到边界。
	if err := os.WriteFile(filepath.Join(outer, "AGENTS.md"), []byte("outer instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" || content != "" {
		t.Errorf("walk should stop at repo root (.git), got path=%q content=%q", path, content)
	}
}

func TestFindProjectInstructions_NotFound(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != "" || content != "" {
		t.Errorf("should return empty for no instructions, got path=%q content=%q", path, content)
	}
}

func TestFindProjectInstructions_BareVariant(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS"), []byte("bare variant"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "AGENTS") {
		t.Errorf("should find bare AGENTS, got %s", path)
	}
	if content != "bare variant" {
		t.Errorf("content mismatch, got %q", content)
	}
}

func TestFindProjectInstructions_CLAUDE_MD(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("claude instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "CLAUDE.md") {
		t.Errorf("should find CLAUDE.md, got %s", path)
	}
	if content != "claude instructions" {
		t.Errorf("content mismatch, got %q", content)
	}
}

func TestFindProjectInstructions_ClaudePriorityOverAgents(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	// CLAUDE.md 与 AGENTS.md 共存时，CLAUDE.md 优先（与 Claude Code 习惯一致）。
	if err := os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("from claude"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("from agents"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, "CLAUDE.md") {
		t.Errorf("CLAUDE.md should take priority over AGENTS.md, got %s", path)
	}
	if content != "from claude" {
		t.Errorf("content mismatch, got %q", content)
	}
}

func TestFindProjectInstructions_CursorRulesFallback(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	// 无 CLAUDE.md / AGENTS 时，.cursorrules 作为最后候选。
	if err := os.WriteFile(filepath.Join(root, ".cursorrules"), []byte("cursor rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasSuffix(path, ".cursorrules") {
		t.Errorf("should fall back to .cursorrules, got %s", path)
	}
	if content != "cursor rules" {
		t.Errorf("content mismatch, got %q", content)
	}
}

func TestTruncateProjectInstr(t *testing.T) {
	small := strings.Repeat("a", 100)
	if got := truncateProjectInstr(small); got != small {
		t.Error("small content should not be truncated")
	}

	large := strings.Repeat("b", projectInstrMaxSize+1000)
	got := truncateProjectInstr(large)
	if len(got) > projectInstrMaxSize {
		t.Errorf("truncated content too long: %d > %d", len(got), projectInstrMaxSize)
	}
	if !strings.Contains(got, "已截断") {
		t.Error("truncated content should contain truncation notice")
	}
	if !strings.HasPrefix(got, "bbb") {
		t.Error("head should be preserved")
	}
	if !strings.HasSuffix(got, "bbb") {
		t.Error("tail should be preserved")
	}
}

func TestTruncateProjectInstr_CJK(t *testing.T) {
	// 中文（多字节 UTF-8）内容截断后必须是合法 UTF-8。
	content := strings.Repeat("中文指令内容", projectInstrMaxSize/6+100)
	got := truncateProjectInstr(content)
	if len(got) > projectInstrMaxSize {
		t.Errorf("truncated content too long: %d > %d", len(got), projectInstrMaxSize)
	}
	if !utf8.ValidString(got) {
		t.Error("truncated content should be valid UTF-8 (no split runes)")
	}
	if !strings.Contains(got, "已截断") {
		t.Error("truncated content should contain truncation notice")
	}
}

func TestRetriever_BuildContext_WithInstructions(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("项目用中文注释"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newTestRetriever(t, root)

	// 切换到 root 目录：LoadProjectInstructions 内部调用 os.Getwd()，
	// t.Chdir 让该调用对临时目录生效（非 parallel，测试结束后自动恢复）。
	t.Chdir(root)

	// 注入前无项目指令。
	ctx, _ := r.BuildContext("test")
	if strings.Contains(ctx, "项目指令") {
		t.Error("should not contain project instructions before load")
	}

	// 加载后注入。
	if err := r.LoadProjectInstructions(); err != nil {
		t.Fatalf("LoadProjectInstructions failed: %v", err)
	}
	ctx, _ = r.BuildContext("test")
	if !strings.Contains(ctx, "## 项目指令") {
		t.Error("BuildContext should contain project instructions section")
	}
	if !strings.Contains(ctx, "项目用中文注释") {
		t.Error("BuildContext should contain instructions content")
	}
	instrIdx := strings.Index(ctx, "## 项目指令")
	if instrIdx != 0 {
		t.Errorf("project instructions should be the first section, got index %d", instrIdx)
	}
}

func TestRetriever_BuildContext_NoInstructions(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	r := newTestRetriever(t, root)

	ctx, err := r.BuildContext("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ctx != "" {
		t.Errorf("empty repo should yield empty context, got %q", ctx)
	}
}

func TestRetriever_BuildContext_ProjectMemory(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	r := newTestRetriever(t, root)

	// 项目级记忆 store 指向独立目录，写入一条 project 类记忆。
	projectRoot := t.TempDir()
	projectMem, err := NewMemoryStore(projectRoot)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if err := projectMem.SaveEntry(MemoryEntry{
		Name:        "project-convention",
		Description: "项目约定：用中文注释",
		Type:        "project",
		Importance:  4,
		Content:     "所有代码注释和文档用中文",
	}); err != nil {
		t.Fatalf("SaveEntry failed: %v", err)
	}
	r.SetProjectMemory(projectMem)

	ctx, err := r.BuildContext("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(ctx, "## 项目记忆索引") {
		t.Error("BuildContext should contain project memory index section")
	}
	if !strings.Contains(ctx, "## 项目重要记忆") {
		t.Error("BuildContext should contain project important memories section")
	}
	if !strings.Contains(ctx, "项目约定：用中文注释") {
		t.Error("BuildContext should contain project memory content")
	}
	// 无项目指令时，项目记忆应在最前。
	if !strings.HasPrefix(ctx, "## 项目记忆索引") {
		t.Errorf("project memory should be first section without project instructions, got %q", ctx[:min(30, len(ctx))])
	}
}

func TestRetriever_BuildContext_GlobalMemory(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	r := newTestRetriever(t, root)

	// 全局记忆 store 指向独立目录，写入一条 user 类记忆。
	globalRoot := t.TempDir()
	globalMem, err := NewMemoryStore(globalRoot)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	if err := globalMem.SaveEntry(MemoryEntry{
		Name:        "user-language-pref",
		Description: "用户偏好：中文回答",
		Type:        "user",
		Importance:  4,
		Content:     "用户喜欢用中文回复",
	}); err != nil {
		t.Fatalf("SaveEntry failed: %v", err)
	}
	r.SetGlobalMemory(globalMem)

	ctx, err := r.BuildContext("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(ctx, "## 全局记忆索引") {
		t.Error("BuildContext should contain global memory index section")
	}
	if !strings.Contains(ctx, "## 全局重要记忆") {
		t.Error("BuildContext should contain global important memories section")
	}
	if !strings.Contains(ctx, "用户偏好：中文回答") {
		t.Error("BuildContext should contain global memory content")
	}
}

func TestRetriever_BuildContext_MemoryLayersOrder(t *testing.T) {
	// 三级记忆顺序：项目指令 → 项目记忆 → 全局记忆 → 会话记忆。
	root := t.TempDir()
	mkdirGitRoot(t, root)
	r := newTestRetriever(t, root)

	// 项目记忆。
	projectMem, _ := NewMemoryStore(t.TempDir())
	_ = projectMem.SaveEntry(MemoryEntry{Name: "p1", Description: "项目约定", Type: "project", Importance: 4, Content: "x"})
	r.SetProjectMemory(projectMem)

	// 全局记忆。
	globalMem, _ := NewMemoryStore(t.TempDir())
	_ = globalMem.SaveEntry(MemoryEntry{Name: "g1", Description: "用户偏好", Type: "user", Importance: 4, Content: "y"})
	r.SetGlobalMemory(globalMem)

	ctx, err := r.BuildContext("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	projIdx := strings.Index(ctx, "## 项目记忆索引")
	globalIdx := strings.Index(ctx, "## 全局记忆索引")
	if projIdx < 0 || globalIdx < 0 {
		t.Fatalf("both layers should appear, project=%d global=%d", projIdx, globalIdx)
	}
	if projIdx > globalIdx {
		t.Error("project memory should come before global memory")
	}
}

func TestRetriever_BuildContext_GlobalMemory_NotEnabled(t *testing.T) {
	// 未设置全局记忆时行为与旧版一致：不含全局记忆段。
	root := t.TempDir()
	mkdirGitRoot(t, root)
	r := newTestRetriever(t, root)

	ctx, err := r.BuildContext("test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(ctx, "全局记忆") {
		t.Errorf("global memory should not appear when not enabled, got %q", ctx)
	}
}

func TestLoadProjectInstructions_Refresh(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("version 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newTestRetriever(t, root)

	// 切换到 root 目录，让 LoadProjectInstructions 的 os.Getwd() 对临时目录生效。
	t.Chdir(root)

	if err := r.LoadProjectInstructions(); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	if r.projectInstr != "version 1" {
		t.Errorf("should load version 1, got %q", r.projectInstr)
	}
	if r.projectInstrSrc != agentsPath {
		t.Errorf("source should point to AGENTS.md, got %q", r.projectInstrSrc)
	}

	// 修改文件 → Clear + Load → 内容更新。
	if err := os.WriteFile(agentsPath, []byte("version 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.ClearProjectInstructions()
	if r.projectInstr != "" {
		t.Error("ClearProjectInstructions should empty the cache")
	}
	if r.projectInstrSrc != "" {
		t.Error("ClearProjectInstructions should empty the source path cache")
	}
	if err := r.LoadProjectInstructions(); err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if r.projectInstr != "version 2" {
		t.Errorf("should reload version 2, got %q", r.projectInstr)
	}
}

// newTestRetriever 构造一个 store 指向 root 的 Retriever（不建真实会话）。
func newTestRetriever(t *testing.T, root string) *Retriever {
	t.Helper()
	history, err := NewHistoryStore(root)
	if err != nil {
		t.Fatalf("NewHistoryStore failed: %v", err)
	}
	summary, err := NewSummaryStore(root)
	if err != nil {
		t.Fatalf("NewSummaryStore failed: %v", err)
	}
	memStore, err := NewMemoryStore(root)
	if err != nil {
		t.Fatalf("NewMemoryStore failed: %v", err)
	}
	events := NewEventStore(root)
	return NewRetriever(history, summary, memStore, events)
}
