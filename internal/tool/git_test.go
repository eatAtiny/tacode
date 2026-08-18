package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ──────────────────────────────────────────────────────────
// 测试辅助：临时 git 仓库
// ──────────────────────────────────────────────────────────

// initTestRepo 在临时目录初始化 git 仓库并配置提交用户。
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q", "-b", "main")
	gitOut(t, dir, "config", "user.email", "test@example.com")
	gitOut(t, dir, "config", "user.name", "Test User")
	return dir
}

// gitOut 在指定目录执行 git 命令，失败时终止测试。
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commitAll 暂存并提交所有变更。
func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	gitOut(t, dir, "add", ".")
	gitOut(t, dir, "commit", "-m", msg)
}

// writeTestFile 在目录中写入文件。
func writeTestFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ──────────────────────────────────────────────────────────
// 只读操作测试
// ──────────────────────────────────────────────────────────

func TestGitStatus_Untracked(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "hello\n")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "status"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "未跟踪") || !strings.Contains(res, "a.txt") {
		t.Errorf("status should show untracked a.txt, got:\n%s", res)
	}
}

func TestGitStatus_Clean(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "hello\n")
	commitAll(t, dir, "init")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "status"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "干净") {
		t.Errorf("clean repo should say clean, got:\n%s", res)
	}
}

func TestGitDiff(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "line1\n")
	commitAll(t, dir, "init")
	writeTestFile(t, dir, "a.txt", "line1\nline2\n")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "diff"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "+line2") {
		t.Errorf("diff should contain added line, got:\n%s", res)
	}

	// staged diff：add 前应为空。
	res, err = g.Execute(toJSON(map[string]any{"action": "diff", "staged": true}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "无变更") {
		t.Errorf("staged diff should be empty before add, got:\n%s", res)
	}
}

func TestGitDiff_Stat(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "line1\nline2\n")
	commitAll(t, dir, "init")
	writeTestFile(t, dir, "a.txt", "line1\nline2\nline3\n")

	g := NewGitToolIn(dir)

	// stat=true → 文件级统计。
	res, err := g.Execute(toJSON(map[string]any{"action": "diff", "stat": true}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !(strings.Contains(res, "file changed") || strings.Contains(res, "files changed")) || !strings.Contains(res, "insertion") {
		t.Errorf("stat diff should contain file-level summary, got:\n%s", res)
	}
	if strings.Contains(res, "+line3") {
		t.Errorf("stat diff should NOT contain full diff lines, got:\n%s", res)
	}

	// stat=false → 完整 diff（默认行为）。
	res, err = g.Execute(toJSON(map[string]any{"action": "diff"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "+line3") {
		t.Errorf("full diff should contain added line, got:\n%s", res)
	}
}

func TestGitLog(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "1\n")
	commitAll(t, dir, "first commit")
	writeTestFile(t, dir, "b.txt", "2\n")
	commitAll(t, dir, "second commit")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "log", "max_count": 5}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "first commit") || !strings.Contains(res, "second commit") {
		t.Errorf("log should contain both messages, got:\n%s", res)
	}
	if !strings.Contains(res, "Test User") {
		t.Errorf("log should contain author name, got:\n%s", res)
	}
}

func TestGitShow(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "hello\n")
	commitAll(t, dir, "first commit")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "show", "path": "HEAD"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "first commit") || !strings.Contains(res, "hello") {
		t.Errorf("show should contain commit message and file content, got:\n%s", res)
	}
}

func TestGitBranch(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "1\n")
	commitAll(t, dir, "init")
	gitOut(t, dir, "branch", "feature")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "branch"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res, "* main") || !strings.Contains(res, "feature") {
		t.Errorf("branch should list current and feature, got:\n%s", res)
	}
}

// ──────────────────────────────────────────────────────────
// 写操作测试
// ──────────────────────────────────────────────────────────

func TestGitAddCommit(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "hello\n")

	g := NewGitToolIn(dir)

	// add（默认全部）。
	res, err := g.Execute(toJSON(map[string]any{"action": "add"}))
	if err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if !strings.Contains(res, "已暂存") {
		t.Errorf("add should confirm, got:\n%s", res)
	}

	// commit。
	res, err = g.Execute(toJSON(map[string]any{"action": "commit", "message": "my commit"}))
	if err != nil {
		t.Fatalf("commit failed: %v", err)
	}
	if !strings.Contains(res, "my commit") {
		t.Errorf("commit output should mention message, got:\n%s", res)
	}

	// 仓库状态验证。
	if status := gitOut(t, dir, "status", "--short"); status != "" {
		t.Errorf("repo should be clean after commit, got:\n%s", status)
	}

	// 无 message 的 commit → 报错。
	if _, err := g.Execute(toJSON(map[string]any{"action": "commit"})); err == nil {
		t.Error("commit without message should error")
	}
}

func TestGitCheckout_Branch(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "1\n")
	commitAll(t, dir, "init")
	gitOut(t, dir, "branch", "feature")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "checkout", "branch_name": "feature"}))
	if err != nil {
		t.Fatalf("checkout failed: %v", err)
	}
	if !strings.Contains(res, "feature") {
		t.Errorf("checkout output should mention branch, got:\n%s", res)
	}
	if got := gitOut(t, dir, "branch", "--show-current"); got != "feature" {
		t.Errorf("current branch should be feature, got %q", got)
	}
}

