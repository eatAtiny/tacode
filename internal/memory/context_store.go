package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agentic/internal/llm"
)

// ──────────────────────────────────────────────────────────
// SessionContext — 会话级压缩后的消息缓冲区
// ──────────────────────────────────────────────────────────

// SessionContext 存储会话当前的压缩后消息状态。
//
// 与 events.jsonl（全量历史）的关系：
//   - events.jsonl = 完整事件日志，不可压缩的真相源
//   - context.json = 压缩后的当前工作集，查询间复用
//
// 消息上限：~40 条。超过时旧轮次被压缩为摘要并通过 system-reminder 注入。
type SessionContext struct {
	Messages  []llm.ChatMessage `json:"messages"`   // 当前消息缓冲区
	Round     int               `json:"round"`      // 当前轮次号
	UpdatedAt time.Time         `json:"updated_at"` // 最后更新时间
}

// ──────────────────────────────────────────────────────────
// ContextStore — context.json 持久化
// ──────────────────────────────────────────────────────────

// ContextStore 管理会话的上下文快照文件（context.json）。
//
// 与 SummaryStore 使用相同的 SetPath 模式：
//   - 会话切换时调用 SetPath 指向新目录
//   - Load/Save 操作当前目录下的 context.json
//
// 文件格式：单个 JSON 对象（整体覆写，非 JSONL 追加）。
type ContextStore struct {
	path string // context.json 的完整路径
}

// NewContextStore 构造 ContextStore，不立即创建文件（惰性创建）。
func NewContextStore(sessionDir string) *ContextStore {
	return &ContextStore{path: filepath.Join(sessionDir, "context.json")}
}

// SetPath 切换底层文件路径（会话切换时调用）。
func (cs *ContextStore) SetPath(sessionDir string) {
	cs.path = filepath.Join(sessionDir, "context.json")
}

// Path 返回当前上下文文件的路径。
func (cs *ContextStore) Path() string {
	return cs.path
}

// Load 从 context.json 加载会话上下文。
//
// 文件不存在时返回 nil（首次查询，走 BuildMessages 创建新上下文）。
func (cs *ContextStore) Load() (*SessionContext, error) {
	data, err := os.ReadFile(cs.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read context file: %w", err)
	}

	var ctx SessionContext
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse context file: %w", err)
	}
	return &ctx, nil
}

// Save 将会话上下文写入 context.json（整体覆写，惰性创建目录）。
func (cs *ContextStore) Save(ctx *SessionContext) error {
	if ctx == nil {
		return nil
	}

	ctx.UpdatedAt = time.Now()

	if err := os.MkdirAll(filepath.Dir(cs.path), 0o755); err != nil {
		return fmt.Errorf("create context dir: %w", err)
	}

	data, err := json.MarshalIndent(ctx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context: %w", err)
	}

	if err := os.WriteFile(cs.path, data, 0o644); err != nil {
		return fmt.Errorf("write context file: %w", err)
	}

	return nil
}

// Delete 删除当前的 context.json（/new 时调用）。
func (cs *ContextStore) Delete() error {
	if err := os.Remove(cs.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete context file: %w", err)
	}
	return nil
}
