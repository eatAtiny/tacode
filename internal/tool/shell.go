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
			return "", fmt.Errorf("命令超时 (%s)", t.timeout)
		}
		// 提取退出码，让 LLM 能看到具体的失败原因。
		exitCode := -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		if result != "" {
			return "", fmt.Errorf("命令失败 (退出码 %d):\n%s", exitCode, result)
		}
		return "", fmt.Errorf("命令失败 (退出码 %d)，无输出", exitCode)
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
		"shell 的默认超时为 30 秒，长时间任务会超时失败。" +
		"执行创建/删除/修改等操作时，务必使用 && echo 输出确认信息，" +
		"例如: rm -rf dir && echo '已删除' 或 mkdir -p a/b && echo '目录已创建'。" +
		"否则命令成功时无输出，你将无法判断任务是否完成。"
}

// ── Tool 接口：并发安全 ──

// IsConcurrencySafe shell 的并发安全取决于具体命令。
//
// 只读命令（ls、cat、grep、find、head、tail、wc 等）可以并发执行。
// 有副作用的命令（rm、mv、写入、修改）必须独占执行。
//
// 通过解析参数中的 command 字段，与已知安全命令前缀匹配。
func (t *ShellTool) IsConcurrencySafe(args string) bool {
	return isReadOnlyShellCommand(args)
}

// IsReadOnly shell 的只读性取决于具体命令。
func (t *ShellTool) IsReadOnly(args string) bool {
	return isReadOnlyShellCommand(args)
}

// readOnlyCommands 已知的只读 Shell 命令前缀列表。
//
// 这些命令只读取信息，不会修改文件系统或系统状态。
// 匹配策略：提取命令的第一个词，去除路径前缀，与列表比较。
var readOnlyCommands = []string{
	"ls", "cat", "head", "tail", "less", "more",
	"grep", "egrep", "fgrep", "find", "locate",
	"wc", "sort", "uniq", "cut", "tr", "awk", "sed",
	"echo", "printf", "date", "pwd", "whoami", "id",
	"uname", "hostname", "which", "type", "command",
	"env", "printenv", "df", "du", "free", "uptime",
	"ps", "pgrep", "top", "htop", "lsof", "stat",
	"file", "readlink", "realpath", "basename", "dirname",
	"diff", "cmp", "comm", "join", "paste",
	"tar -t", "zipinfo", "unzip -l",
	"git log", "git show", "git diff", "git status", "git branch",
	"go test", "go vet", "go list", "go doc", "go version", "go env",
	"docker ps", "docker images", "docker inspect", "docker logs",
	"curl", "wget",
}

// isReadOnlyShellCommand 检查 shell 命令是否为只读操作。
//
// 策略：
//  1. 解析 JSON 参数提取 command 字段
//  2. 去掉前导空格
//  3. 检查是否以只读命令前缀开头
//
// fail-closed：任何不确定的命令都返回 false（不安全）。
func isReadOnlyShellCommand(args string) bool {
	var params map[string]interface{}
	if err := parseArgs(args, &params); err != nil {
		return false // 解析失败 → 不安全
	}

	command, ok := params["command"].(string)
	if !ok || command == "" {
		return false
	}

	cmd := strings.TrimSpace(command)
	cmdLower := strings.ToLower(cmd)

	for _, safe := range readOnlyCommands {
		if strings.HasPrefix(cmdLower, safe) {
			// 额外的安全检查：避免误匹配
			// 例如 "cat file" 是安全的，但 "cat file > other" 不是
			remaining := cmdLower[len(safe):]
			if remaining == "" || remaining[0] == ' ' || remaining[0] == '\t' {
				// 检查是否有重定向或管道写入
				if strings.Contains(cmdLower, ">") && !strings.Contains(cmdLower, "2>") {
					// 有输出重定向（排除 stderr 重定向）→ 不是只读
					// 但 "git log > /dev/null" 实际无副作用，此处简化处理
					return false
				}
				return true
			}
		}
	}

	return false
}

// ── Tool 接口：结果上限 ──

// ResultLimit shell 输出上限 16000 字符。
func (t *ShellTool) ResultLimit() int { return 16000 }
