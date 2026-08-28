package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"agentic/internal/sandbox"
)

// defaultCmdTimeout 命令执行默认超时（shell/git 工具共享）。
const defaultCmdTimeout = 30 * time.Second

// ShellTool 执行 shell 命令。
type ShellTool struct {
	timeout  time.Duration
	sandbox  sandbox.Sandbox // nil = 无沙箱（默认，向后兼容）
	approved map[string]bool // 已确认放行网络的 args（key=原始 args JSON）
}

// NewShellTool 创建无沙箱 shell 工具（默认，向后兼容）。
func NewShellTool() *ShellTool {
	return &ShellTool{timeout: defaultCmdTimeout, approved: make(map[string]bool)}
}

// NewShellToolWithSandbox 创建带沙箱的 shell 工具。
func NewShellToolWithSandbox(sb sandbox.Sandbox) *ShellTool {
	return &ShellTool{timeout: defaultCmdTimeout, sandbox: sb, approved: make(map[string]bool)}
}

// AllowNetworkFor 记录已确认放行网络的命令参数。
// queryLoop 在用户确认 network:true 请求后调用，Execute 据此重建 allow-network 沙箱。
// 这是 ShellTool 特有方法，通过类型断言被 queryLoop 发现。
func (t *ShellTool) AllowNetworkFor(args string) {
	if t.approved == nil {
		t.approved = make(map[string]bool)
	}
	t.approved[args] = true
}

// ── Tool 接口：基础方法 ──

func (t *ShellTool) Name() string      { return "shell" }
func (t *ShellTool) Aliases() []string { return nil }

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
			"network": map[string]any{
				"type":        "boolean",
				"description": "是否需要网络访问（curl 等）。默认 false（沙箱禁止网络时需确认）",
			},
		},
		"required": []string{"command"},
	}
}

