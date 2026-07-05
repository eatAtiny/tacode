package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EditTool 提供 search-and-replace 文件编辑能力。
//
// 设计参考 Claude Code 的 FileEditTool：
//   - 唯一性校验：search 文本在文件中必须唯一匹配
//   - 引号容错：自动标准化弯引号为直引号
//   - Diff 输出：编辑后显示变更内容
//   - read-before-edit：编辑前验证文件已被读取且未被外部修改
//
// 这是对 file write（全量覆盖）的安全替代。
type EditTool struct {
	maxFileSize int64              // 最大文件大小（避免 LLM 尝试编辑大文件）
	readState   ReadState          // 已读文件状态（框架注入），nil 表示跳过检查
}

// NewEditTool 创建编辑工具。
func NewEditTool() *EditTool {
	return &EditTool{maxFileSize: 5 * 1024 * 1024} // 5MB
}

// SetReadState 实现 ReadStateAware 接口。
// 框架在 Execute 前调用，注入本轮已读取的文件 mtime。
func (t *EditTool) SetReadState(state ReadState) {
	t.readState = state
}

// ── Tool 接口：基础方法 ──

func (t *EditTool) Name() string    { return "edit" }
func (t *EditTool) Aliases() []string { return nil }

func (t *EditTool) Description() string {
	return "在文件中搜索并替换文本。默认 search 必须唯一匹配一次，设置 replace_all=true 可替换所有匹配项。"
}

func (t *EditTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "要编辑的文件路径",
			},
			"search": map[string]any{
				"type":        "string",
				"description": "要搜索并替换的文本（必须在文件中唯一匹配一次，除非设置 replace_all=true）",
			},
			"replace": map[string]any{
				"type":        "string",
				"description": "替换后的新文本（可以为空字符串，表示删除）",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "是否替换所有匹配项，默认 false（要求唯一匹配）。设为 true 时替换所有出现。",
			},
		},
		"required": []string{"path", "search", "replace"},
	}
}

