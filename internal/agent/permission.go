package agent

import (
	"encoding/json"
	"strings"
)

// ──────────────────────────────────────────────────────────
// 权限检测系统
//
// 调用链：
//   queryLoop.executeSingleTool()
//     → checkToolPermission(toolName, args)          ← 入口函数
//       → globalPermissionChecker.CheckPermission()  ← 默认实现
//         → isHighRiskOperation()                   ← 危险操作检测
//           → isDangerousShellCommand()             ← shell 危险模式匹配
//           → isDangerousFileOperation()            ← file 写操作检测
//     → [deny]  yield 错误工具结果，跳过
//     → [allow] 继续执行工具
//     → [confirm] yield Permission 事件 → 阻塞等待用户决策 → 继续或拒绝
// ──────────────────────────────────────────────────────────

// PermissionAction 权限动作类型。
type PermissionAction string

const (
	PermissionAllow   PermissionAction = "allow"   // 允许执行（安全工具或低风险操作）
	PermissionDeny    PermissionAction = "deny"    // 拒绝执行（禁止的工具）
	PermissionConfirm PermissionAction = "confirm" // 需要用户确认（危险工具）
)

// PermissionResult 权限检测结果。
type PermissionResult struct {
	Action  PermissionAction // 权限动作
	Message string           // 附加消息（deny 或 confirm 时展示给用户）
	Reason  string           // 原因说明（内部标识，如 "forbidden_tool"、"high_risk_operation"）
}

// ToolPermissionChecker 工具权限检查器接口。
//
// 实现者需要根据工具名称和参数判断权限：
//   - 禁止的工具 → 返回 PermissionDeny
//   - 危险但可确认的工具 → 返回 PermissionConfirm
//   - 安全的工具 → 返回 PermissionAllow
//
// 扩展方向：
//   - 基于用户角色的权限控制
//   - 基于资源路径的权限控制
//   - 权限缓存机制
//   - 权限审计日志
type ToolPermissionChecker interface {
	// CheckPermission 检查工具调用权限。
	// toolName: 工具名称（如 "shell"、"file"）
	// args: 工具参数（JSON 字符串）
	CheckPermission(toolName string, args string) PermissionResult
}

// DefaultPermissionChecker 默认权限检查器。
//
// 权限策略（简单规则）：
//   - 禁止列表中的工具 → deny（不可执行）
//   - 危险列表中的工具 → confirm（需用户确认）
//   - 其他工具 → allow（直接执行）
//
// 高风险操作细分：
//   - shell: 匹配危险命令模式（rm -rf、sudo、chmod 777 等）
//   - file: 写操作（write/delete）需确认，读操作直接允许
type DefaultPermissionChecker struct {
	// DangerousTools 危险工具列表（执行前需要用户确认）
	DangerousTools []string
	// ForbiddenTools 禁止的工具列表（不可执行）
	ForbiddenTools []string
}

// NewDefaultPermissionChecker 创建默认权限检查器。
// 默认危险工具：shell、file（需要确认）
// 默认禁止工具：无
func NewDefaultPermissionChecker() *DefaultPermissionChecker {
	return &DefaultPermissionChecker{
		DangerousTools: []string{"shell", "file"},
		ForbiddenTools: []string{},
	}
}

// CheckPermission 检查工具调用权限（实现 ToolPermissionChecker 接口）。
//
// 检查顺序：
//   1. 先检查禁止列表 → deny（优先级最高）
//   2. 再检查危险列表 → 进一步判断是否高风险操作
//      - 高风险 → confirm（如 rm -rf、文件写操作）
//      - 普通危险 → confirm（如 ls、cat 等安全的 shell 命令）
//   3. 不在任何列表中 → allow
func (c *DefaultPermissionChecker) CheckPermission(toolName string, args string) PermissionResult {
	// ── 步骤 1: 检查禁止的工具 ──
	for _, forbidden := range c.ForbiddenTools {
		if toolName == forbidden {
			return PermissionResult{
				Action:  PermissionDeny,
				Message: "该工具已被禁止使用",
				Reason:  "forbidden_tool",
			}
		}
	}

	// ── 步骤 2: 检查危险的工具 ──
	for _, dangerous := range c.DangerousTools {
		if toolName == dangerous {
			// 进一步分析参数，判断是否是高风险操作。
			if isHighRiskOperation(toolName, args) {
				return PermissionResult{
					Action:  PermissionConfirm,
					Message: "该操作可能有风险，需要确认",
					Reason:  "high_risk_operation",
				}
			}
			return PermissionResult{
				Action:  PermissionConfirm,
				Message: "该工具需要确认执行",
				Reason:  "dangerous_tool",
			}
		}
	}

	// ── 步骤 3: 安全工具，直接允许 ──
	return PermissionResult{
		Action: PermissionAllow,
		Reason: "safe_tool",
	}
}

// isHighRiskOperation 检查是否是高风险操作。
// 根据工具类型分发到对应的检测函数。
func isHighRiskOperation(toolName string, args string) bool {
	switch toolName {
	case "shell":
		return isDangerousShellCommand(args)
	case "file":
		return isDangerousFileOperation(args)
	default:
		return false
	}
}

// isDangerousShellCommand 检查是否是危险的 shell 命令。
//
// 检测方式：解析 JSON 参数，提取 command 字段，
// 与危险命令模式列表进行子串匹配（大小写不敏感）。
//
// 危险命令模式包括：
//   - 文件破坏: rm -rf, rm -r, dd if=
//   - 权限修改: chmod 777, chown, sudo
//   - 用户管理: passwd, useradd, userdel, groupadd, groupdel
//   - 进程终止: kill -9, pkill
//   - 系统控制: shutdown, reboot, halt, poweroff
//   - 文件系统: mkfs
func isDangerousShellCommand(args string) bool {
	// 解析参数。
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

// isDangerousFileOperation 检查是否是危险的文件操作。
//
// 检测方式：解析 JSON 参数，提取 action 字段。
// 写操作（write/delete）需要确认，读操作直接允许。
func isDangerousFileOperation(args string) bool {
	// 解析参数。
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return false
	}

	// 检查是否是写操作。
	action, ok := params["action"].(string)
	if !ok {
		return false
	}

	// 写操作和删除操作需要确认。
	if action == "write" || action == "delete" {
		return true
	}

	return false
}

// ──────────────────────────────────────────────────────────
// 全局权限检查器
// ──────────────────────────────────────────────────────────

// globalPermissionChecker 全局权限检查器实例。
// 通过 init() 初始化为 DefaultPermissionChecker。
var globalPermissionChecker ToolPermissionChecker

// init 初始化全局权限检查器为默认实现。
func init() {
	globalPermissionChecker = NewDefaultPermissionChecker()
}

// checkToolPermission 检查工具调用权限（queryLoop 调用的入口函数）。
//
// 这是 queryLoop → 权限系统的桥梁：
//
//	queryLoop.executeSingleTool()
//	  → checkToolPermission(toolName, args)
//	    → globalPermissionChecker.CheckPermission()
//	    → 返回 PermissionResult{Action: deny|allow|confirm}
//
// 扩展接口：
//   - 通过 SetPermissionChecker() 替换全局检查器
//   - 后续可添加用户确认回调函数
//   - 后续可添加权限缓存机制
func checkToolPermission(toolName string, args string) PermissionResult {
	return globalPermissionChecker.CheckPermission(toolName, args)
}

// SetPermissionChecker 设置全局权限检查器。
// 允许上层注入自定义权限策略（如基于用户角色、资源路径的权限控制）。
func SetPermissionChecker(checker ToolPermissionChecker) {
	globalPermissionChecker = checker
}