func (t *ShellTool) Execute(args string) (string, error) {
	var params struct {
		Command string `json:"command"`
		Network bool   `json:"network"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", err
	}
	if strings.TrimSpace(params.Command) == "" {
		return "", fmt.Errorf("command is empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), t.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", "-c", params.Command)
	if t.sandbox != nil {
		if params.Network && t.approved[args] {
			// 用户已确认放行网络：重建 AllowNetwork:true 的沙箱。
			// 注意：该临时沙箱的 Close() 不会被调用（仅 main.go 持有的原始沙箱会被关闭），
			// 每次确认网络命令会泄漏一个临时 profile 文件（体积很小，OS 会清理 /tmp），
			// 可接受；本任务不接线 per-command 沙箱的 Close。
			sb := sandbox.NewSandbox(sandbox.Config{AllowNetwork: true})
			cmd = sb.Wrap(cmd)
		} else {
			cmd = t.sandbox.Wrap(cmd)
		}
	}
	return runCmd(ctx, cmd, "命令", "", t.timeout)
}

// ──────────────────────────────────────────────────────────
// 共享命令执行辅助（shell/git 工具复用）
// ──────────────────────────────────────────────────────────

// runCmd 执行 cmd 并返回 TrimSpace 后的合并输出（stdout + stderr），
// 统一包装超时与非零退出码错误：
//   - 超时 → "<label>超时 (<timeout>)"，timeoutCmd 非空时附命令回显
//   - 非零退出码 → "<label>失败 (退出码 N)"，附输出或「无输出」
//
// label 是错误前缀（shell 传 "命令"，git 传 "git 命令"）；
// timeoutCmd 是超时错误中回显给用户的命令（空串表示不回显）。
func runCmd(ctx context.Context, cmd *exec.Cmd, label, timeoutCmd string, timeout time.Duration) (string, error) {
	output, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(output))

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			if timeoutCmd != "" {
				return "", fmt.Errorf("%s超时 (%s): %s", label, timeout, timeoutCmd)
			}
			return "", fmt.Errorf("%s超时 (%s)", label, timeout)
		}
		if result != "" {
			return "", fmt.Errorf("%s失败 (退出码 %d):\n%s", label, exitCodeOf(err), result)
		}
		return "", fmt.Errorf("%s失败 (退出码 %d)，无输出", label, exitCodeOf(err))
	}
	return result, nil
}

// ── Tool 接口：权限内聚 ──

// CheckPermission 检查 shell 命令是否需要用户确认。
//
// 策略（危险检查优先级最高，即使已放行网络也需每次确认）：
//   - 包含危险命令模式（rm -rf、sudo、chmod 777 等）→ 需确认
//   - 沙箱开启且请求 network=true → 需确认（网络访问是风险操作）
//   - 其他命令 → 直接允许
func (t *ShellTool) CheckPermission(args string) PermissionResult {
	// 危险命令检查优先级最高：即使已放行网络，危险命令仍需每次确认。
	if isDangerousShellCommand(args) {
		return PermissionResult{
			Allow:  false,
			Reason: "该命令可能有风险，需要确认执行",
		}
	}
	if t.sandbox != nil {
		var params struct {
			Network bool `json:"network"`
		}
		if err := parseArgs(args, &params); err == nil && params.Network {
			// 已确认放行（AllowNetworkFor 已记录）→ 允许。
			if t.approved[args] {
				return PermissionResult{Allow: true}
			}
			return PermissionResult{
				Allow:  false,
				Reason: "需要网络访问，请确认",
			}
		}
	}
	return PermissionResult{Allow: true}
}

// isDangerousShellCommand 检查是否是危险的 shell 命令。
//
// 从 internal/agent/permission.go 移入。
// 检测方式：解析 JSON 参数，提取 command 字段，对空白做归一化
// （strings.Fields 按空白切分后重组，多个空格/制表符归为一个空格），
// 再与危险命令模式列表进行子串匹配（大小写不敏感）。
//
// 归一化防止 "rm  -rf"（多空格）、"rm\t-rf"（制表符）绕过子串匹配。
// 注意：变量拼接（"x=rm; $x -rf"）等间接构造无法静态识别——fail-open
// 可接受（该命令已不属于只读，走串行 + 权限确认路径兜底）。
func isDangerousShellCommand(args string) bool {
	var params map[string]interface{}
	if err := parseArgs(args, &params); err != nil {
		return false
	}

	command, ok := params["command"].(string)
	if !ok {
		return false
	}

	// 空白归一化：任意连续空白（含制表符）→ 单个空格。
	normalized := strings.Join(strings.Fields(command), " ")

	// 危险命令列表（归一化后子串匹配，大小写不敏感）。
	// "rm " 前缀已覆盖 "rm -rf"/"rm -r" 等所有 rm 变体，无需单列。
	dangerousCommands := []string{
		"rm ", // 普通 rm 删除也需确认（如 "rm /tmp/foo"）；尾随空格避免子串误伤 rmdir/warmup/firmware 等
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

	commandLower := strings.ToLower(normalized)
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
//
// 注意：sed/awk 因支持 -i 就地修改与输出重定向，curl/wget 因有网络副作用
// （外发请求），均不列入（即使配合重定向检查也可能漏网，fail-closed）。
var readOnlyCommands = []string{
	"ls", "cat", "head", "tail", "less", "more",
	"grep", "egrep", "fgrep", "find", "locate",
	"wc", "sort", "uniq", "cut", "tr",
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
}

// isReadOnlyShellCommand 检查 shell 命令是否为只读操作。
//
// 策略（按序检查，任一命中 → false）：
//  1. 解析 JSON 参数提取 command 字段
//  2. 命令串包含 ">"（任何重定向：>、>>、2>、2>&1）→ 一律 false。
//     旧实现按整串包含做「含 2> 则豁免 > 检查」，被 "cat a > b 2>&1"
//     类命令利用任意位置的 2> 绕过（已实证），故收紧为见 > 即非只读。
//  3. 包含 "|" → 按 | 拆段，每段 TrimSpace 后必须命中白名单前缀，
//     否则 false。旧实现只校验首段，管道右侧完全不受限，
//     "cat f | tee g" 因此被误判只读（已实证）。
//  4. 其余：整串按白名单前缀判断（与管道分段同一规则）。
//
// fail-closed：任何不确定的命令（解析失败、空段、白名单未命中、
// 含重定向）都返回 false（不安全），并发绿色通道只在全部规则通过时
// 开放。已知存量缺口（前缀白名单模型固有）：; / && / 换行命令链、
// $() 与反引号命令替换、白名单命令自身的写参数（如 sort -o）不在
// 本函数解析范围内。
func isReadOnlyShellCommand(args string) bool {
	var params map[string]interface{}
	if err := parseArgs(args, &params); err != nil {
		return false // 解析失败 → 不安全
	}

	command, ok := params["command"].(string)
	if !ok || command == "" {
		return false
	}

	cmdLower := strings.ToLower(strings.TrimSpace(command))

	// 任何重定向 → 一律非只读。纯 stderr 丢弃（2>/dev/null）实际无
	// 副作用，此处一并收紧，换取判定规则的可证明性（整串包含式的
	// 条件豁免已被实证可绕过，不再使用）。
	if strings.Contains(cmdLower, ">") {
		return false
	}

	// 管道：逐段白名单校验，任一段不命中 → 非只读。
	if strings.Contains(cmdLower, "|") {
		for _, seg := range strings.Split(cmdLower, "|") {
			if !matchesReadOnlyPrefix(strings.TrimSpace(seg)) {
				return false
			}
		}
		return true
	}

	return matchesReadOnlyPrefix(cmdLower)
}

// matchesReadOnlyPrefix 检查单条裸命令段（不含重定向/管道）是否以
// readOnlyCommands 白名单前缀开头。前缀后必须是串尾或空白字符，
// 防止 "lsfoo" 误命中 "ls"。
func matchesReadOnlyPrefix(cmdLower string) bool {
	if cmdLower == "" {
		return false
	}
	for _, safe := range readOnlyCommands {
		if strings.HasPrefix(cmdLower, safe) {
			remaining := cmdLower[len(safe):]
			if remaining == "" || remaining[0] == ' ' || remaining[0] == '\t' {
				return true
			}
		}
	}
	return false
}

// ── Tool 接口：结果上限 ──

// ResultLimit shell 输出上限 16000 字符。
func (t *ShellTool) ResultLimit() int { return 16000 }