// Execute 执行搜索替换操作。
//
// 流程：
//  1. 引号标准化（弯引号→直引号）
//  2. 唯一性校验（replace_all=false 时：0次→报错, >1次→报错, 1次→执行）
//  3. 执行替换 + 回写文件
//  4. 生成 diff 输出
func (t *EditTool) Execute(args string) (string, error) {
	var params struct {
		Path       string `json:"path"`
		Search     string `json:"search"`
		Replace    string `json:"replace"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if strings.TrimSpace(params.Path) == "" {
		return "", fmt.Errorf("path is empty")
	}

	// ── 步骤 1: 引号标准化 ──
	search := stripQuotes(params.Search)
	replace := stripQuotes(params.Replace)

	// ── 步骤 2: 读取文件 ──
	info, err := os.Stat(params.Path)
	if err != nil {
		return "", fmt.Errorf("cannot access file: %w", err)
	}
	if info.Size() > t.maxFileSize {
		return "", fmt.Errorf("file too large (%d bytes, max %d)", info.Size(), t.maxFileSize)
	}

	// ── 步骤 2a: read-before-edit 检查 ──
	// 验证文件在本轮已被读取，且读取后未被外部修改。
	// readState 为 nil 时跳过检查（测试或独立使用场景）。
	if t.readState != nil {
		if err := t.checkReadBeforeEdit(params.Path, info.ModTime()); err != nil {
			return "", err
		}
	}

	data, err := os.ReadFile(params.Path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	content := string(data)

	// ── 步骤 3: 唯一性校验 ──
	count := strings.Count(content, search)
	if count == 0 {
		return "", fmt.Errorf("search 文本在文件中未找到:\n%s", truncateForDisplay(search, 200))
	}
	if !params.ReplaceAll && count > 1 {
		locations := findOccurrences(content, search, 10)
		return "", fmt.Errorf(
			"search 文本在文件中出现了 %d 次（非唯一匹配）。请提供更多上下文使匹配唯一，或设置 replace_all=true 替换全部。\n前 %d 处位置: %s",
			count, len(locations), strings.Join(locations, ", "),
		)
	}

	// ── 步骤 4: 执行替换 ──
	var newContent string
	if params.ReplaceAll {
		newContent = strings.ReplaceAll(content, search, replace)
	} else {
		newContent = strings.Replace(content, search, replace, 1)
	}

	if err := os.WriteFile(params.Path, []byte(newContent), info.Mode()); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	// ── 步骤 5: 生成 diff ──
	diff := generateDiff(content, search, replace)

	note := ""
	if search != params.Search {
		note = " (引号已自动标准化)"
	}

	replaceCount := count
	if !params.ReplaceAll {
		replaceCount = 1
	}

	if params.ReplaceAll {
		return fmt.Sprintf("✅ 文件已编辑: %s%s（替换了 %d 处）\n\n%s", params.Path, note, replaceCount, diff), nil
	}
	return fmt.Sprintf("✅ 文件已编辑: %s%s\n\n%s", params.Path, note, diff), nil
}

// ── Tool 接口：权限内聚 ──

// CheckPermission 编辑文件始终需要用户确认。
func (t *EditTool) CheckPermission(args string) PermissionResult {
	return PermissionResult{
		Allow:  false,
		Reason: "编辑文件需要确认",
	}
}

// ── Tool 接口：Prompt 自引导 ──

// PromptGuide 返回 edit 工具的使用引导。
func (t *EditTool) PromptGuide() string {
	return "search 文本默认必须在文件中唯一匹配一次。" +
		"如需替换所有匹配项（如批量重命名），设置 replace_all=true。" +
		"替换后会显示 diff，请仔细检查变更是否正确。" +
		"修改已有文件时优先使用 edit，而非 file write 全量覆盖。"
}

// ── Tool 接口：并发安全 ──

// IsConcurrencySafe 编辑文件有写入副作用，不可并发执行。
func (t *EditTool) IsConcurrencySafe(args string) bool { return false }

// IsReadOnly 编辑文件是写入操作。
func (t *EditTool) IsReadOnly(args string) bool { return false }

// ── Tool 接口：结果上限 ──

// ResultLimit edit 的结果（diff）通常较短。
func (t *EditTool) ResultLimit() int { return 4000 }

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// stripQuotes 去除包裹字符串的引号（直引号和弯引号）。
//
// LLM 的 tokenization 可能将直引号映射为弯引号，
// 容错处理确保编辑操作不会因为引号差异而失败。
func stripQuotes(s string) string {
	if len(s) < 2 {
		return s
	}

	// 直引号
	if (s[0] == '"' && s[len(s)-1] == '"') ||
		(s[0] == '\'' && s[len(s)-1] == '\'') ||
		(s[0] == '`' && s[len(s)-1] == '`') {
		return s[1 : len(s)-1]
	}

	return s
}

// findOccurrences 查找 search 在 content 中的前 maxN 处行号。
func findOccurrences(content, search string, maxN int) []string {
	var locations []string
	lines := strings.Split(content, "\n")
	offset := 0
	for i, line := range lines {
		lineEnd := offset + len(line)
		if idx := strings.Index(content[offset:lineEnd], search); idx != -1 {
			locations = append(locations, fmt.Sprintf("L%d", i+1))
			if len(locations) >= maxN {
				break
			}
		}
		offset += len(line) + 1 // +1 for newline
	}
	return locations
}

// generateDiff 生成简易 unified-diff 风格的变更展示。
//
// 输出格式：
//
//	@@ -5,1 +5,1 @@
//	-old line content
//	+new line content
func generateDiff(original, search, replace string) string {
	origIdx := strings.Index(original, search)
	if origIdx < 0 {
		return ""
	}

	// 计算行号：统计 search 之前有多少个换行符。
	prefix := original[:origIdx]
	lineNum := strings.Count(prefix, "\n") + 1

	// 提取上下文行（search 所在的行）。
	lineStart := strings.LastIndex(prefix, "\n")
	if lineStart < 0 {
		lineStart = 0
	} else {
		lineStart++ // 跳过换行符
	}
	lineEnd := strings.Index(original[origIdx:], "\n")
	var oldLine string
	if lineEnd < 0 {
		oldLine = original[lineStart:]
	} else {
		oldLine = original[lineStart : origIdx+lineEnd]
	}

	// 构造新行。
	newLine := strings.Replace(oldLine, search, replace, 1)

	return fmt.Sprintf("@@ -%d +%d @@\n-%s\n+%s", lineNum, lineNum, oldLine, newLine)
}

// truncateForDisplay 截断用于错误显示的文本。
func truncateForDisplay(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ──────────────────────────────────────────────────────────
// Read-Before-Edit 检查
// ──────────────────────────────────────────────────────────

// checkReadBeforeEdit 验证目标文件已被读取且未被外部修改。
//
// 从 queryLoop.checkReadBeforeEdit 迁移至此，让工具自包含安全逻辑。
//
// 规则：
//  1. 文件未在 readState 中 → 错误："请先读取文件"
//  2. 文件 mtime 与记录不一致 → 错误："文件已被外部修改，请重新读取"
//
// 设计原则：这是硬约束，不是软警告。未满足条件时拒绝执行。
func (t *EditTool) checkReadBeforeEdit(path string, currentMtime time.Time) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil // 无法解析路径时跳过检查
	}

	recordedMtime, wasRead := t.readState[absPath]
	if !wasRead {
		return fmt.Errorf(
			"read-before-edit 检查失败：你尚未读取文件 %s。请先使用 file read 或 grep 读取文件内容后再编辑。",
			absPath,
		)
	}

	if !currentMtime.Equal(recordedMtime) {
		return fmt.Errorf(
			"read-before-edit 检查失败：文件 %s 的修改时间已变化（上次读取后可能被外部修改）。请重新读取文件确认内容后再编辑。",
			absPath,
		)
	}

	return nil
}
