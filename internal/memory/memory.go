package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 最多保留最近 20 条记忆（即最近 20 轮）。
const maxRecords = 20

// Record 表示单轮对话记忆。
type Record struct {
	Round           int    `json:"round"`
	Timestamp       string `json:"timestamp"`
	UserInput       string `json:"user_input"`
	AssistantOutput string `json:"assistant_output"`
}

// Store 负责把记忆持久化到本地 JSONL 文件。
type Store struct {
	path string
}

// NewStore 初始化存储目录。
func NewStore(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create memory dir failed: %w", err)
	}
	return &Store{path: path}, nil
}

// SetPath 切换底层 JSONL 文件路径（用于会话切换）。
func (s *Store) SetPath(path string) {
	s.path = path
}

// Append 追加一条新记忆，并裁剪为最近 20 条。
func (s *Store) Append(round int, userInput, assistantOutput string) error {
	record := Record{
		Round:           round,
		Timestamp:       time.Now().Format(time.RFC3339),
		UserInput:       userInput,
		AssistantOutput: assistantOutput,
	}

	all, err := s.readAll()
	if err != nil {
		return err
	}
	all = append(all, record)
	if len(all) > maxRecords {
		all = all[len(all)-maxRecords:]
	}

	return s.writeAll(all)
}

// readAll 从 JSONL 读取全部记忆。
func (s *Store) readAll() ([]Record, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open memory file failed: %w", err)
	}
	defer f.Close()

	var all []Record
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			continue
		}
		all = append(all, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan memory file failed: %w", err)
	}
	return all, nil
}

// writeAll 覆盖写回全部记忆。
func (s *Store) writeAll(records []Record) error {
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open memory file failed: %w", err)
	}
	defer f.Close()

	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshal memory record failed: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("write memory record failed: %w", err)
		}
	}
	return nil
}

// Digest 返回最近 N 条记忆的摘要文本，用于拼接到 prompt 中。
func (s *Store) Digest(lastN int) string {
	if lastN <= 0 {
		return "(no memory)"
	}

	all, err := s.readAll()
	if err != nil {
		return "(memory read error)"
	}
	if len(all) == 0 {
		return "(no memory)"
	}

	start := len(all) - lastN
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	for i := start; i < len(all); i++ {
		r := all[i]
		fmt.Fprintf(&b, "- round %d user=%q assistant=%q\n", r.Round, trim(r.UserInput, 80), trim(r.AssistantOutput, 120))
	}
	return strings.TrimSpace(b.String())
}

// trim 压缩文本长度，避免摘要过长。
func trim(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
