# 项目上下文注入（AGENTS.md）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agent 自动读取仓库的 AGENTS.md / AGENTS 指令文件并注入每轮上下文，让 agent"认识仓库"；提供 `/reload` 命令手动刷新。

**Architecture:** `Retriever` 新增 `projectInstr`/`projectInstrSrc` 字段 + `LoadProjectInstructions()`/`ClearProjectInstructions()`；`findProjectInstructions` 从 cwd 向上查找（到 `.git` 所在目录停止）；`BuildContext` 步骤 0 注入 `## 项目指令`；`/reload` REPL 命令沿袭 `/compress` 模式。

**Tech Stack:** Go 1.26.4，无新依赖。截断在 memory 包内自实现（不引入 tool 依赖，避免破坏 leaf 独立性）。

**Spec:** `docs/superpowers/specs/2026-08-18-project-context-design.md`

---

### Task 1: Retriever 项目指令加载与注入

**Files:**
- Modify: `internal/memory/retriever.go`

- [ ] **Step 1: 新增字段 + 方法（放在 `NewRetriever` 之后、`BuildContext` 之前）**

在 `Retriever` 结构体（`retriever.go:40-45`）加两个字段：

```go
type Retriever struct {
	history *HistoryStore
	summary *SummaryStore
	memory  *MemoryStore
	events  *EventStore
	projectInstr   string // 项目指令内容（缓存），空 = 未加载
	projectInstrSrc string // 来源文件路径（调试/显示用）
}
```

在 `NewRetriever` 之后新增方法：

```go
// projectInstrMaxSize 项目指令文件最大读取大小（64KB）。
// 超过此大小截断，避免注入过大内容占用上下文。
const projectInstrMaxSize = 64 * 1024

// LoadProjectInstructions 从进程工作目录向上查找 AGENTS.md / AGENTS 文件并缓存。
//
// 查找规则：
//   - 候选文件名：AGENTS.md、AGENTS（无扩展名）
//   - 从 os.Getwd() 逐级向上，到包含 .git 的目录（仓库根）停止
//   - 找到即停（不合并多级，只取最近一级）
//
// 失败时返回错误（如无文件），但不清空已有缓存（保留旧内容）。
func (r *Retriever) LoadProjectInstructions() error {
	startDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("get working dir failed: %w", err)
	}
	path, content, err := findProjectInstructions(startDir)
	if err != nil {
		return err
	}
	if path == "" {
		return nil // 未找到，保持空缓存
	}
	r.projectInstr = content
	r.projectInstrSrc = path
	return nil
}

// ClearProjectInstructions 清空项目指令缓存。
// 配合 LoadProjectInstructions 实现手动刷新（/reload 命令）。
func (r *Retriever) ClearProjectInstructions() {
	r.projectInstr = ""
	r.projectInstrSrc = ""
}
```

- [ ] **Step 2: `BuildContext` 步骤 0 注入项目指令**

把 `BuildContext` 开头（`retriever.go:73-76`，`var parts []string` 之后、`步骤 1.1` 之前）：

```go
	var parts []string

	// ── 步骤 1.1: L3 记忆索引 ──
```

改为：

```go
	var parts []string

	// ── 步骤 0: 项目指令（AGENTS.md） ──
	// 仓库级约定注入到上下文最前（优先级高于会话记忆）。
	// 未加载时静默跳过，不影响现有行为。
	if r.projectInstr != "" {
		parts = append(parts, "## 项目指令\n"+r.projectInstr)
	}

	// ── 步骤 1.1: L3 记忆索引 ──
```

- [ ] **Step 3: 新增 `findProjectInstructions` + `truncateProjectInstr` 函数（文件末尾）**

```go
// findProjectInstructions 从 startDir 向上查找 AGENTS.md / AGENTS 文件。
//
// 候选文件名按优先级：AGENTS.md、AGENTS。
// 从 startDir 逐级向上，到包含 .git 的目录（仓库根）停止——包括该目录本身。
// 找到第一个存在的候选文件即返回（不合并多级）。
// 若一直未遇到 .git，到文件系统根停止。
//
// 返回：文件路径（未找到为空字符串）、内容（截断后）、错误。
func findProjectInstructions(startDir string) (string, string, error) {
	dir := startDir
	for {
		for _, name := range []string{"AGENTS.md", "AGENTS"} {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err == nil {
				return path, truncateProjectInstr(string(data)), nil
			}
			if !os.IsNotExist(err) {
				// 非"不存在"错误（权限等）——跳过该文件继续找。
				continue
			}
		}

		// 到达仓库根（含 .git）停止。
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return "", "", nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", nil // 到达文件系统根
		}
		dir = parent
	}
}

// truncateProjectInstr 截断过大的项目指令内容。
// 保留 head + tail（各占约一半），中间标记截断信息。
// memory 包内自实现，避免引入 tool 依赖（破坏 leaf 独立性）。
func truncateProjectInstr(s string) string {
	if len(s) <= projectInstrMaxSize {
		return s
	}
	headLen := projectInstrMaxSize / 2
	tailLen := projectInstrMaxSize - headLen
	note := fmt.Sprintf("\n\n…(项目指令过大，已截断，原 %d 字符)…\n\n", len(s))
	// 扣除提示信息长度。
	for headLen+tailLen+len(note) > projectInstrMaxSize && headLen > 0 {
		headLen--
	}
	return s[:headLen] + note + s[len(s)-tailLen:]
}
```

