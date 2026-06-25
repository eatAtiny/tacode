package session

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SessionMeta 表示单个会话的元数据。
type SessionMeta struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created string `json:"created"`
	Updated string `json:"updated"`
}

// manifest 是 sessions/manifest.json 的持久化结构。
type manifest struct {
	Active   string        `json:"active"`
	Sessions []SessionMeta `json:"sessions"`
}

// SessionManager 管理多个独立会话。
type SessionManager struct {
	dir  string // sessions 目录路径
	data manifest
}

// NewSessionManager 加载或创建会话清单。
// 首次运行时自动创建目录和一个「默认会话」。
func NewSessionManager(dir string) (*SessionManager, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create sessions dir failed: %w", err)
	}

	m := &SessionManager{dir: dir}
	path := m.manifestPath()

	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read manifest failed: %w", err)
		}
		// 首次运行：创建默认会话
		id, err := generateID()
		if err != nil {
			return nil, fmt.Errorf("generate session id failed: %w", err)
		}
		now := time.Now().Format(time.RFC3339)
		m.data = manifest{
			Active: id,
			Sessions: []SessionMeta{{
				ID:      id,
				Name:    "默认会话",
				Created: now,
				Updated: now,
			}},
		}
		if err := m.save(); err != nil {
			return nil, err
		}
		// 创建默认会话的子目录。
		if err := os.MkdirAll(m.SessionDir(id), 0o755); err != nil {
			return nil, fmt.Errorf("create default session dir failed: %w", err)
		}
		return m, nil
	}

	if err := json.Unmarshal(data, &m.data); err != nil {
		return nil, fmt.Errorf("parse manifest failed: %w", err)
	}

	// 迁移旧格式：将 <id>.jsonl 移入 <id>/history.jsonl。
	for _, s := range m.data.Sessions {
		m.migrateOldFormat(s.ID)
	}

	return m, nil
}

// Create 创建一个新会话并自动切换过去，返回会话 ID。
func (m *SessionManager) Create(name string) (string, error) {
	id, err := generateID()
	if err != nil {
		return "", fmt.Errorf("generate session id failed: %w", err)
	}
	if name == "" {
		name = fmt.Sprintf("会话 %d", len(m.data.Sessions)+1)
	}
	now := time.Now().Format(time.RFC3339)
	m.data.Sessions = append(m.data.Sessions, SessionMeta{
		ID:      id,
		Name:    name,
		Created: now,
		Updated: now,
	})
	m.data.Active = id
	if err := m.save(); err != nil {
		return "", err
	}
	// 创建会话子目录。
	if err := os.MkdirAll(m.SessionDir(id), 0o755); err != nil {
		return "", fmt.Errorf("create session dir failed: %w", err)
	}
	return id, nil
}

// List 返回所有会话元数据（按创建时间排序）。
func (m *SessionManager) List() []SessionMeta {
	sort.Slice(m.data.Sessions, func(i, j int) bool {
		return m.data.Sessions[i].Created < m.data.Sessions[j].Created
	})
	out := make([]SessionMeta, len(m.data.Sessions))
	copy(out, m.data.Sessions)
	return out
}

// ActiveID 返回当前活跃会话的 ID。
func (m *SessionManager) ActiveID() string {
	return m.data.Active
}

// Switch 切换到指定 ID 的会话。支持 ID 前缀匹配。
func (m *SessionManager) Switch(id string) error {
	s := m.findByPrefix(id)
	if s == nil {
		return fmt.Errorf("会话 %q 不存在", id)
	}
	m.data.Active = s.ID
	m.touch(s.ID)
	return m.save()
}

// Delete 删除指定 ID 的会话及其整个目录。不能删除当前活跃会话。
func (m *SessionManager) Delete(id string) error {
	s := m.findByPrefix(id)
	if s == nil {
		return fmt.Errorf("会话 %q 不存在", id)
	}
	if s.ID == m.data.Active {
		return fmt.Errorf("不能删除当前活跃的会话")
	}
	// 删除整个会话目录。
	os.RemoveAll(m.SessionDir(s.ID))
	// 兼容：也删除旧格式的 .jsonl 文件。
	os.Remove(filepath.Join(m.dir, s.ID+".jsonl"))
	// 从列表移除。
	for i, sess := range m.data.Sessions {
		if sess.ID == s.ID {
			m.data.Sessions = append(m.data.Sessions[:i], m.data.Sessions[i+1:]...)
			break
		}
	}
	return m.save()
}

