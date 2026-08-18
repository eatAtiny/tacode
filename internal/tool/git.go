package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// gitTimeout git 命令默认执行超时。
const gitTimeout = 30 * time.Second

// GitTool 提供结构化的 git 版本控制操作。
//
// 与 shell 手搓 git 命令的区别：
//   - action 参数限定操作集合（只读 5 个 + 写 4 个），避免 LLM 拼错 git 子命令
//   - 只读操作自动放行并可并行执行；写操作（add/commit/stash/checkout）需要用户确认
//   - status/log/branch 输出格式化为 LLM 易读的紧凑格式
//
// 高危操作（reset、rebase、branch -d 等）首版不开放。
type GitTool struct {
	timeout time.Duration
	dir     string // 命令执行目录，空 = 当前工作目录
}

// NewGitTool 创建 git 工具，默认超时 30 秒。
func NewGitTool() *GitTool {
	return &GitTool{timeout: gitTimeout}
}

// NewGitToolIn 创建 git 工具并固定执行目录（测试或子目录场景使用）。
func NewGitToolIn(dir string) *GitTool {
	return &GitTool{timeout: gitTimeout, dir: dir}
}

// ── Tool 接口：基础方法 ──

func (t *GitTool) Name() string      { return "git" }
func (t *GitTool) Aliases() []string { return nil }

func (t *GitTool) Description() string {
	return "执行 git 版本控制操作。只读: status/diff/log/show/branch；写操作: add/commit/stash/checkout（需确认）。"
}

func (t *GitTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "要执行的 git 操作",
				"enum":        []string{"status", "diff", "log", "show", "branch", "add", "commit", "stash", "checkout"},
			},
			"path": map[string]any{
				"type":        "string",
				"description": "限制作用的路径，如单个文件",
			},
			"staged": map[string]any{
				"type":        "boolean",
				"description": "仅 status/diff 使用：只看暂存区",
			},
			"max_count": map[string]any{
				"type":        "integer",
				"description": "仅 log 使用：最多显示几条，默认 20",
			},
			"message": map[string]any{
				"type":        "string",
				"description": "commit 的提交信息 / stash push 的备注",
			},
			"files": map[string]any{
				"type":        "string",
				"description": "add 要暂存的文件（空格分隔，空=全部）/ checkout 要恢复的文件",
			},
			"branch_name": map[string]any{
				"type":        "string",
				"description": "checkout 要切换的分支名",
			},
			"stash_action": map[string]any{
				"type":        "string",
				"description": "stash 操作：list（默认，只读）/ push / pop",
				"enum":        []string{"push", "pop", "list"},
			},
		},
		"required": []string{"action"},
	}
}