- [ ] **Step 4: 补 import（`os`、`path/filepath`）**

`retriever.go` 当前 import（`retriever.go:3-10`）：

```go
import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentic/internal/llm"
)
```

改为：

```go
import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agentic/internal/llm"
)
```

- [ ] **Step 5: 验证编译**

Run: `cd /Users/wang/Documents/agentic-1 && go build ./internal/memory/`
Expected: 无输出（编译成功）

- [ ] **Step 6: 提交**

```bash
git add internal/memory/retriever.go
git commit -m "feat(memory): load and inject AGENTS.md project instructions

Retriever caches the nearest AGENTS.md/AGENTS found upward from cwd
(stop at .git root); BuildContext injects it as the first context
section. Truncation is self-implemented to keep the memory package
a leaf (no tool dependency).

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 2: Retriever 测试

**Files:**
- Create: `internal/memory/retriever_test.go`

- [ ] **Step 1: 创建 `internal/memory/retriever_test.go`**

```go
package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// 子目录的 AGENTS.md 应该优先于仓库根的。
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
	// 在仓库根放 AGENTS.md，在仓库外（上级）也放一个。
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("root instructions"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 仓库根之上的 AGENTS.md（parent of root）不应该被读到。
	parent := filepath.Dir(root)
	parentAgents := filepath.Join(parent, "AGENTS.md")
	_ = os.WriteFile(parentAgents, []byte("parent instructions"), 0o644)
	defer os.Remove(parentAgents)

	path, content, err := findProjectInstructions(filepath.Join(root, "sub", "deep"))
	// sub/deep 不存在也 OK——查找逻辑只看目录层级。
	_ = err
	_ = content
	// 从 root 内任意位置向上，应命中 root 的 AGENTS.md 后停止。
	path, content, err = findProjectInstructions(root)
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
	// AGENTS（无扩展名）变体。
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
	// head 保留开头。
	if !strings.HasPrefix(got, "bbb") {
		t.Error("head should be preserved")
	}
	// tail 保留结尾。
	if !strings.HasSuffix(got, "bbb") {
		t.Error("tail should be preserved")
	}
}

