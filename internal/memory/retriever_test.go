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
	root := t.TempDir()
	mkdirGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != filepath.Join(root, "AGENTS.md") {
		t.Errorf("should stop at repo root, got %s", path)
	}
	if content != "root instructions" {
		t.Errorf("content mismatch, got %q", content)
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

	// 注入前无项目指令。
	ctx, _ := r.BuildContext("test")
	if strings.Contains(ctx, "项目指令") {
		t.Error("should not contain project instructions before load")
	}

	// 加载后注入。
	//
	// 注意：LoadProjectInstructions 内部调用 os.Getwd()（进程工作目录），
	// 无法对临时目录生效。这里通过 findProjectInstructions(root) 手动填充
	// 缓存字段，等价地验证 BuildContext 的注入行为；
	// findProjectInstructions 本身已由 TestFindProjectInstructions_* 覆盖。
	path, content, err := findProjectInstructions(root)
	if err != nil {
		t.Fatalf("findProjectInstructions failed: %v", err)
	}
	if path == "" {
		t.Fatal("should find AGENTS.md in temp dir")
	}
	r.projectInstr = content
	r.projectInstrSrc = path

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

func TestLoadProjectInstructions_Refresh(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	agentsPath := filepath.Join(root, "AGENTS.md")
	if err := os.WriteFile(agentsPath, []byte("version 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newTestRetriever(t, root)

	// 首次加载。
	//
	// 同 TestRetriever_BuildContext_WithInstructions：绕开 LoadProjectInstructions
	// 对 os.Getwd() 的依赖，直接用 findProjectInstructions(root) 填充缓存，
	// 验证「缓存 → Clear → 重新加载 → 内容更新」的刷新语义。
	load := func() {
		_, content, err := findProjectInstructions(root)
		if err != nil {
			t.Fatalf("findProjectInstructions failed: %v", err)
		}
		r.projectInstr = content
		r.projectInstrSrc = agentsPath
	}
	load()
	if r.projectInstr != "version 1" {
		t.Errorf("should load version 1, got %q", r.projectInstr)
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
	load()
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
