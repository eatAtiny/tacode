package memory

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EventType 事件类型枚举。
type EventType string

const (
	EventUser         EventType = "user"          // 用户输入
	EventAssistant    EventType = "assistant"     // 模型最终回答
	EventToolUse      EventType = "tool_use"      // 工具调用请求
	EventToolResult   EventType = "tool_result"   // 工具执行结果
	EventSystem       EventType = "system"        // 系统命令（/new, /rename 等）
	EventSessionStart EventType = "session_start" // 会话启动/恢复
)

// Event 是事件日志中的一条记录。
type Event struct {
	UUID       string    `json:"uuid"`
	Type       EventType `json:"type"`
	Timestamp  string    `json:"timestamp"`
	Round      int       `json:"round,omitempty"`
	ParentUUID string    `json:"parentUuid,omitempty"`

	// user / assistant / system / session_start
	Content string `json:"content,omitempty"`

	// tool_use
	ToolCalls []ToolCallEvent `json:"toolCalls,omitempty"`

	// tool_result
	ToolResult  string `json:"toolResult,omitempty"`
	IsError     bool   `json:"isError,omitempty"`
	ToolCallID  string `json:"toolCallId,omitempty"`
	ToolName    string `json:"toolName,omitempty"`

	// system
	Command string `json:"command,omitempty"`

	// assistant
	Model string `json:"model,omitempty"`
}

// ToolCallEvent 记录一次工具调用。
type ToolCallEvent struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// EventStore 负责全量事件日志的持久化。
// 追加写入，永不截断，是会话历史的真相源。
type EventStore struct {
	path string // events.jsonl 的完整路径
}

// NewEventStore 构造 EventStore，不立即创建目录（惰性创建）。
func NewEventStore(sessionDir string) *EventStore {
	return &EventStore{path: filepath.Join(sessionDir, "events.jsonl")}
}

// SetPath 切换底层文件路径（用于会话切换）。
func (s *EventStore) SetPath(sessionDir string) {
	s.path = filepath.Join(sessionDir, "events.jsonl")
}

// Path 返回当前 events.jsonl 路径。
func (s *EventStore) Path() string {
	return s.path
}

// Append 追加一条事件记录（追加写入，不读不改）。
func (s *EventStore) Append(event Event) error {
	if event.UUID == "" {
		event.UUID = generateUUID()
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().Format(time.RFC3339)
	}

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event failed: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create session dir failed: %w", err)
	}

	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open events file failed: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write event failed: %w", err)
	}
	return nil
}

// ReadAll 读取全部事件。
func (s *EventStore) ReadAll() ([]Event, error) {
	return s.readAll()
}

// ReadRound 读取指定轮次的所有事件。
func (s *EventStore) ReadRound(round int) ([]Event, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	var result []Event
	for _, e := range all {
		if e.Round == round {
			result = append(result, e)
		}
	}
	return result, nil
}

// RecentEvents 返回最近 n 条事件。
func (s *EventStore) RecentEvents(n int) ([]Event, error) {
	all, err := s.readAll()
	if err != nil {
		return nil, err
	}
	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// Count 返回事件总数。
func (s *EventStore) Count() (int, error) {
	all, err := s.readAll()
	if err != nil {
		return 0, err
	}
	return len(all), nil
}

// Digest 从事件流生成可读的文本摘要（用于 prompt 注入降级）。
func (s *EventStore) Digest(lastN int) string {
	if lastN <= 0 {
		return "(无历史记录)"
	}
	all, err := s.readAll()
	if err != nil || len(all) == 0 {
		return "(无历史记录)"
	}

	// 按轮次分组，只取 user 和 assistant 事件。
	rounds := make(map[int][]Event)
	for _, e := range all {
		if e.Type == EventUser || e.Type == EventAssistant {
			rounds[e.Round] = append(rounds[e.Round], e)
		}
	}

	// 收集最近 N 轮。
	var roundNums []int
	for r := range rounds {
		roundNums = append(roundNums, r)
	}
	// 排序
	for i := 0; i < len(roundNums); i++ {
		for j := i + 1; j < len(roundNums); j++ {
			if roundNums[i] > roundNums[j] {
				roundNums[i], roundNums[j] = roundNums[j], roundNums[i]
			}
		}
	}

	start := len(roundNums) - lastN
	if start < 0 {
		start = 0
	}

	var b strings.Builder
	for _, r := range roundNums[start:] {
		for _, e := range rounds[r] {
			switch e.Type {
			case EventUser:
				fmt.Fprintf(&b, "- 轮次 %d 用户=%q\n", r, trimText(e.Content, 80))
			case EventAssistant:
				fmt.Fprintf(&b, "- 轮次 %d 回复=%q\n", r, trimText(e.Content, 120))
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// readAll 从 JSONL 读取全部事件。
func (s *EventStore) readAll() ([]Event, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open events file failed: %w", err)
	}
	defer f.Close()

	var all []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		all = append(all, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan events file failed: %w", err)
	}
	return all, nil
}

// generateUUID 生成简单的 UUID（时间戳 + 随机）。
func generateUUID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), b)
}
