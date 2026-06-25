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

// SummaryStore 负责 L2 对话摘要的持久化。
type SummaryStore struct {
	path string // summaries.jsonl 的完整路径
}

// NewSummaryStore 构造 SummaryStore，不立即创建目录（惰性创建）。
func NewSummaryStore(sessionDir string) (*SummaryStore, error) {
	return &SummaryStore{path: filepath.Join(sessionDir, "summaries.jsonl")}, nil
}

// SetPath 切换底层文件路径。
func (s *SummaryStore) SetPath(sessionDir string) {
	s.path = filepath.Join(sessionDir, "summaries.jsonl")
}

// Append 追加一条摘要。
func (s *SummaryStore) Append(round int, summaryText string) error {
	summary := Summary{
		Round:     round,
		Timestamp: time.Now().Format(time.RFC3339),
		Summary:   summaryText,
	}

	all, err := s.readAll()
	if err != nil {
		return err
	}
	all = append(all, summary)
	return s.writeAll(all)
}

// LoadRecent 加载最近 N 条摘要。
func (s *SummaryStore) LoadRecent(n int) ([]Summary, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// LoadAll 加载全部摘要。
func (s *SummaryStore) LoadAll() ([]Summary, error) {
	return s.readAll()
}

// ReplaceAll 整体替换全部摘要（压缩后使用）。
func (s *SummaryStore) ReplaceAll(summaries []Summary) error {
	return s.writeAll(summaries)
}

// Count 返回摘要总数。
func (s *SummaryStore) Count() (int, error) {
	all, err := s.readAll()
	if err != nil {
		return 0, err
	}
	return len(all), nil
}

// FormatRecent 格式化最近 N 条摘要为可读文本，用于注入 prompt。
func (s *SummaryStore) FormatRecent(n int) (string, error) {
	summaries, err := s.LoadRecent(n)
	if err != nil {
		return "", err
	}
	if len(summaries) == 0 {
		return "", nil
	}

	var b strings.Builder
	for _, s := range summaries {
		fmt.Fprintf(&b, "- [轮次 %d] %s\n", s.Round, s.Summary)
	}
	return strings.TrimSpace(b.String()), nil
}

// readAll 从 JSONL 读取全部摘要。
func (s *SummaryStore) readAll() ([]Summary, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open summary file failed: %w", err)
	}
	defer f.Close()

	var all []Summary
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec Summary
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		all = append(all, rec)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan summary file failed: %w", err)
	}
	return all, nil
}

// writeAll 覆盖写回全部摘要，写入前惰性创建目录。
func (s *SummaryStore) writeAll(summaries []Summary) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session dir failed: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open summary file failed: %w", err)
	}
	defer f.Close()

	for _, rec := range summaries {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal summary record failed: %w", err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("write summary record failed: %w", err)
		}
	}
	return nil
}
