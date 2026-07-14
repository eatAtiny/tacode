# 5. 提示词与上下文工程

## 设计目标

通过精心维护上下文结构来最大化 LLM API 的前缀缓存（Prefix Cache）命中率。核心原则：

- **动静分离**：不变内容放前（全局缓存命中），可变内容放后（最小化缓存断点）
- **会话级复用**：压缩后的消息缓冲区跨查询持久化（context.json），不丢工具调用历史
- **锚点估算**：利用 API 返回的精确 `prompt_tokens` 作为锚点，只估算新增消息

## 架构总览

```
┌─ prompt/prompt.go (底层工厂) ─────────────────────────────┐
│ BuildReActStaticPrompt()    → 静态段（所有用户相同）       │
│ BuildReActDynamicPrompt()   → 动态段（会话级稳定）         │
│ BuildReActSystemPrompt()    → 完整 system prompt          │
│ BuildSystemReminder()       → <system-reminder> 包装      │
│ BuildUserTask()             → 轮次 + 用户任务              │
└───────────────────────────────────────────────────────────┘
         │ 组合
         ▼
┌─ prompt/builder.go (对外门面) ─────────────────────────────┐
│ NewBuilder(toolDescs, guides) → 预构建 system prompt       │
│ BuildMessages(opts)           → 全新 3 段消息              │
│ ExtendContext(existing, opts) → 复用历史 + 追加新轮次      │
└───────────────────────────────────────────────────────────┘
         │ 传入
         ▼
┌─ agent/query_engine.go ──┐      ┌─ memory/context_store.go ┐
│ Load() → Build/Extend    │ ←──→ │ context.json 读写        │
│ → queryLoop() → Save()   │      │ SessionContext 生命周期   │
└──────────────────────────┘      └──────────────────────────┘
```

## 消息结构（3 段布局）

```
messages[0] system:
  ┌─ Static (全局可缓存, 永不变) ───────────────────────────┐
  │ 你是一个具备工具调用能力的 AI 助手...                    │
  │ ## 可用工具 (JSON Schema, 固定顺序)                      │
  │ ## 工作方式 (4 步)                                       │
  │ ## 注意事项 (6 条)                                       │
  │ <system-reminder> 标签说明                               │
  │ ## 工具结果说明 (截断语义 + 持久化路径)                   │
  └──────────────────────────────────────────────────────────┘
  ──── __SYSTEM_PROMPT_DYNAMIC_BOUNDARY__ ──────────────────
  ┌─ Dynamic (会话稳定) ────────────────────────────────────┐
  │ ## 工具使用指南 (PromptGuide 汇总)                       │
  └──────────────────────────────────────────────────────────┘

messages[1] user (system-reminder, contextDigest 非空时):
  <system-reminder>
  会话环境:
  - 工作目录: /path/to/project           ← 会话不变
  - 会话开始时间: 2026-07-09 10:30:00    ← 会话不变
  上下文生成时间: 2026-07-09 10:30:05     ← 本轮生成
  {L3 索引 + L3 重要记忆 + L2 摘要}       ← 偶尔变
  </system-reminder>

messages[2] user (task):
  轮次: 3

  用户任务:
  fix the bug
```

### 为什么是 3 段

| 消息 | 变化频率 | 缓存行为 |
|------|---------|---------|
| `[0] system` | 永不变（静态段对所有用户相同） | 全局命中 |
| `[1] system-reminder` | 会话级（workDir + startTime 不变，记忆偶尔更新） | 会话内大概率命中 |
| `[2] user task` | 每轮变（但极简：只有轮次+输入） | 断点代价最小 |

对比旧版（2 段，上下文嵌入 user prompt）：记忆上下文变化导致整个 `messages[1]` 缓存断掉，然后所有后续的 ReAct 消息全部重建缓存。新版将变化隔离到 `[2]`，前面两段尽可能保持稳定。

## 动静分离哨兵

```
__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__
```

写在 system prompt 中，静态段和动态段的分界线。哨兵之上对所有部署实例相同（可全局缓存），哨兵之下是工具使用指南（会话级稳定）。

对应代码：
```go
// internal/prompt/prompt.go
const StaticDynamicBoundary = "__SYSTEM_PROMPT_DYNAMIC_BOUNDARY__"

func BuildReActSystemPrompt(toolDescriptions string, guides []ToolGuide) string {
    static := BuildReActStaticPrompt(toolDescriptions)
    dynamic := BuildReActDynamicPrompt(guides)
    if dynamic == "" {
        return static
    }
    return static + "\n" + StaticDynamicBoundary + "\n" + dynamic
}
```

## Builder 模式

`Builder` 封装消息组装逻辑，agent 循环不关心消息内部结构：

```go
// 会话开始时创建一次
b := prompt.NewBuilder(toolDescs, guides)

// 首次查询
msgs := b.BuildMessages(prompt.BuildOptions{Round: 1, ...})

// 后续查询（复用历史）
msgs := b.ExtendContext(existingMsgs, prompt.BuildOptions{Round: 3, ...})
```

`BuildMessages` 创建全新的 3 段消息数组。`ExtendContext` 保留 system prompt + 历史对话，只替换 per-turn 的 system-reminder 和 user task。

## 锚点令牌估算

利用 API 每次返回的 `usage.prompt_tokens` 作为精确锚点：

```
纯启发式: 所有消息逐字符估算 → ~30% 误差
锚点法:   API 精确值(prompt_tokens) + 只估算新增消息 → <5% 误差
```

实现位置：`internal/agent/query_loop.go`

