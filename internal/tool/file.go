package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxReadSize = 8192 // 读取文件最大字节数

// FileTool 提供文件读写能力。
type FileTool struct{}

func NewFileTool() *FileTool { return &FileTool{} }

func (t *FileTool) Name() string { return "file" }

func (t *FileTool) Description() string {
	return "读取或写入文件。action=read 读取文件内容，action=write 写入文件。"
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
		return "", fmt.Errorf("parse args: %w", err)
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

func (t *FileTool) readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}
	content := string(data)
	if len(content) > maxReadSize {
		content = content[:maxReadSize] + "\n...(truncated)"
	}
	return content, nil
}

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
	return fmt.Sprintf("文件已写入: %s (%d bytes)", path, len(content)), nil
}
