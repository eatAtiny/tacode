package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ListTool 提供结构化目录列表能力。
//
// 输出格式：每行一个条目，包含类型标识、大小、名称。
// 目录在前、文件在后，各自按字母序排列。
type ListTool struct{}

// NewListTool 创建列表工具。
func NewListTool() *ListTool { return &ListTool{} }

// ── Tool 接口：基础方法 ──

func (t *ListTool) Name() string { return "list" }

func (t *ListTool) Description() string {
	return "列出目录内容。支持递归深度控制、条目数量上限。返回格式: [TYPE] SIZE NAME。"
}

func (t *ListTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "目录路径，默认当前目录",
			},
			"depth": map[string]any{
				"type":        "integer",
				"description": "递归深度，1=仅当前目录（默认），2=包含一级子目录，以此类推",
			},
			"max_entries": map[string]any{
				"type":        "integer",
				"description": "最大返回条目数，默认 50",
			},
		},
	}
}

// dirEntry 表示一个目录条目。
type dirEntry struct {
	isDir bool
	name  string
	size  int64
	path  string // 相对路径（含缩进，用于递归显示）
}

// Execute 列出目录内容。
func (t *ListTool) Execute(args string) (string, error) {
	var params struct {
		Path       string `json:"path"`
		Depth      int    `json:"depth"`
		MaxEntries int    `json:"max_entries"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if params.Path == "" {
		params.Path = "."
	}
	if params.Depth <= 0 {
		params.Depth = 1
	}
	if params.MaxEntries <= 0 {
		params.MaxEntries = 50
	}

	// 检查路径是否存在。
	info, err := os.Stat(params.Path)
	if err != nil {
		return "", fmt.Errorf("cannot access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("path is not a directory: %s", params.Path)
	}

	// 收集条目。
	var entries []dirEntry
	collectEntries(params.Path, "", 1, params.Depth, params.MaxEntries, &entries)

	// 排序：目录优先 → 字母序。
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir // 目录在前
		}
		return strings.ToLower(entries[i].name) < strings.ToLower(entries[j].name)
	})

	// 格式化输出。
	if len(entries) == 0 {
		return "(空目录)", nil
	}

	var sb strings.Builder
	truncated := false
	for i, e := range entries {
		if i >= params.MaxEntries {
			truncated = true
			break
		}
		sb.WriteString(formatEntry(e))
		sb.WriteByte('\n')
	}

	if truncated {
		sb.WriteString(fmt.Sprintf("…(已截断, 共 %d 个条目)\n", len(entries)))
	}

	return strings.TrimRight(sb.String(), "\n"), nil
}

// ── Tool 接口：权限内聚 ──

// CheckPermission list 是纯只读操作，始终允许。
func (t *ListTool) CheckPermission(args string) PermissionResult {
	return PermissionResult{Allow: true}
}

// ── Tool 接口：Prompt 自引导 ──

// PromptGuide list 行为直观，无需额外引导。
func (t *ListTool) PromptGuide() string {
	return ""
}

// ── Tool 接口：结果上限 ──

// ResultLimit list 结果上限 3000 字符。
func (t *ListTool) ResultLimit() int { return 3000 }

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// collectEntries 递归收集目录条目。
func collectEntries(basePath, prefix string, currentDepth, maxDepth, maxEntries int, entries *[]dirEntry) {
	if currentDepth > maxDepth || len(*entries) >= maxEntries*2 {
		return
	}

	dirEntries, err := os.ReadDir(basePath)
	if err != nil {
		return
	}

	for _, de := range dirEntries {
		if len(*entries) >= maxEntries*2 {
			return
		}

		info, err := de.Info()
		if err != nil {
			continue
		}

		name := de.Name()
		displayPath := prefix + name

		if de.IsDir() {
			*entries = append(*entries, dirEntry{
				isDir: true,
				name:  displayPath + "/",
				path:  filepath.Join(basePath, name),
			})
			// 递归进入子目录。
			subPrefix := prefix + "  "
			subPath := filepath.Join(basePath, name)
			collectEntries(subPath, subPrefix, currentDepth+1, maxDepth, maxEntries, entries)
		} else {
			*entries = append(*entries, dirEntry{
				isDir: false,
				name:  displayPath,
				size:  info.Size(),
			})
		}
	}
}

// formatEntry 格式化单个条目。
//
// 格式:
//
//	[DIR]  dirname/
//	[FILE] 4.2K  filename
//	[FILE] 120B  smallfile
func formatEntry(e dirEntry) string {
	if e.isDir {
		return fmt.Sprintf("[DIR]  %s", e.name)
	}
	return fmt.Sprintf("[FILE] %s  %s", formatSize(e.size), e.name)
}

// formatSize 格式化文件大小（人类可读）。
//
// < 1024 → 直接显示字节
// < 1024*1024 → 显示为 KB
// >= 1024*1024 → 显示为 MB
func formatSize(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%4dB", size)
	}
	if size < 1024*1024 {
		return fmt.Sprintf("%4.1fK", float64(size)/1024)
	}
	return fmt.Sprintf("%4.1fM", float64(size)/(1024*1024))
}
