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

func (t *ListTool) Name() string      { return "list" }
func (t *ListTool) Aliases() []string { return nil }

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
}

// Execute 列出目录内容。
func (t *ListTool) Execute(args string) (string, error) {
	var params struct {
		Path       string `json:"path"`
		Depth      int    `json:"depth"`
		MaxEntries int    `json:"max_entries"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", err
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
	// prefix 设为用户指定的 path 参数，确保输出的相对路径是从 CWD 出发的，
	// 而非从 path 参数出发。LLM 可以直接复用输出中的路径。
	// 例外：path 为 "." 时不加前缀（避免出现 "./..." 的冗余写法）。
	basePrefix := params.Path
	if basePrefix == "." {
		basePrefix = ""
	}
	// scanTruncated 表示双倍采集触顶提前终止，收集到的条目可能不完整。
	entries, scanTruncated := collectEntries(params.Path, basePrefix, 1, params.Depth, params.MaxEntries)

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
		// scanTruncated 时 len(entries) 是采集上限（maxEntries*2）而非真实总数，
		// 必须明示扫描可能不完整，避免 LLM/用户把该数字误读为目录真实条目数。
		if scanTruncated {
			sb.WriteString(fmt.Sprintf("…(已截断, 共 %d 个条目（已达扫描上限，可能不完整）)\n", len(entries)))
		} else {
			sb.WriteString(fmt.Sprintf("…(已截断, 共 %d 个条目)\n", len(entries)))
		}
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

// ── Tool 接口：并发安全 ──

// IsConcurrencySafe list 是纯只读操作，可以并发执行。
func (t *ListTool) IsConcurrencySafe(args string) bool { return true }

// IsReadOnly list 不修改任何文件。
func (t *ListTool) IsReadOnly(args string) bool { return true }

// ── Tool 接口：结果上限 ──

// ResultLimit list 结果上限 3000 字符。
func (t *ListTool) ResultLimit() int { return 3000 }

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// collectEntries 递归收集目录条目。
//
// displayPath 是从最初 base 出发的相对路径（用 "/" 连接），
// 而非之前的空格缩进。LLM 可以直接用这个路径作为后续 list/grep/file 的参数。
//
// 返回 entries 与 truncated：truncated 表示扫描因 maxEntries*2 双倍采集上限
// 提前终止，收集结果可能不完整。双倍采集而非恰取 maxEntries，是为排序前留
// 余量：目录条目按「目录优先 + 字母序」排序后取前 maxEntries 条，若只采集
// maxEntries 条就排序截断，可能因采集顺序丢排头本该出现的条目。
func collectEntries(basePath, prefix string, currentDepth, maxDepth, maxEntries int) ([]dirEntry, bool) {
	var entries []dirEntry
	truncated := collectEntriesRec(basePath, prefix, currentDepth, maxDepth, maxEntries, &entries)
	return entries, truncated
}

// collectEntriesRec 是 collectEntries 的递归实现。
//
// 通过共享切片指针，整个遍历共用同一个 maxEntries*2 采集上限：任意递归层
// 发现已采集数达到上限即提前终止（返回 true 表示截断），而非各层独立计数。
func collectEntriesRec(basePath, prefix string, currentDepth, maxDepth, maxEntries int, entries *[]dirEntry) bool {
	if currentDepth > maxDepth || len(*entries) >= maxEntries*2 {
		return len(*entries) >= maxEntries*2
	}

	dirEntries, err := os.ReadDir(basePath)
	if err != nil {
		return false
	}

	truncated := false
	for _, de := range dirEntries {
		if len(*entries) >= maxEntries*2 {
			return true
		}

		info, err := de.Info()
		if err != nil {
			continue
		}

		name := de.Name()
		// 相对路径：父路径 + "/" + 当前名（第一层直接用 name，无前缀）。
		var displayPath string
		if prefix == "" {
			displayPath = name
		} else {
			displayPath = prefix + "/" + name
		}

		if de.IsDir() {
			subPath := filepath.Join(basePath, name)
			*entries = append(*entries, dirEntry{
				isDir: true,
				name:  displayPath + "/",
			})
			// 递归进入子目录，prefix 传递相对路径而非空格缩进。
			if collectEntriesRec(subPath, displayPath, currentDepth+1, maxDepth, maxEntries, entries) {
				truncated = true
			}
		} else {
			*entries = append(*entries, dirEntry{
				isDir: false,
				name:  displayPath,
				size:  info.Size(),
			})
		}
	}
	return truncated
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
