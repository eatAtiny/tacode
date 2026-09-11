package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileTool 提供文件读写能力。
type FileTool struct{}

// NewFileTool 创建文件工具。
func NewFileTool() *FileTool { return &FileTool{} }

// ── Tool 接口：基础方法 ──

func (t *FileTool) Name() string      { return "file" }
func (t *FileTool) Aliases() []string { return nil }

func (t *FileTool) Description() string {
	return "读取或写入文件。action=read 读取文件内容（带行号），action=write 写入文件（自动创建目录）。"
}

func (t *FileTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"description": "操作类型：read 或 write",
				"enum":        []string{"read", "write"},
			},
			"path": map[string]any{
				"type":        "string",
				"description": "文件路径",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "写入的内容（仅 write 操作需要）",
			},
		},
		"required": []string{"action", "path"},
	}
}

func (t *FileTool) Execute(args string) (string, error) {
	var params struct {
		Action  string `json:"action"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", err
	}

	params.Path = strings.TrimSpace(params.Path)
	if params.Path == "" {
		return "", fmt.Errorf("path is empty")
	}

	switch params.Action {
	case "read":
		return t.readFile(params.Path)
	case "write":
		return t.writeFile(params.Path, params.Content)
	default:
		return "", fmt.Errorf("unknown action: %s (use read or write)", params.Action)
	}
}

// readFile 读取文件内容，添加行号前缀。
// 框架层通过 ResultLimit 统一处理截断，此处不做硬截断。
func (t *FileTool) readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	lines := strings.Split(string(data), "\n")
	return formatWithLineNumbers(lines, 0), nil
}

// writeFile 写入文件内容，自动创建父目录。
// 写入成功后回显前 30 行带行号的预览，帮助 LLM 验证写入内容。
func (t *FileTool) writeFile(path, content string) (string, error) {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create dir: %w", err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	lines := strings.Split(content, "\n")
	lineCount := len(lines)
	preview := formatWithLineNumbers(lines, 30)

	truncNote := ""
	if lineCount > 30 {
		truncNote = fmt.Sprintf("\n  ... (%d lines total)", lineCount)
	}

	return fmt.Sprintf("✅ 文件已写入: %s (%d lines, %d bytes)\n\n%s%s",
		path, lineCount, len(content), preview, truncNote), nil
}

// ──────────────────────────────────────────────────────────
// 行号格式化辅助
// ──────────────────────────────────────────────────────────

// formatWithLineNumbers 为文本行添加行号前缀。
//
// 格式: "    1 | content"（4 位右对齐行号 + 竖线分隔符）。
// 与 readFile 和 writeFile 预览共用，保持格式一致。
//
// maxLines=0 表示不限制，maxLines>0 时只显示前 maxLines 行。
func formatWithLineNumbers(lines []string, maxLines int) string {
	n := len(lines)
	if maxLines > 0 && n > maxLines {
		n = maxLines
	}

	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "%4d | %s\n", i+1, lines[i])
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ── Tool 接口：权限内聚 ──

// CheckPermission 文件读操作直接允许，写操作需要确认。
func (t *FileTool) CheckPermission(args string) PermissionResult {
	var params struct {
		Action string `json:"action"`
	}
	if err := parseArgs(args, &params); err != nil {
		return PermissionResult{Allow: false, Reason: "无法解析参数"}
	}
	if params.Action == "read" {
		return PermissionResult{Allow: true}
	}
	return PermissionResult{Allow: false, Reason: "写入文件需要确认"}
}

// ── Tool 接口：Prompt 自引导 ──

// PromptGuide 返回 file 工具的使用引导。
func (t *FileTool) PromptGuide() string {
	return "read 操作返回带行号的文本，行号格式为 \"    1 | content\"。" +
		"write 操作会自动创建不存在的父目录。" +
		"修改已有文件时，优先使用 edit 工具（而非 write 全量覆盖）。"
}

// ── Tool 接口：并发安全 ──

// IsConcurrencySafe file read 可并发，file write 不可并发。
func (t *FileTool) IsConcurrencySafe(args string) bool {
	var params struct {
		Action string `json:"action"`
	}
	if err := parseArgs(args, &params); err != nil {
		return false
	}
	return params.Action == "read"
}

// IsReadOnly file read 是只读，file write 不是。
func (t *FileTool) IsReadOnly(args string) bool {
	var params struct {
		Action string `json:"action"`
	}
	if err := parseArgs(args, &params); err != nil {
		return false
	}
	return params.Action == "read"
}

// ── Tool 接口：结果上限 ──

// ResultLimit 文件读取上限 8192 字符。
func (t *FileTool) ResultLimit() int { return 8192 }