func TestRetriever_BuildContext_WithInstructions(t *testing.T) {
	root := t.TempDir()
	mkdirGitRoot(t, root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("项目用中文注释"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Retriever 需要 store。用内存/临时目录构造最小 store。
	r := newTestRetriever(t, root)

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
	// 项目指令应在记忆之前（更靠前）。
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
	// 无项目指令时行为与现状一致：空仓库无记忆 → 空上下文。
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

	if err := r.LoadProjectInstructions(); err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
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
```

> 注意：`TestFindProjectInstructions_GitRoot` 里我写了 `_ = err` / `_ = content`（因为中间对 `sub/deep` 路径的调用结果未使用）——如果 go vet 报 unused，改为直接删除那次调用，保留从 `root` 开始的调用即可。实现时以编译通过为准。

- [ ] **Step 2: 运行测试**

Run: `cd /Users/wang/Documents/agentic-1 && go test -count=1 ./internal/memory/ -v`
Expected: 8 个测试 PASS（TestFindProjectInstructions_Nearest / GitRoot / NotFound / BareVariant + TestTruncateProjectInstr + TestRetriever_BuildContext_WithInstructions / NoInstructions + TestLoadProjectInstructions_Refresh）

- [ ] **Step 3: 提交**

```bash
git add internal/memory/retriever_test.go
git commit -m "test(memory): add project instructions loading tests

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 3: main.go 启动加载 + /reload 命令

**Files:**
- Modify: `main.go`（retriever 初始化后调用 LoadProjectInstructions）
- Modify: `internal/agent/session.go`（handleSessionCommand 加 /reload 分支）
- Modify: `internal/agent/runner.go`（未知命令提示加 /reload）

- [ ] **Step 1: `main.go` 启动加载**

在 `main.go` 的 retriever 初始化处（当前 `main.go:145` 附近）：

```go
	// ── 步骤 9: 初始化记忆检索器 ──
	// Retriever 组合三层存储，在每轮查询前构建上下文（L3 记忆 + L2 摘要 + 降级 L1）。
	// 同时负责自动压缩：当上下文 token 用量超过模型窗口 80% 时触发 L2 摘要合并。
	retriever := memory.NewRetriever(history, summary, memStore, events)
```

改为：

```go
	// ── 步骤 9: 初始化记忆检索器 ──
	// Retriever 组合三层存储，在每轮查询前构建上下文（项目指令 + L3 记忆 + L2 摘要 + 降级 L1）。
	// 同时负责自动压缩：当上下文 token 用量超过模型窗口 80% 时触发 L2 摘要合并。
	retriever := memory.NewRetriever(history, summary, memStore, events)

	// 加载项目指令（AGENTS.md），失败只警告不退出（无指令文件时正常启动）。
	if err := retriever.LoadProjectInstructions(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: load project instructions failed: %v\n", err)
	}
```

- [ ] **Step 2: `internal/agent/session.go` 加 `/reload` 分支**

在 `handleSessionCommand` 的 switch 里，`case "/compress":` 之后加：

```go
	// ── /reload ──
	// 重新加载项目指令（AGENTS.md），用于指令文件变更后手动刷新。
	case "/reload":
		r.retriever.ClearProjectInstructions()
		if err := r.retriever.LoadProjectInstructions(); err != nil {
			r.ui.OnError(fmt.Errorf("重新加载项目指令失败: %v", err))
			return 0, true
		}
		r.ui.OnMessage("✅ 已重新加载项目指令")
		return 0, true
```

- [ ] **Step 3: `runner.go` 未知命令提示加 `/reload`**

在 `Runner.Run` 的未知命令提示（`runner.go:228` 附近）：

```go
					r.ui.OnError(fmt.Errorf("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory"))
```

改为：

```go
					r.ui.OnError(fmt.Errorf("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory, /reload"))
```

- [ ] **Step 4: 验证编译 + 全量测试**

Run: `cd /Users/wang/Documents/agentic-1 && go build ./... && go test -count=1 ./...`
Expected: 全部包 PASS

- [ ] **Step 5: 提交**

```bash
git add main.go internal/agent/session.go internal/agent/runner.go
git commit -m "feat: load project instructions at startup and add /reload command

Co-Authored-By: Claude <noreply@anthropic.com>"
```

---

### Task 4: CLAUDE.md 文档 + 冒烟验证

**Files:**
- Modify: `CLAUDE.md`（REPL 命令表加 /reload + 上下文流程说明）

- [ ] **Step 1: 更新 `CLAUDE.md` REPL 命令表**

在 REPL 命令表中 `/compress` 行后加：

```markdown
| `/reload` | Reload AGENTS.md project instructions |
```

- [ ] **Step 2: 更新 `CLAUDE.md` 的 BuildContext 流程说明**

把：

```
**Context building** (`Retriever.BuildContext`):
1. L3 memory index + high-importance memories
2. L2 recent summaries (last 10)
3. Fallback: L2 → EventStore digest → HistoryStore digest
```

改为：

```
**Context building** (`Retriever.BuildContext`):
0. Project instructions (AGENTS.md, cached at startup, `/reload` to refresh)
1. L3 memory index + high-importance memories
2. L2 recent summaries (last 10)
3. Fallback: L2 → EventStore digest → HistoryStore digest
```

- [ ] **Step 3: 冒烟验证**

在仓库根（当前目录 `/Users/wang/Documents/agentic-1`）没有 AGENTS.md——先创建一个临时测试文件验证加载，再删除：

Run:
```bash
cd /Users/wang/Documents/agentic-1
echo "项目约定：所有代码注释必须用中文。用户偏好简洁回答。" > AGENTS.md
go build -o /tmp/agentic-pi .
OPENAI_API_KEY= /tmp/agentic-pi -one-shot "这个项目有哪些约定？" 
rm -f AGENTS.md /tmp/agentic-pi
```

Expected: 模型回答中引用 AGENTS.md 里的约定（"用中文注释"、"简洁回答"）。若 API key 无效导致无法验证，以 `go test` 全绿为准。

- [ ] **Step 4: 提交**

```bash
git add CLAUDE.md
git commit -m "docs: document /reload command and project context step

Co-Authored-By: Claude <noreply@anthropic.com>"
```
