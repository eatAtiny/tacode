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

// L1 层最多保留的原始对话记录数。
const maxHistoryRecords = 50

// HistoryStore 负责 L1 原始对话日志的持久化。
type HistoryStore struct {
	path string // history.jsonl 的完整路径
}

// NewHistoryStore 构造 HistoryStore，不立即创建目录（惰性创建）。
func NewHistoryStore(sessionDir string) *HistoryStore {
	return &HistoryStore{path: filepath.Join(sessionDir, "history.jsonl")}
}

// SetPath 切换底层文件路径（用于会话切换）。
func (s *HistoryStore) SetPath(sessionDir string) {
	s.path = filepath.Join(sessionDir, "history.jsonl")
}

// Path 返回当前 history.jsonl 路径。
func (s *HistoryStore) Path() string {
	return s.path
}

// Append 追加一条原始对话记录，超出上限时裁剪最旧的。
func (s *HistoryStore) Append(round int, userInput, assistantOutput string) error {
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
	if len(all) > maxHistoryRecords {
		all = all[len(all)-maxHistoryRecords:]
	}
	return s.writeAll(all)
}

// Digest 从原始日志生成简易摘要（降级方案：当 L2 为空时使用）。
func (s *HistoryStore) Digest(lastN int) string {
	if lastN <= 0 {
		return "(无历史记录)"
	}
	all, err := s.readAll()
	if err != nil {
		return "(读取历史失败)"
	}
	if len(all) == 0 {
		return "(无历史记录)"
	}

	start := len(all) - lastN
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	for i := start; i < len(all); i++ {
		r := all[i]
		fmt.Fprintf(&b, "- 轮次 %d 用户=%q 回复=%q\n", r.Round, trimText(r.UserInput, 80), trimText(r.AssistantOutput, 120))
	}
	return strings.TrimSpace(b.String())
}

// readAll 从 JSONL 读取全部记录。
func (s *HistoryStore) readAll() ([]Record, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open history file failed: %w", err)
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
		return nil, fmt.Errorf("scan history file failed: %w", err)
	}
	return all, nil
}

// writeAll 覆盖写回全部记录，写入前惰性创建目录。
func (s *HistoryStore) writeAll(records []Record) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session dir failed: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open history file failed: %w", err)
	}
	defer f.Close()

	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("marshal history record failed: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("write history record failed: %w", err)
		}
	}
	return nil
}

// trimText 压缩文本长度。
func trimText(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
