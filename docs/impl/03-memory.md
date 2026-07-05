# 3. 记忆系统

## 设计目标

实现三层记忆架构，从原始对话到结构化长期记忆逐层提炼。核心原则：

- **分层存储**：L1 原始记录 → L2 摘要 → L3 结构化记忆，越往上层信息密度越高
- **增量提取**：每轮对话后 LLM 自动提取摘要和记忆动作
- **上下文预算**：构建查询上下文时按优先级组合各层记忆

## 架构

```
用户对话
   │
   ▼
EventStore (events.jsonl)        ← 完整事件日志，只增不删（真相源）
   │
   ▼
Extractor (LLM 调用)
   ├──→ L2 SummaryStore          ← 每轮对话摘要 (~200 tokens/条)
   └──→ L3 MemoryStore           ← markdown 文件 + frontmatter
         ├── create (新记忆)
         ├── update (更新已有记忆)
         └── delete  (删除过时记忆)

L1 HistoryStore (history.jsonl)   ← 原始对话记录 (最近 50 条)
```

## 三层存储

| 层 | 存储 | 文件 | 容量 | 用途 |
|----|------|------|------|------|
| L1 | HistoryStore | `history.jsonl` | 最近 50 条 | 原始对话记录，JSONL 格式 |
| L2 | SummaryStore | `summaries.jsonl` | 最近 10 条 | LLM 提取的每轮摘要 |
| L3 | MemoryStore | `memory/*.md` | 无限制 | 结构化长期记忆（frontmatter + 内容） |

### L1: HistoryStore — 原始记录

```go
type Record struct {
    ID        string    `json:"id"`
    Timestamp time.Time `json:"timestamp"`
    Role      string    `json:"role"`      // "user" | "assistant"
    Content   string    `json:"content"`
}
```

- 追加写入 `history.jsonl`
- 超过 50 条时自动截断（保留最新）
- 作为记忆提取的原始素材

### L2: SummaryStore — 对话摘要

```go
type Summary struct {
    ID        string    `json:"id"`
    Round     int       `json:"round"`
    Content   string    `json:"content"`     // ~200 tokens 摘要
    CreatedAt time.Time `json:"created_at"`
}
```

- 每轮对话后 LLM 提取一条摘要
- 追加写入 `summaries.jsonl`
- 上下文构建时取最近 10 条
- 手动压缩 (`/compress`)：保留最新 3 条，将旧的合并为一条

### L3: MemoryStore — 结构化长期记忆

Markdown 文件，带 YAML frontmatter：

```markdown
---
name: <kebab-case-slug>
description: <one-line summary>
metadata:
  type: user | feedback | project | reference
---

<memory content>
**Why:** ...
**How to apply:** ...
```

- 每个文件一条记忆
- `MEMORY.md` 作为索引文件（一行一条链接）
- 支持 CRUD：LLM 每轮可返回 create/update/delete 动作
- 按重要性过滤：high-importance 记忆始终进入上下文

## 上下文构建

```go
func (r *Retriever) BuildContext(query string) string {
    // 1. L3 记忆（高重要性 + 关键词匹配）
    memoryContext := r.loadRelevantMemories(query)

    // 2. L2 摘要（最近 10 条）
    summaries := r.summaryStore.Recent(10)

    // 3. 回退链（任一层为空时向下回退）
    if len(summaries) == 0 {
        summaries = r.digestFromEvents()     // EventStore → LLM 摘要
        if len(summaries) == 0 {
            summaries = r.digestFromHistory() // HistoryStore → LLM 摘要
        }
    }

    return combine(memoryContext, summaries)
}
```

## 记忆提取

每轮对话结束后，Extractor 发起一次 LLM 调用，同时生成 L2 摘要和 L3 记忆动作：

```go
type ExtractionResult struct {
    Summary string         // L2 摘要文本
    Memories []MemoryAction // L3 记忆操作
}

type MemoryAction struct {
    Action  string // "create" | "update" | "delete"
    Name    string // 记忆文件名 (slug)
    Content string // 记忆内容（create/update 时）
}
```

提取在后台 goroutine 中执行，不阻塞用户交互。

## 自动压缩

### 查询引擎层（80% 阈值）

在构建查询上下文时，如果估计 token 超过模型窗口的 80%：
- 调用 LLM 压缩旧的 L2 摘要
- 保留最新 3 条，更早的合并为一条

### ReAct 循环内

当 messages 超过 80% 阈值时：
- 保留 system prompt + 最后 2 轮 tool 对话
- 压缩更早的消息

### 手动触发

用户执行 `/compress`：同上逻辑，同步执行。

## 文件清单

| 文件 | 职责 |
|------|------|
| `internal/memory/types.go` | MemoryEntry、Summary、ExtractionResult、Record、MemoryAction |
| `internal/memory/event.go` | EventStore — 追加式 JSONL，真相源 |
| `internal/memory/history.go` | HistoryStore — L1，最近 50 条 |
| `internal/memory/summary_store.go` | SummaryStore — L2，JSONL |
| `internal/memory/store.go` | MemoryStore — L3，md 文件 + frontmatter |
| `internal/memory/extractor.go` | Extractor — LLM 驱动的记忆提取 |
| `internal/memory/retriever.go` | Retriever — 3 层检索 + 压缩 |
