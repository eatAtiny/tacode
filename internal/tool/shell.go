package tool

import (
	"context"
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
