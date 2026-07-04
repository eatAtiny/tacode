package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ShellTool 执行 shell 命令。
type ShellTool struct {
	timeout time.Duration
}

// NewShellTool 创建 Shell 工具，默认超时 30 秒。
func NewShellTool() *ShellTool {
	return &ShellTool{timeout: 30 * time.Second}
}

// ── Tool 接口：基础方法 ──

func (t *ShellTool) Name() string { return "shell" }

func (t *ShellTool) Description() string {
	return "执行 shell 命令并返回输出。可用于查看文件、运行程序、系统操作等。"
}

func (t *ShellTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "要执行的 shell 命令",
			},
		},
		"required": []string{"command"},
	}
}

func (t *ShellTool) Execute(args string) (string, error) {
	var params struct {
		Command string `json:"command"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}
	if strings.TrimSpace(params.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", params.Command)
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return result, fmt.Errorf("command timed out after %s", t.timeout)
		}
		return result, fmt.Errorf("command failed: %w\n%s", err, result)
	}
	return result, nil
}

// ── Tool 接口：权限内聚 ──

// CheckPermission 检查 shell 命令是否需要用户确认。
//
// 策略：
//   - 包含危险命令模式（rm -rf、sudo、chmod 777 等）→ 需确认
//   - 其他命令 → 直接允许
//
// 危险模式列表来自原有的 isDangerousShellCommand，现内移到工具自身。
func (t *ShellTool) CheckPermission(args string) PermissionResult {
	if isDangerousShellCommand(args) {
		return PermissionResult{
			Allow:  false,
			Reason: "该命令可能有风险，需要确认执行",
		}
	}
	return PermissionResult{Allow: true}
}

// isDangerousShellCommand 检查是否是危险的 shell 命令。
//
// 从 internal/agent/permission.go 移入。
// 检测方式：解析 JSON 参数，提取 command 字段，
// 与危险命令模式列表进行子串匹配（大小写不敏感）。
func isDangerousShellCommand(args string) bool {
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return false
	}

	command, ok := params["command"].(string)
	if !ok {
		return false
	}

	// 危险命令列表（子串匹配，大小写不敏感）。
	dangerousCommands := []string{
		"rm -rf",
		"rm -r",
		"mkfs",
		"dd if=",
		"chmod 777",
		"chown",
		"sudo",
		"su ",
		"passwd",
		"useradd",
		"userdel",
		"groupadd",
		"groupdel",
		"kill -9",
		"pkill",
		"shutdown",
		"reboot",
		"halt",
		"poweroff",
	}

	commandLower := strings.ToLower(command)
	for _, dangerous := range dangerousCommands {
		if strings.Contains(commandLower, dangerous) {
			return true
		}
	}

	return false
}

// ── Tool 接口：Prompt 自引导 ──

// PromptGuide 返回 shell 工具的使用引导。
func (t *ShellTool) PromptGuide() string {
	return "优先使用 grep、list 等专用工具代替 shell 命令进行搜索和目录浏览。" +
		"shell 的默认超时为 30 秒，长时间任务会超时失败。"
}

// ── Tool 接口：结果上限 ──

// ResultLimit shell 输出上限 16000 字符。
func (t *ShellTool) ResultLimit() int { return 16000 }
