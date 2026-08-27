package agent

// ──────────────────────────────────────────────────────────
// 权限检测系统（简化版）
//
// 调用链（重构后）：
//   queryLoop.executeSingleTool()
//     → t.CheckPermission(args)                       ← 工具自检权限
//     → [Allow=true]  继续执行工具
//     → [Allow=false] yield Permission 事件 → 阻塞等待用户决策 → 继续或拒绝
//     → 额外: 全局 ForbiddenTools 列表（管理级别禁止）
//
// 重构变化：
//   - 删除了 DefaultPermissionChecker，权限现在内聚在工具自身（Tool.CheckPermission）
//   - isDangerousShellCommand 移入 shell.go；isDangerousFileOperation 已随重构
//     删除（文件写危险检测内聚在 file/edit 工具的 CheckPermission）
//   - 保留 ToolPermissionChecker 接口和全局注入点（极端定制场景）
//   - 保留全局 ForbiddenTools 列表（管理策略：完全禁用某工具）
// ──────────────────────────────────────────────────────────

// ToolPermissionChecker 工具权限检查器接口（保留用于极端定制场景）。
//
// 正常情况下，权限由工具自身通过 Tool.CheckPermission(args) 声明。
// 此接口仅在需要覆盖工具默认权限策略时使用（如"所有 shell 命令都必须确认"）。
//
// 通过 SetPermissionChecker() 注入自定义实现。
type ToolPermissionChecker interface {
	// CheckPermission 检查工具调用权限。
	// 返回 true 表示允许，false 表示需要确认。
	CheckPermission(toolName string, args string) bool
}

// ──────────────────────────────────────────────────────────
// 全局禁止列表
// ──────────────────────────────────────────────────────────

// ForbiddenTools 全局禁止的工具名称列表（预留扩展点）。
// 被列入此列表的工具完全不可执行（管理级别的安全策略）。
// 当前无任何代码向此切片追加，isToolForbidden 的禁用检查恒为 false。
var ForbiddenTools []string

// isToolForbidden 检查工具是否在全局禁止列表中。
func isToolForbidden(toolName string) bool {
	for _, forbidden := range ForbiddenTools {
		if toolName == forbidden {
			return true
		}
	}
	return false
}

// ──────────────────────────────────────────────────────────
// 全局权限检查器（注入点）
// ──────────────────────────────────────────────────────────

// globalPermissionChecker 全局权限检查器实例。
// 默认为 nil（使用工具自身的 CheckPermission）。
// 设置为非 nil 时将覆盖所有工具的权限判定。
var globalPermissionChecker ToolPermissionChecker

// SetPermissionChecker 设置全局权限检查器。
// 允许上层注入自定义权限策略，覆盖所有工具的默认权限判定。
func SetPermissionChecker(checker ToolPermissionChecker) {
	globalPermissionChecker = checker
}