```go
type queryLoopContext struct {
    anchorTotalTokens  int  // 上次 API 返回的 prompt_tokens
    anchorMessageCount int  // 锚点时的消息条数
}

func (lc *queryLoopContext) estimateTokensAnchored() int {
    if lc.anchorTotalTokens == 0 {
        return estimateMessagesTokens(lc.messages)  // 退化为纯启发式
    }
    delta := len(lc.messages) - lc.anchorMessageCount
    if delta <= 0 {
        return lc.anchorTotalTokens  // 无新增，直接用精确值
    }
    newTokens := estimateMessagesTokens(lc.messages[lc.anchorMessageCount:])
    return lc.anchorTotalTokens + newTokens
}
```

锚点在每次 `StreamEventDone`（API 调用完成）后存储。压缩消息后重置为 0（消息结构变化导致旧锚点失效）。

## 会话上下文持久化 (context.json)

### 问题

每个查询结束后 `lc.messages` 被 GC，下次查询从头开始。压缩后的工具调用历史全部丢失，模型无法跨查询引用之前的操作结果。

### 方案

```json
// data/sessions/<id>/context.json
{
  "messages": [
    {"role": "system", "content": "..."},
    {"role": "user", "content": "<system-reminder>..."},
    {"role": "user", "content": "轮次: 5\n用户任务: ..."},
    {"role": "assistant", "content": "", "tool_calls": [...]},
    {"role": "tool", "content": "..."},
    {"role": "assistant", "content": "最终回答..."}
  ],
  "round": 5,
  "updated_at": "2026-07-09T10:30:05Z"
}
```

### 数据流

```
首次查询:
  Load() → nil
  BuildMessages() → 3 段新消息
  queryLoop → ReAct 追加 + 压缩
  Save() → context.json

后续查询:
  Load() → 之前的 messages（含 system + 历史对话）
  ExtendContext() → 保留 system + 历史，替换 per-turn 消息
  queryLoop → 在历史基础上继续
  Save() → context.json（含压缩后的最新状态）
```

### 关键细节

- **剥离旧 per-turn**：`ExtendContext` 保留 `messages[0]`（system）和 `messages[3:]`（对话历史），替换 `[1]`（旧 reminder）和 `[2]`（旧 task）
- **会话切换**：`ContextStore.SetPath()` 跟随 `switchSession()`, 各会话独立的 `context.json`
- **临时会话**：`ensurePersisted()` 中迁移 `context.json` 从临时目录到正式目录
- **首次查询**：文件不存在 → `Load()` 返回 nil → 走 `BuildMessages`

### 与 events.jsonl 的关系

| | context.json | events.jsonl |
|------|-------------|-------------|
| 内容 | 压缩后的当前工作集 | 全量事件日志 |
| 格式 | 单个 JSON 对象 | JSONL 追加 |
| 生命周期 | 每次查询后覆写 | 只增不删（真相源） |
| 用途 | 查询间上下文延续 | 完整历史回放 |

## system-reminder 注入

所有系统级上下文注入统一使用 `<system-reminder>` 标签包装：

```go
func BuildSystemReminder(contextDigest string) string {
    return "<system-reminder>\n" + contextDigest + "\n</system-reminder>"
}
```

System prompt 静态段声明了标签语义：

```
工具结果和用户消息可能包含 <system-reminder> 标签。
其中的内容是系统自动注入的上下文信息，与你当前的具体任务可能无关，
仅供参考。
```

压缩保护：`compressMessages` 中 `isSystemReminder()` 检测并跳过 `<system-reminder>` 消息，确保注入的上下文不被压缩。

## 工具结果说明

System prompt 静态段包含工具结果截断语义说明：

```
## 工具结果说明
- 工具结果可能因内容过长而被截断。截断时保留头部和尾部，并标明原始总长度。
- 工具结果末尾出现"💾 完整结果已保存到: <路径>"时，说明完整内容已持久化到磁盘，
  你可以使用 file read 读取该路径获取完整内容。
- 如果截断后的信息不足以回答问题，先尝试用更精细的工具获取补充信息，
  而不是重复调用同一个已截断的工具。
```

## 环境元数据注入

工作目录和会话开始时间注入在 system-reminder 中（会话级稳定，不破坏缓存）：

```
会话环境:
- 工作目录: /home/ubuntu/workspace/agentic
- 会话开始时间: 2026-07-09 10:30:00
```

`sessionStartTime` 在 `Runner.Run()` 启动时捕获一次，整个会话不变。

## 缓存命中分析

```
Turn N, Iter 1 (首次查询):
  → [0]system + [1]reminder(new) + [2]task(new)
  → API 缓存 [0] (前缀匹配)，[1][2] 写入缓存

Turn N, Iter 2 (ReAct 工具调用):
  → [0]system + [1]reminder(不变) + [2]task(不变)
    + [3]assistant(tool_calls) + [4]tool(result)
  → [0][1][2] 全部命中！仅 [3][4] 新增

Turn N+1 (下一轮用户输入):
  → [0]system(命中) + [1]reminder(可能更新 L2/L3)
    + [2]task(新输入) + [3:]history(从 context.json 恢复)
  → system 始终命中，reminder 大概率命中
```

## 文件映射

| 文件 | 职责 |
|------|------|
| `internal/prompt/prompt.go` | 底层工厂函数（动静分离、system-reminder、user task） |
| `internal/prompt/builder.go` | Builder 门面（BuildMessages、ExtendContext） |
| `internal/memory/context_store.go` | SessionContext + ContextStore（context.json 读写） |
| `internal/agent/query_loop.go` | 锚点令牌估算、system-reminder 压缩保护 |
| `internal/agent/query_engine.go` | 消息组装协调（Load → Build/Extend → queryLoop → Save） |
