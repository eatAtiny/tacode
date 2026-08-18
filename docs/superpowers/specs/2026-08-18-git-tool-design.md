# Git 工具设计文档

- 日期：2026-08-18
- 状态：已批准
- 范围：`internal/tool/git.go` 新增 GitTool + `internal/tool/git_test.go` 测试 + `main.go` 注册 + CLAUDE.md 更新

## 背景

Agent 目前只能通过 `shell` 工具手搓 git 命令（只读命令列表里已含 `git log/status/diff/branch`），输出不结构化、无 diff 审阅、写操作依赖危险命令检测。需要一个结构化 git 工具，作为 Coding Agent 的立身之本。

## 决策

| 决策点 | 结论 |
|--------|------|
| 工具形态 | 单个 `git` 工具 + `action` 参数（与 `file` 工具模式一致） |
| 只读 action | `status` / `diff` / `log` / `show` / `branch` |
| 写 action | `commit` / `add` / `stash` / `checkout` |
| 权限策略 | 只读自动放行（可并行），写操作 y/N 确认 |
| 执行方式 | 调 `git` 二进制（`exec.CommandContext`，30s 超时，同 shell 工具） |
| 不做 | reset、branch -d、cherry-pick、rebase 等高危操作（未来可加） |

## 架构

新文件 `internal/tool/git.go`，实现 `Tool` 接口（6 个方法）+ 辅助函数：

```
GitTool
  ├─ Execute(args)  → 解析 {action, ...} → dispatch
  ├─ 只读子函数：doStatus / doDiff / doLog / doShow / doBranch
  ├─ 写子函数：doAdd / doCommit / doStash / doCheckout
  ├─ runGit(args...) → exec.Command("git", args...) 封装
  │     └─ 30s 超时 + 退出码错误处理
  └─ 格式化辅助：formatStatus / formatLog / formatBranch
```

每个 action 一个独立子函数，可独立单测；与现有 `FileTool` 的 action 分派 + `GrepTool` 的格式化输出风格一致。

## 参数 Schema

`action` 必填，其余参数按 action 复用：

```json
{
  "action":       {"type": "string", "enum": ["status","diff","log","show","branch","add","commit","stash","checkout"]},
  "path":         {"type": "string", "description": "限制作用的路径，如单个文件"},
  "staged":       {"type": "boolean", "description": "仅 status/diff 使用：只看暂存区"},
  "max_count":    {"type": "integer", "description": "仅 log 使用：最多显示几条，默认 20"},
  "message":      {"type": "string", "description": "仅 commit 使用：提交信息"},
  "files":        {"type": "string", "description": "仅 add/checkout 使用：要操作的文件/路径"},
  "branch_name":  {"type": "string", "description": "仅 stash/checkout 使用：分支名"},
  "stash_action": {"type": "string", "enum": ["push","pop","list"], "description": "仅 stash 使用"}
}
```

## Action 行为

| action | 执行的命令 | 输出 |
|--------|------------|------|
| `status` | `git status --short` + `git status` | 紧凑变更列表（新增/修改/删除/重命名 + untracked） |
| `diff` | `git diff` / `git diff --staged` | unified diff，默认截断（ResultLimit 处理） |
| `log` | `git log --oneline -<max_count>` | `hash 日期 作者 主题` 列表 |
| `show` | `git show <commit>` | 提交详情 + diff |
| `branch` | `git branch -a` | 分支列表，当前分支标记 `*` |
| `add` | `git add <files>` | 确认信息 + 变更统计 |
| `commit` | `git commit -m <message>` | 提交确认 + 提交统计 |
| `stash` | `git stash push/pop/list` | 操作确认 |
| `checkout` | `git checkout <branch_name>` 或 `-- <files>` | 切换/恢复确认 |

## 参数优先级与默认值

- `add`：`files` 为空时 → `git add .`（暂存所有变更）
- `commit`：`message` 为空 → 报错 "commit 需要提供 message 参数"，不执行
- `show`：`path` 为空时视为 commit 引用（如 `HEAD`、`abc123`）；`path` 非空且能被解析为 commit 引用时按 commit 处理，否则报错
- `checkout`：`files` 非空 → `git checkout -- <files>`（恢复文件）；否则用 `branch_name` → `git checkout <branch_name>`（切分支）；两者都为空 → 报错，不执行。若 `branch_name` 与 `files` 同时提供，以 `files` 优先（恢复文件更安全）
- `stash`：`stash_action` 默认 `list`（只读，展示 stash 列表）；`push`/`pop` 为写操作需确认

## 权限

`CheckPermission` 解析 `action` 字段：

- 只读 5 个（status/diff/log/show/branch）→ `PermissionResult{Allow: true}`
- 写 4 个（commit/add/stash/checkout）→ `PermissionResult{Allow: false, Reason: "git 写操作需要确认"}`

## 并发安全

- 只读 action → `IsConcurrencySafe=true, IsReadOnly=true`（可并行执行）
- 写 action → 均 `false`（独占执行）
- 依据：只读 git 命令不写 `.git/index.lock`，并发安全；写命令全部排除在并行之外

## 错误处理

复用 `shell.go` 的错误模式：

- `runGit` 封装 `exec.CommandContext`，30s 超时
- 非零退出码 → 提取 stderr 作为错误返回给 LLM（保持 git 原文，如 `fatal: not a git repository`）
- 超时 → `命令超时 (30s)`
- 非 git 仓库 → 返回清晰的中文引导（帮助 LLM 意识到需要 cd 到仓库或用 shell 定位仓库）

## 测试计划（`internal/tool/git_test.go`）

用 `t.TempDir()` 建临时 git 仓库（`git init` + 配置 user.name/email），避免污染真实仓库：

| 测试 | 覆盖点 |
|------|--------|
| `TestGitStatus` | 新建/修改/删除文件 → status 输出含对应标记 |
| `TestGitDiff` | 修改文件 → diff 含 `+/-` 行；`staged:true` → 只看暂存区 |
| `TestGitLog` | 提交两次 → log 显示 2 条 |
| `TestGitCommit` | add + commit → 提交成功 + 输出确认 |
| `TestGitCheckout` | 分支切换 → HEAD 变化 |
| `TestGitNotARepo` | 在非 git 目录 → 友好错误提示 |
| `TestGitPermission` | 只读 action → Allow; 写 action → 需确认 |
| `TestGitConcurrency` | 只读 → safe/readonly; 写 → 均 false |

## 集成

- `main.go` 注册：`tools.Register(tool.NewGitTool())`
- CLAUDE.md 的 File Map 更新 `internal/tool/` 一节
- 完成标准：`go build ./...` + `go test ./...` 全绿