// Rename 重命名指定 ID 的会话。
func (m *SessionManager) Rename(id, name string) error {
	s := m.findByPrefix(id)
	if s == nil {
		return fmt.Errorf("会话 %q 不存在", id)
	}
	s.Name = name
	s.Updated = time.Now().Format(time.RFC3339)
	return m.save()
}

// Dir 返回 sessions 目录路径。
func (m *SessionManager) Dir() string {
	return m.dir
}

// SessionDir 返回指定会话的子目录路径。
func (m *SessionManager) SessionDir(id string) string {
	return filepath.Join(m.dir, id)
}

// ActiveSessionDir 返回当前活跃会话的子目录路径。
func (m *SessionManager) ActiveSessionDir() string {
	return m.SessionDir(m.data.Active)
}

// MemoryDir 返回指定会话的 memory/ 子目录路径。
func (m *SessionManager) MemoryDir(id string) string {
	return filepath.Join(m.dir, id, "memory")
}

// ActiveMemoryDir 返回当前活跃会话的 memory/ 子目录路径。
func (m *SessionManager) ActiveMemoryDir() string {
	return m.MemoryDir(m.data.Active)
}

// ActivePath 返回当前活跃会话的 history.jsonl 文件路径。
func (m *SessionManager) ActivePath() string {
	return m.SessionPath(m.data.Active)
}

// SessionPath 返回指定会话的 history.jsonl 文件路径。
func (m *SessionManager) SessionPath(id string) string {
	return filepath.Join(m.dir, id, "history.jsonl")
}

// TempPath 返回临时会话的 history.jsonl 文件路径（不在 manifest 中）。
func (m *SessionManager) TempPath(tempID string) string {
	return filepath.Join(m.dir, tempID, "history.jsonl")
}

// FindMeta 根据 ID 前缀查找会话元数据，找不到返回 nil。
func (m *SessionManager) FindMeta(id string) *SessionMeta {
	return m.findByPrefix(id)
}

// manifestPath 返回 manifest.json 的完整路径。
func (m *SessionManager) manifestPath() string {
	return filepath.Join(m.dir, "manifest.json")
}

// save 将清单写回磁盘。
func (m *SessionManager) save() error {
	data, err := json.MarshalIndent(m.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest failed: %w", err)
	}
	if err := os.WriteFile(m.manifestPath(), data, 0o644); err != nil {
		return fmt.Errorf("write manifest failed: %w", err)
	}
	return nil
}

// findByPrefix 按 ID 前缀匹配会话。精确匹配优先，再尝试前缀。
func (m *SessionManager) findByPrefix(prefix string) *SessionMeta {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return nil
	}
	// 精确匹配
	for i := range m.data.Sessions {
		if m.data.Sessions[i].ID == prefix {
			return &m.data.Sessions[i]
		}
	}
	// 前缀匹配
	var match *SessionMeta
	for i := range m.data.Sessions {
		if strings.HasPrefix(m.data.Sessions[i].ID, prefix) {
			if match != nil {
				return nil // 多个匹配，不明确
			}
			match = &m.data.Sessions[i]
		}
	}
	return match
}

// touch 更新指定会话的 Updated 时间。
func (m *SessionManager) touch(id string) {
	now := time.Now().Format(time.RFC3339)
	for i := range m.data.Sessions {
		if m.data.Sessions[i].ID == id {
			m.data.Sessions[i].Updated = now
			return
		}
	}
}

// migrateOldFormat 将旧格式的 <id>.jsonl 迁移到新格式 <id>/history.jsonl。
func (m *SessionManager) migrateOldFormat(id string) {
	oldPath := filepath.Join(m.dir, id+".jsonl")
	newDir := m.SessionDir(id)
	newPath := m.SessionPath(id)

	// 如果旧文件不存在，无需迁移。
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		return
	}
	// 如果新路径已存在，无需迁移。
	if _, err := os.Stat(newPath); err == nil {
		// 清理旧文件。
		os.Remove(oldPath)
		return
	}

	// 创建新目录并移动文件。
	os.MkdirAll(newDir, 0o755)
	if err := os.Rename(oldPath, newPath); err != nil {
		// 迁移失败，保留旧文件不动。
		return
	}
}

// GenerateID 生成短会话 ID：日期 + 随机 4 字节十六进制。
func GenerateID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%x", time.Now().Format("20060102-150405"), b), nil
}

// generateID 内部别名，保持兼容。
func generateID() (string, error) {
	return GenerateID()
}