func (t *GitTool) Execute(args string) (string, error) {
	var params struct {
		Action      string `json:"action"`
		Path        string `json:"path"`
		Staged      bool   `json:"staged"`
		MaxCount    int    `json:"max_count"`
		Message     string `json:"message"`
		Files       string `json:"files"`
		BranchName  string `json:"branch_name"`
		StashAction string `json:"stash_action"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	params.Action = strings.TrimSpace(params.Action)
	if params.Action == "" {
		return "", fmt.Errorf("action 必填 (status/diff/log/show/branch/add/commit/stash/checkout)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()

	switch params.Action {
	case "status":
		return t.doStatus(ctx, params.Path, params.Staged)
	case "diff":
		return t.doDiff(ctx, params.Path, params.Staged)
	case "log":
		return t.doLog(ctx, params.MaxCount)
	case "show":
		return t.doShow(ctx, params.Path)
	case "branch":
		return t.doBranch(ctx)
	case "add":
		return t.doAdd(ctx, params.Files)
	case "commit":
		return t.doCommit(ctx, params.Message)
	case "stash":
		return t.doStash(ctx, params.StashAction, params.Message)
	case "checkout":
		return t.doCheckout(ctx, params.BranchName, params.Files)
	default:
		return "", fmt.Errorf("未知 action: %s", params.Action)
	}
}

// ──────────────────────────────────────────────────────────
// runGit — git 命令执行封装
// ──────────────────────────────────────────────────────────

// runGit 执行 git 命令，返回合并输出（stdout + stderr）。
// 非零退出码通过 error 返回，包含 git 原始错误信息。
func (t *GitTool) runGit(ctx context.Context, args ...string) (string, error) {
	args = append([]string{"-c", "core.quotePath=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	if t.dir != "" {
		cmd.Dir = t.dir
	}
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git 命令超时 (%s): git %s", gitTimeout, strings.Join(args, " "))
		}
		if result != "" {
			return "", fmt.Errorf("git 命令失败 (退出码 %d):\n%s", exitCodeOf(err), result)
		}
		return "", fmt.Errorf("git 命令失败 (退出码 %d)，无输出", exitCodeOf(err))
	}
	return result, nil
}

// exitCodeOf 提取命令退出码，无法提取时返回 -1。
func exitCodeOf(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// gitError 包装 git 命令错误，对非 git 仓库给出中文引导。
func (t *GitTool) gitError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "not a git repository") {
		return fmt.Errorf("当前目录不是 git 仓库。请在仓库目录内使用 git 工具，或用 shell 的 cd 命令先进入仓库目录。原始错误: %v", err)
	}
	return err
}

// ──────────────────────────────────────────────────────────
// 只读操作（自动放行）
// ──────────────────────────────────────────────────────────

func (t *GitTool) doStatus(ctx context.Context, path string, staged bool) (string, error) {
	args := []string{"status", "--short", "--no-color"}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := t.runGit(ctx, args...)
	if err != nil {
		return "", t.gitError(err)
	}
	if out == "" {
		return "工作区干净，无未提交变更。", nil
	}
	return formatStatus(out, staged), nil
}

// formatStatus 将 git status --short 输出格式化为分组的变更列表。
// staged=true 时只保留已暂存条目（X 列非空且非 '?'）。
func formatStatus(raw string, staged bool) string {
	var added, modified, deleted, renamed, untracked []string

	for _, line := range strings.Split(raw, "\n") {
		if len(line) < 3 {
			continue
		}
		xy := line[:2]
		file := line[3:]

		// staged=true：只看暂存区（X 列为空格或 ? 的跳过）。
		if staged && (xy[0] == ' ' || xy[0] == '?') {
			continue
		}

		switch {
		case xy[0] == '?' || xy[1] == '?':
			untracked = append(untracked, file)
		case xy[0] == 'R':
			renamed = append(renamed, file)
		case xy[0] == 'D' || xy[1] == 'D':
			deleted = append(deleted, file)
		case xy[0] == 'A' || xy[1] == 'A':
			added = append(added, file)
		default:
			modified = append(modified, file)
		}
	}

	var sb strings.Builder
	writeGroup := func(label string, items []string) {
		if len(items) == 0 {
			return
		}
		fmt.Fprintf(&sb, "%s (%d):\n", label, len(items))
		for _, f := range items {
			fmt.Fprintf(&sb, "  %s\n", f)
		}
	}

	writeGroup("新增", added)
	writeGroup("修改", modified)
	writeGroup("删除", deleted)
	writeGroup("重命名", renamed)
	writeGroup("未跟踪", untracked)

	return strings.TrimRight(sb.String(), "\n")
}

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

func (t *GitTool) doLog(ctx context.Context, maxCount int) (string, error) {
	if maxCount <= 0 {
		maxCount = 20
	}
	out, err := t.runGit(ctx,
		"log", "-n", fmt.Sprintf("%d", maxCount),
		"--format=%h %ad %an %s", "--date=short",
	)
	if err != nil {
		return "", t.gitError(err)
	}
	if out == "" {
		return "仓库没有提交记录。", nil
	}
	return out, nil
}

func (t *GitTool) doShow(ctx context.Context, commit string) (string, error) {
	if strings.TrimSpace(commit) == "" {
		commit = "HEAD"
	}
	out, err := t.runGit(ctx, "show", "--no-color", commit)
	if err != nil {
		return "", t.gitError(err)
	}
	return out, nil
}

func (t *GitTool) doBranch(ctx context.Context) (string, error) {
	out, err := t.runGit(ctx, "branch", "-a")
	if err != nil {
		return "", t.gitError(err)
	}
	if out == "" {
		return "当前仓库没有任何分支。", nil
	}
	return out, nil
}

// ──────────────────────────────────────────────────────────
// 写操作（需确认）
// ──────────────────────────────────────────────────────────

func (t *GitTool) doAdd(ctx context.Context, files string) (string, error) {
	args := []string{"add"}
	if strings.TrimSpace(files) == "" {
		args = append(args, ".")
	} else {
		args = append(args, strings.Fields(files)...)
	}
	if _, err := t.runGit(ctx, args...); err != nil {
		return "", t.gitError(err)
	}

	// 返回暂存后的状态摘要，帮助 LLM 确认结果。
	status, statusErr := t.runGit(ctx, "status", "--short", "--no-color")
	if statusErr != nil || status == "" {
		return "✅ 已暂存变更。", nil
	}
	return "✅ 已暂存变更，当前暂存区:\n" + formatStatus(status, true), nil
}

func (t *GitTool) doCommit(ctx context.Context, message string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", fmt.Errorf("commit 需要提供 message 参数")
	}
	out, err := t.runGit(ctx, "commit", "-m", message)
	if err != nil {
		return "", t.gitError(err)
	}
	return out, nil
}

func (t *GitTool) doStash(ctx context.Context, stashAction, message string) (string, error) {
	switch stashAction {
	case "push":
		args := []string{"stash", "push"}
		if strings.TrimSpace(message) != "" {
			args = append(args, "-m", message)
		}
		out, err := t.runGit(ctx, args...)
		if err != nil {
			return "", t.gitError(err)
		}
		return "✅ 已保存到 stash。" + prefixOut(out), nil
	case "pop":
		out, err := t.runGit(ctx, "stash", "pop")
		if err != nil {
			return "", t.gitError(err)
		}
		return "✅ 已从 stash 恢复。" + prefixOut(out), nil
	default: // list（含空值，只读）
		out, err := t.runGit(ctx, "stash", "list")
		if err != nil {
			return "", t.gitError(err)
		}
		if out == "" {
			return "stash 列表为空。", nil
		}
		return out, nil
	}
}

func (t *GitTool) doCheckout(ctx context.Context, branchName, files string) (string, error) {
	// files 优先：恢复文件比切分支更安全。
	if strings.TrimSpace(files) != "" {
		args := []string{"checkout", "--"}
		args = append(args, strings.Fields(files)...)
		out, err := t.runGit(ctx, args...)
		if err != nil {
			return "", t.gitError(err)
		}
		return "✅ 已恢复文件。" + prefixOut(out), nil
	}
	if strings.TrimSpace(branchName) == "" {
		return "", fmt.Errorf("checkout 需要提供 branch_name（切分支）或 files（恢复文件）")
	}
	out, err := t.runGit(ctx, "checkout", branchName)
	if err != nil {
		return "", t.gitError(err)
	}
	return "✅ 已切换到分支 " + branchName + "。" + prefixOut(out), nil
}

// prefixOut 为空输出加换行前缀（拼接消息时避免粘连）。
func prefixOut(out string) string {
	if out == "" {
		return ""
	}
	return "\n" + out
}

// ──────────────────────────────────────────────────────────
// Tool 接口：权限内聚 / Prompt 自引导 / 并发安全 / 结果上限
// ──────────────────────────────────────────────────────────

// CheckPermission 只读 action 直接允许，写 action 需要确认。
func (t *GitTool) CheckPermission(args string) PermissionResult {
	if isGitReadAction(args) {
		return PermissionResult{Allow: true}
	}
	return PermissionResult{Allow: false, Reason: "git 写操作需要确认"}
}

// isGitReadAction 判断 args 对应的操作是否只读。
// 只读: status/diff/log/show/branch + stash list（默认）。
func isGitReadAction(args string) bool {
	var params struct {
		Action      string `json:"action"`
		StashAction string `json:"stash_action"`
	}
	if err := parseArgs(args, &params); err != nil {
		return false // fail-closed
	}
	switch params.Action {
	case "status", "diff", "log", "show", "branch":
		return true
	case "stash":
		return params.StashAction == "" || params.StashAction == "list"
	default:
		return false
	}
}

// PromptGuide 返回 git 工具的使用引导。
func (t *GitTool) PromptGuide() string {
	return "优先使用此工具而非 shell 手搓 git 命令。" +
		"只读操作（status/diff/log/show/branch）自动执行；写操作（add/commit/stash/checkout）会请求确认。" +
		"提交前先用 status 和 diff 检查变更；diff 的 staged=true 查看已暂存变更。" +
		"stash 默认 list（只读），push 保存、pop 恢复。"
}

// IsConcurrencySafe 只读操作可并行，写操作独占。
func (t *GitTool) IsConcurrencySafe(args string) bool { return isGitReadAction(args) }

// IsReadOnly 只读操作无副作用。
func (t *GitTool) IsReadOnly(args string) bool { return isGitReadAction(args) }

// ResultLimit git 输出（diff/show 等）上限 12000 字符。
func (t *GitTool) ResultLimit() int { return 12000 }