func TestGitCheckout_RestoreFile(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "original\n")
	commitAll(t, dir, "init")
	writeTestFile(t, dir, "a.txt", "modified\n")

	g := NewGitToolIn(dir)
	res, err := g.Execute(toJSON(map[string]any{"action": "checkout", "files": "a.txt"}))
	if err != nil {
		t.Fatalf("checkout restore failed: %v", err)
	}
	if !strings.Contains(res, "已恢复") {
		t.Errorf("checkout restore should confirm, got:\n%s", res)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(data) != "original\n" {
		t.Errorf("file should be restored, got %q", string(data))
	}
}

func TestGitStash(t *testing.T) {
	dir := initTestRepo(t)
	writeTestFile(t, dir, "a.txt", "original\n")
	commitAll(t, dir, "init")
	writeTestFile(t, dir, "a.txt", "wip changes\n")

	g := NewGitToolIn(dir)

	// stash list（默认，只读）→ 空列表。
	res, err := g.Execute(toJSON(map[string]any{"action": "stash"}))
	if err != nil {
		t.Fatalf("stash list failed: %v", err)
	}
	if !strings.Contains(res, "为空") {
		t.Errorf("stash list should be empty, got:\n%s", res)
	}

	// stash push。
	res, err = g.Execute(toJSON(map[string]any{"action": "stash", "stash_action": "push", "message": "wip"}))
	if err != nil {
		t.Fatalf("stash push failed: %v", err)
	}
	if !strings.Contains(res, "已保存") {
		t.Errorf("stash push should confirm, got:\n%s", res)
	}

	// stash list → 有一条。
	res, err = g.Execute(toJSON(map[string]any{"action": "stash", "stash_action": "list"}))
	if err != nil {
		t.Fatalf("stash list failed: %v", err)
	}
	if !strings.Contains(res, "wip") {
		t.Errorf("stash list should contain wip, got:\n%s", res)
	}

	// stash pop。
	res, err = g.Execute(toJSON(map[string]any{"action": "stash", "stash_action": "pop"}))
	if err != nil {
		t.Fatalf("stash pop failed: %v", err)
	}
	if !strings.Contains(res, "已从 stash 恢复") {
		t.Errorf("stash pop should confirm, got:\n%s", res)
	}
}

// ──────────────────────────────────────────────────────────
// 权限 / 并发 / 错误测试
// ──────────────────────────────────────────────────────────

func TestGitPermission(t *testing.T) {
	g := NewGitTool()

	readActions := []string{"status", "diff", "log", "show", "branch"}
	for _, a := range readActions {
		if perm := g.CheckPermission(toJSON(map[string]any{"action": a})); !perm.Allow {
			t.Errorf("action %s should be allowed, got %+v", a, perm)
		}
	}

	writeActions := []string{"add", "commit", "checkout"}
	for _, a := range writeActions {
		if perm := g.CheckPermission(toJSON(map[string]any{"action": a})); perm.Allow {
			t.Errorf("action %s should require confirmation", a)
		}
	}

	// stash：默认 list 只读，push/pop 需要确认。
	if perm := g.CheckPermission(toJSON(map[string]any{"action": "stash"})); !perm.Allow {
		t.Error("stash default (list) should be read-only")
	}
	if perm := g.CheckPermission(toJSON(map[string]any{"action": "stash", "stash_action": "push"})); perm.Allow {
		t.Error("stash push should require confirmation")
	}
	if perm := g.CheckPermission(toJSON(map[string]any{"action": "stash", "stash_action": "pop"})); perm.Allow {
		t.Error("stash pop should require confirmation")
	}

	// 无效参数 fail-closed。
	if perm := g.CheckPermission(`invalid`); perm.Allow {
		t.Error("invalid args should fail-closed (require confirmation)")
	}
}

func TestGitConcurrency(t *testing.T) {
	g := NewGitTool()

	if !g.IsReadOnly(toJSON(map[string]any{"action": "status"})) {
		t.Error("status should be read-only")
	}
	if !g.IsConcurrencySafe(toJSON(map[string]any{"action": "diff"})) {
		t.Error("diff should be concurrency-safe")
	}
	if g.IsReadOnly(toJSON(map[string]any{"action": "commit", "message": "x"})) {
		t.Error("commit should not be read-only")
	}
	if g.IsConcurrencySafe(toJSON(map[string]any{"action": "checkout", "branch_name": "x"})) {
		t.Error("checkout should not be concurrency-safe")
	}
	if !g.IsReadOnly(toJSON(map[string]any{"action": "stash"})) {
		t.Error("stash list should be read-only")
	}
	if g.IsReadOnly(toJSON(map[string]any{"action": "stash", "stash_action": "pop"})) {
		t.Error("stash pop should not be read-only")
	}
	if g.IsReadOnly(`invalid`) {
		t.Error("invalid args should fail-closed")
	}
}

func TestGitNotARepo(t *testing.T) {
	dir := t.TempDir() // 非 git 仓库
	g := NewGitToolIn(dir)

	_, err := g.Execute(toJSON(map[string]any{"action": "status"}))
	if err == nil {
		t.Fatal("expected error for non-repo directory")
	}
	if !strings.Contains(err.Error(), "不是 git 仓库") {
		t.Errorf("should give friendly not-a-repo hint, got: %v", err)
	}
}
