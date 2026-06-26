package agent

import (
	"encoding/json"
	"strings"
)

// ──────────────────────────────────────────────────────────
// 权限检测系统
// ──────────────────────────────────────────────────────────

// PermissionAction 权限动作类型。
type PermissionAction string

const (
	PermissionAllow  PermissionAction = "allow"  // 允许执行
	PermissionDeny   PermissionAction = "deny"   // 拒绝执行
	PermissionConfirm PermissionAction = "confirm" // 需要用户确认
)

// PermissionResult 权限检测结果。
type PermissionResult struct {
	Action  PermissionAction // 权限动作
	Message string           // 附加消息（deny 或 confirm 时）
	Reason  string           // 原因说明
}

// ToolPermissionChecker 工具权限检查器接口。
// 留好扩展接口，后续可以实现更复杂的权限策略。
type ToolPermissionChecker interface {
	// CheckPermission 检查工具调用权限。
	// 参数：
	//   - toolName: 工具名称
	//   - args: 工具参数（JSON 字符串）
	// 返回：
	//   - PermissionResult: 权限检测结果
	CheckPermission(toolName string, args string) PermissionResult
}

// DefaultPermissionChecker 默认权限检查器。
// 实现简单的权限策略：
//   - 危险工具（shell、file）需要确认
//   - 其他工具直接允许
//
// TODO: 扩展权限检查器功能
//   - 支持基于用户角色的权限控制
//   - 支持基于资源路径的权限控制
//   - 支持权限缓存机制
//   - 支持权限审计日志
type DefaultPermissionChecker struct {
	// 危险工具列表，需要确认
	DangerousTools []string
	// 禁止的工具列表
	ForbiddenTools []string
	// TODO: 添加用户角色字段
	// UserRole string
	// TODO: 添加权限缓存
	// PermissionCache map[string]PermissionResult
}

// NewDefaultPermissionChecker 创建默认权限检查器。
func NewDefaultPermissionChecker() *DefaultPermissionChecker {
	return &DefaultPermissionChecker{
		DangerousTools: []string{"shell", "file"},
		ForbiddenTools: []string{}, // 默认没有禁止的工具
	}
}

// CheckPermission 检查工具调用权限。
func (c *DefaultPermissionChecker) CheckPermission(toolName string, args string) PermissionResult {
	// 检查是否是禁止的工具。
	for _, forbidden := range c.ForbiddenTools {
		if toolName == forbidden {
			return PermissionResult{
				Action:  PermissionDeny,
				Message: "该工具已被禁止使用",
				Reason:  "forbidden_tool",
			}
		}
	}

	// 检查是否是危险的工具。
	for _, dangerous := range c.DangerousTools {
		if toolName == dangerous {
			// 解析参数，检查是否是高风险操作。
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

	// 其他工具直接允许。
	return PermissionResult{
		Action: PermissionAllow,
		Reason: "safe_tool",
	}
}

// isHighRiskOperation 检查是否是高风险操作。
// 留好扩展接口，后续可以实现更复杂的风险评估。
//
// TODO: 实现更复杂的风险评估
//   - 基于机器学习的风险评估
//   - 基于历史行为的风险评估
//   - 基于上下文的风险评估
//   - 支持自定义风险规则
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

	// 危险命令列表。
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

	// 写操作需要确认。
	if action == "write" || action == "delete" {
		return true
	}

	return false
}

// 全局权限检查器实例。
var globalPermissionChecker ToolPermissionChecker

// init 初始化全局权限检查器。
func init() {
	globalPermissionChecker = NewDefaultPermissionChecker()
}

// checkToolPermission 检查工具调用权限。
// 这是 queryLoop 调用的入口函数。
//
// 参数：
//   - toolName: 工具名称
//   - args: 工具参数（JSON 字符串）
//
// 返回：
//   - PermissionResult: 权限检测结果
//
// 扩展接口：
//   - 可以通过替换 globalPermissionChecker 实现自定义权限策略
//   - 后续可以添加用户确认回调函数
//   - 后续可以添加权限缓存机制
//
// TODO: 实现用户确认回调机制
//   - 添加 ConfirmCallback 类型
//   - 在 confirm 动作时调用回调函数
//   - 支持异步确认（非阻塞）
//
// TODO: 实现权限缓存机制
//   - 添加缓存过期时间
//   - 支持缓存清理
//   - 避免重复检查相同权限
func checkToolPermission(toolName string, args string) PermissionResult {
	return globalPermissionChecker.CheckPermission(toolName, args)
}

// SetPermissionChecker 设置全局权限检查器。
// 留好扩展接口，允许上层注入自定义权限策略。
func SetPermissionChecker(checker ToolPermissionChecker) {
	globalPermissionChecker = checker
}
