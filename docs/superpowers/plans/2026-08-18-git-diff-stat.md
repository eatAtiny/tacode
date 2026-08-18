# Git diff --stat Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `git` 工具的 `diff` action 支持 `stat` 参数，输出文件级变更统计概览。

**Architecture:** 单任务实现——`internal/tool/git.go` 的 `Parameters()` 加 `stat` 字段、`Execute` 解析并传参、`doDiff` 分支加 `--stat`；`internal/tool/git_test.go` 加 `TestGitDiff_Stat`。

**Tech Stack:** Go 1.26.4，无新依赖。

**Spec:** `docs/superpowers/specs/2026-08-18-git-diff-stat-design.md`

---

### Task 1: git diff --stat 实现 + 测试

**Files:**
- Modify: `internal/tool/git.go`
- Modify: `internal/tool/git_test.go`

- [ ] **Step 1: `Parameters()` 加 `stat` 字段**

在 `internal/tool/git.go` 的 `Parameters()` 里，`"staged"` 字段（当前约 `git.go:59-62`）之后加：

```go
			"stat": map[string]any{
				"type":        "boolean",
				"description": "仅 diff 使用：只看文件级变更统计（X files changed, +N/-N），不看具体 diff",
			},
```

- [ ] **Step 2: `Execute` 解析 `Stat` 并传参**

`Execute` 的 params 结构体（`git.go:90-99`）加字段：

```go
		Stat        bool   `json:"stat"`
```

`case "diff":` 行（`git.go:115-116`）：

```go
	case "diff":
		return t.doDiff(ctx, params.Path, params.Staged)
```

改为：

```go
	case "diff":
		return t.doDiff(ctx, params.Path, params.Staged, params.Stat)
```

- [ ] **Step 3: `doDiff` 签名加 `stat` 参数 + 分支**

`doDiff`（`git.go` 约 283-297 行）：

```go
func (t *GitTool) doDiff(ctx context.Context, path string, staged bool) (string, error) {
	args := []string{"diff", "--no-color"}
	if staged {
		args = append(args, "--staged")
	}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := t.runGit(ctx, args...)
	if err != nil {
		return "", t.gitError(err)
	}
	if out == "" {
		if staged {
			return "暂存区无变更。", nil
		}
		return "无变更。", nil
	}
	return out, nil
}
```

改为：

```go
func (t *GitTool) doDiff(ctx context.Context, path string, staged, stat bool) (string, error) {
	args := []string{"diff", "--no-color"}
	if stat {
		args = append(args, "--stat")
	}
	if staged {
		args = append(args, "--staged")
	}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := t.runGit(ctx, args...)
	if err != nil {
		return "", t.gitError(err)
	}
	if out == "" {
		if staged {
			return "暂存区无变更。", nil
		}
		return "无变更。", nil
	}
	return out, nil
}
```

- [ ] **Step 4: `git_test.go` 加 `TestGitDiff_Stat`**

在 `internal/tool/git_test.go` 的 `TestGitDiff` 之后加：

```go
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
	if !strings.Contains(res, "files changed") || !strings.Contains(res, "insertion") {
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
```

> 注：`git diff --stat` 输出格式为 `X files changed, N insertions(+), M deletions(-)`（单文件时 `1 file changed`）。断言 `insertion`（含复数 `insertions` 前缀匹配）和 `files changed`。

- [ ] **Step 5: 运行测试**

Run: `cd /Users/wang/Documents/agentic-1 && go test -count=1 ./internal/tool/ -run TestGitDiff -v`
Expected: TestGitDiff + TestGitDiff_Stat 均 PASS

- [ ] **Step 6: 全量验证**

Run: `cd /Users/wang/Documents/agentic-1 && go build ./... && go test -count=1 ./...`
Expected: 全部包 PASS

- [ ] **Step 7: 提交（精确路径）**

```bash
git add internal/tool/git.go internal/tool/git_test.go
git commit -m "feat(tool): add stat option to git diff for file-level summary

Co-Authored-By: Claude <noreply@anthropic.com>"
```
