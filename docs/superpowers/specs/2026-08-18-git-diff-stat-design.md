# Git diff --stat 增强设计文档

- 日期：2026-08-18
- 状态：已批准
- 范围：`internal/tool/git.go` 的 `doDiff` 加 `stat` 参数 + `internal/tool/git_test.go` 加测试 + CLAUDE.md 无需改（git 工具描述不变）

## 背景

`doDiff`（`internal/tool/git.go`）目前只输出完整 unified diff。大改动（如批量重构）容易撞 `ResultLimit`（12000 字符）被截断，LLM 看不到全貌。Coding Agent 审阅大改动的正确姿势是：先看 `--stat` 概览 → 再钻取单个文件。

## 决策

| 决策点 | 结论 |
|--------|------|
| 参数 | `stat: boolean`，仅 diff 使用 |
| 行为 | `stat=true` → `git diff --stat`（文件级统计）；默认不变（完整 diff） |
| 权限/并发 | 不变（stat 是只读参数，`isGitReadAction` 无需改） |

## 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/tool/git.go` | `Parameters()` 加 `stat` 字段；`Execute` 解析 `Stat`；`doDiff` 签名加 `stat` 参数 + 分支 |
| `internal/tool/git_test.go` | 加 `TestGitDiff_Stat` |

## 实现

### Parameters() 加 stat 字段

在 `staged` 字段后加：

```json
"stat": {
	"type":        "boolean",
	"description": "仅 diff 使用：只看文件级变更统计（X files changed, +N/-N），不看具体 diff",
}
```

### Execute 分派

`params` 加 `Stat bool \`json:"stat"\``；`case "diff":` 改为 `t.doDiff(ctx, params.Path, params.Staged, params.Stat)`。

### doDiff 分支

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

输出示例（`git diff --stat`）：

```
 file.go | 10 +++++-----
 1 file changed, 6 insertions(+), 4 deletions(-)
```

## 测试计划

| 测试 | 覆盖点 |
|------|--------|
| `TestGitDiff_Stat` | 修改文件 → `stat:true` 输出含 `files changed` 和 `insertions`；`stat:false` 输出含 `+`/`-` 行 |

## 集成

- 完成标准：`go build ./...` + `go test ./...` 全绿
