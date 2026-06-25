package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MemoryStore 负责 L3 结构化记忆文件的管理。
// 每条记忆对应一个 .md 文件，目录下有 MEMORY.md 索引。
type MemoryStore struct {
	dir string // memory/ 目录的完整路径
}

// NewMemoryStore 构造 MemoryStore，不立即创建目录（惰性创建）。
func NewMemoryStore(sessionDir string) (*MemoryStore, error) {
	return &MemoryStore{dir: filepath.Join(sessionDir, "memory")}, nil
}

// SetPath 切换 memory/ 目录路径（用于会话切换），不立即创建目录。
func (s *MemoryStore) SetPath(sessionDir string) {
	s.dir = filepath.Join(sessionDir, "memory")
}

// Dir 返回 memory/ 目录路径。
func (s *MemoryStore) Dir() string {
	return s.dir
}

// LoadIndex 读取 MEMORY.md 索引内容。
func (s *MemoryStore) LoadIndex() string {
	data, err := os.ReadFile(filepath.Join(s.dir, "MEMORY.md"))
	if err != nil {
		return ""
	}
	return string(data)
}

// ListEntries 列出所有记忆条目（从 .md 文件解析）。
func (s *MemoryStore) ListEntries() ([]MemoryEntry, error) {
	files, err := filepath.Glob(filepath.Join(s.dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob memory files failed: %w", err)
	}

	var entries []MemoryEntry
	for _, f := range files {
		name := filepath.Base(f)
		if name == "MEMORY.md" {
			continue
		}
		entry, err := s.loadEntry(f)
		if err != nil {
			continue
		}
		entries = append(entries, entry)
	}

	// 按重要性降序排列。
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Importance > entries[j].Importance
	})
	return entries, nil
}

// GetEntry 按 name 读取单条记忆。
func (s *MemoryStore) GetEntry(name string) *MemoryEntry {
	path := filepath.Join(s.dir, name+".md")
	entry, err := s.loadEntry(path)
	if err != nil {
		return nil
	}
	return &entry
}

// SaveEntry 写入/更新一条记忆，同时更新索引。写入前惰性创建目录。
func (s *MemoryStore) SaveEntry(entry MemoryEntry) error {
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return fmt.Errorf("create memory dir failed: %w", err)
	}
	if entry.Created == "" {
		entry.Created = time.Now().Format(time.RFC3339)
	}
	entry.Updated = time.Now().Format(time.RFC3339)

	content := s.formatEntry(entry)
	path := filepath.Join(s.dir, entry.Name+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write memory file failed: %w", err)
	}
	return s.rebuildIndex()
}

// DeleteEntry 按 name 删除一条记忆，同时更新索引。
func (s *MemoryStore) DeleteEntry(name string) error {
	path := filepath.Join(s.dir, name+".md")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("delete memory file failed: %w", err)
	}
	return s.rebuildIndex()
}

// FormatForPrompt 按重要性加载记忆，返回适合注入 prompt 的文本。
// maxEntries 限制最多加载多少条，minImportance 过滤最低重要性。
func (s *MemoryStore) FormatForPrompt(maxEntries, minImportance int) (string, error) {
	entries, err := s.ListEntries()
	if err != nil {
		return "", err
	}

	var selected []MemoryEntry
	for _, e := range entries {
		if e.Importance < minImportance {
			continue
		}
		selected = append(selected, e)
		if len(selected) >= maxEntries {
			break
		}
	}

	if len(selected) == 0 {
		return "", nil
	}

	var b strings.Builder
	for _, e := range selected {
		fmt.Fprintf(&b, "### %s\n%s\n\n", e.Description, e.Content)
	}
	return strings.TrimSpace(b.String()), nil
}

// rebuildIndex 遍历 memory/ 目录重建 MEMORY.md。
func (s *MemoryStore) rebuildIndex() error {
	entries, err := s.ListEntries()
	if err != nil {
		return err
	}

	var b strings.Builder
	if len(entries) == 0 {
		b.WriteString("# Memory Index\n\n(暂无记忆)\n")
	} else {
		b.WriteString("# Memory Index\n\n")
		for _, e := range entries {
			typeTag := e.Type
			if typeTag == "" {
				typeTag = "general"
			}
			fmt.Fprintf(&b, "- [%s](%s.md) — %s\n", e.Description, e.Name, typeTag)
		}
	}

	path := filepath.Join(s.dir, "MEMORY.md")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// loadEntry 从 .md 文件解析 MemoryEntry（frontmatter + content）。
func (s *MemoryStore) loadEntry(path string) (MemoryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return MemoryEntry{}, err
	}
	return s.parseEntry(string(data)), nil
}

// parseEntry 解析 frontmatter + content 格式的记忆文件。
// 格式：
//
//	---
//	name: xxx
//	description: xxx
//	type: user
//	importance: 3
//	---
//	正文内容
func (s *MemoryStore) parseEntry(content string) MemoryEntry {
	entry := MemoryEntry{}

	// 查找 frontmatter 边界。
	if !strings.HasPrefix(content, "---") {
		// 没有 frontatter，整段作为 content。
		entry.Content = content
		// 从文件名推断 name（调用方应设置）。
		return entry
	}

	endIdx := strings.Index(content[3:], "---")
	if endIdx < 0 {
		entry.Content = content
		return entry
	}

	frontmatter := content[3 : endIdx+3]
	entry.Content = strings.TrimSpace(content[endIdx+6:])

	// 简单解析 YAML frontmatter（不引入外部依赖）。
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		switch key {
		case "name":
			entry.Name = val
		case "description":
			entry.Description = val
		case "type":
			entry.Type = val
		case "importance":
			fmt.Sscanf(val, "%d", &entry.Importance)
		case "tags":
			// 解析 [tag1, tag2] 格式。
			val = strings.Trim(val, "[]")
			for _, tag := range strings.Split(val, ",") {
				tag = strings.TrimSpace(tag)
				if tag != "" {
					entry.Tags = append(entry.Tags, tag)
				}
			}
		case "created":
			entry.Created = val
		case "updated":
			entry.Updated = val
		}
	}

	return entry
}

// formatEntry 将 MemoryEntry 序列化为 frontmatter + content 格式。
func (s *MemoryStore) formatEntry(entry MemoryEntry) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", entry.Name)
	fmt.Fprintf(&b, "description: %s\n", entry.Description)
	fmt.Fprintf(&b, "type: %s\n", entry.Type)
	fmt.Fprintf(&b, "importance: %d\n", entry.Importance)
	if len(entry.Tags) > 0 {
		fmt.Fprintf(&b, "tags: [%s]\n", strings.Join(entry.Tags, ", "))
	}
	if entry.Created != "" {
		fmt.Fprintf(&b, "created: %s\n", entry.Created)
	}
	if entry.Updated != "" {
		fmt.Fprintf(&b, "updated: %s\n", entry.Updated)
	}
	b.WriteString("---\n\n")
	b.WriteString(entry.Content)
	b.WriteString("\n")
	return b.String()
}
