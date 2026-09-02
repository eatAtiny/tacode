package agent

import (
	"tacode/internal/tool"
)

// ──────────────────────────────────────────────────────────
// CompactTool — 模型主动请求压缩的工具
//
// 镜像 s08 的 compact 工具：模型在阶段结束后可主动调用，
// 表示后续工作只需要保留当前阶段的摘要。
//
// 执行语义（在 queryLoop 中处理）：
//   - 与其他工具一起整批执行（Execute 返回固定提示）
//   - 整批 tool 结果追加完毕（回合闭合）后，若批次含 compact，
//     对已闭合的回合运行 compactHistory（镜像 s08）
//
// 这样既不会留下孤立的工具结果，也不会在发生文件写入后丢失执行记录，
// 导致模型重复同一个副作用。
// ──────────────────────────────────────────────────────────

// CompactTool 实现 tool.Tool 接口。
type CompactTool struct{}

// Name 返回工具名。
func (t *CompactTool) Name() string { return "compact" }

// Aliases 返回工具别名（无）。
func (t *CompactTool) Aliases() []string { return nil }

// Description 返回工具描述。
func (t *CompactTool) Description() string {
	return "Summarize earlier conversation to free context space."
}

// Parameters 返回参数定义（JSON schema 格式，无参数）。
func (t *CompactTool) Parameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

// PromptGuide 返回使用引导（无）。
func (t *CompactTool) PromptGuide() string { return "" }

// ResultLimit 返回结果截断上限（0 = 使用默认）。
func (t *CompactTool) ResultLimit() int { return 0 }

// IsReadOnly 返回是否只读（false：compact 会修改上下文）。
func (t *CompactTool) IsReadOnly(string) bool { return false }

// IsConcurrencySafe 返回是否并发安全（false：串行执行）。
func (t *CompactTool) IsConcurrencySafe(string) bool { return false }

// CheckPermission 返回权限检查结果（始终允许，不弹确认）。
func (t *CompactTool) CheckPermission(string) tool.PermissionResult {
	return tool.PermissionResult{Allow: true}
}

// Execute 执行工具（固定返回提示，实际压缩由 queryLoop 处理）。
func (t *CompactTool) Execute(string) (string, error) {
	return "Compaction requested after this tool batch.", nil
}
