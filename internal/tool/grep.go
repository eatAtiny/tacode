package tool

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GrepTool 提供结构化文本搜索能力。
//
// 与 shell grep 的区别：
//   - 自动跳过 .git/ 目录
//   - 自动跳过二进制文件
//   - 结果数量上限，防止上下文爆炸
//   - 统一的输出格式
type GrepTool struct{}

// NewGrepTool 创建搜索工具。
func NewGrepTool() *GrepTool { return &GrepTool{} }

// ── Tool 接口：基础方法 ──

func (t *GrepTool) Name() string { return "grep" }

func (t *GrepTool) Description() string {
	return "在文件中搜索文本模式。自动跳过 .git/ 和二进制文件，结果有数量上限。"
}

func (t *GrepTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "搜索模式（支持 Go 正则表达式语法）",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "搜索路径（文件或目录），默认当前目录",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "最大返回结果数，默认 30",
			},
			"case_sensitive": map[string]any{
				"type":        "boolean",
				"description": "是否区分大小写，默认 false（忽略大小写）",
			},
		},
		"required": []string{"pattern"},
	}
}

// Execute 执行文本搜索。
func (t *GrepTool) Execute(args string) (string, error) {
	var params struct {
		Pattern       string `json:"pattern"`
		Path          string `json:"path"`
		MaxResults    int    `json:"max_results"`
		CaseSensitive bool   `json:"case_sensitive"`
	}
	if err := parseArgs(args, &params); err != nil {
		return "", fmt.Errorf("parse args: %w", err)
	}

	if strings.TrimSpace(params.Pattern) == "" {
		return "", fmt.Errorf("pattern is empty")
	}
	if params.Path == "" {
		params.Path = "."
	}
	if params.MaxResults <= 0 {
		params.MaxResults = 30
	}

	// 构建正则表达式。
	pattern := params.Pattern
	if !params.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	// 搜索并收集结果。
	var results []string
	totalMatches := 0
	truncated := false

	err = filepath.Walk(params.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的文件
		}

		// 跳过 .git/ 目录。
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		// 跳过二进制文件。
		if isBinary(path) {
			return nil
		}

		// 搜索文件内容。
		fileResults := searchFile(path, re)
		totalMatches += len(fileResults)

		for _, r := range fileResults {
			if len(results) >= params.MaxResults {
				truncated = true
				return filepath.SkipAll
			}
			results = append(results, r)
		}
		return nil
	})

	if err != nil && !truncated {
		return "", fmt.Errorf("search error: %w", err)
	}

	if len(results) == 0 {
		return "未找到匹配结果。", nil
	}

	var sb strings.Builder
	sb.WriteString(strings.Join(results, "\n"))
	if truncated {
		remaining := totalMatches - len(results)
		sb.WriteString(fmt.Sprintf("\n…(结果已截断, 还有 %d 条匹配)", remaining))
	}
	return sb.String(), nil
}

// ── Tool 接口：权限内聚 ──

// CheckPermission grep 是纯只读操作，始终允许。
func (t *GrepTool) CheckPermission(args string) PermissionResult {
	return PermissionResult{Allow: true}
}

// ── Tool 接口：Prompt 自引导 ──

// PromptGuide 返回 grep 工具的使用引导。
func (t *GrepTool) PromptGuide() string {
	return "支持 Go 正则表达式语法，默认忽略大小写。" +
		"优先使用此工具而非 shell grep 命令。" +
		"结果默认限制 30 条，可以缩小搜索范围或增加 max_results 获取更多。"
}

// ── Tool 接口：并发安全 ──

// IsConcurrencySafe grep 是纯只读操作，可以并发执行。
func (t *GrepTool) IsConcurrencySafe(args string) bool { return true }

// IsReadOnly grep 不修改任何文件。
func (t *GrepTool) IsReadOnly(args string) bool { return true }

// ── Tool 接口：结果上限 ──

// ResultLimit grep 结果上限 3000 字符。
func (t *GrepTool) ResultLimit() int { return 3000 }

// ──────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────

// isBinary 检查文件是否为二进制文件（前 512 字节含 null byte）。
func isBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true // 无法打开的文件视为二进制，跳过
	}
	defer f.Close()

	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

// searchFile 在单个文件中搜索匹配行。
// 返回格式: path/to/file.go:42: matching line content
func searchFile(path string, re *regexp.Regexp) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var results []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if re.MatchString(line) {
			results = append(results, fmt.Sprintf("%s:%d: %s", path, lineNum, strings.TrimSpace(line)))
		}
	}
	return results
}
