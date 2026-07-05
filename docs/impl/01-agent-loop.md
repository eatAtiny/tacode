# 1. Agent 循环

## 设计目标

实现一个 ReAct (Reasoning + Acting) 模式的 AI Agent，支持流式 LLM 响应、工具调用、权限确认和多轮对话。核心特点：

- **两层查询架构**：QueryEngine（协调层）+ queryLoop（纯 ReAct 核心）
- **异步生成器模式**：通过 channel 解耦核心逻辑与 UI/事件记录
- **Channel 主循环**：select 多路复用输入和查询结果，支持 `/stop` 中断
- **流式输出**：LLM 响应逐 token 推送，用户可实时看到思考过程

## 架构

```
main.go
  └── Runner.Run()
       ├── bufio.Scanner → 读取用户输入
       ├── QueryEngine.Query()
       │    ├── Retriever.BuildContext()  ← 3 层记忆
       │    ├── BuildSystemPrompt()       ← 工具描述 + PromptGuide
       │    ├── queryLoop() → <-chan QueryEvent  (异步)
       │    └── consume events → UI + EventStore
       └── loop: select { input, queryResult, stopSignal }
```

## 两层查询架构

### QueryEngine — 上层协调

```go
func (e *QueryEngine) Query(query string) string {
    // 1. 构建上下文（3 层记忆）
    context := e.retriever.BuildContext(query)

    // 2. 自动压缩检查（80% token 阈值）
    e.autoCompressCheck()

    // 3. 构建 system + user prompt
    systemPrompt := e.buildSystemPrompt()
    messages := e.buildMessages(query, context)

    // 4. 调用 ReAct 核心 → 获取事件流
    events := e.queryLoop(messages)

    // 5. 消费事件 → UI 显示 + EventStore 记录
    for evt := range events {
        e.ui.OnEvent(evt)
        e.eventStore.Append(evt)
    }

    return finalAnswer
}
```

职责：
- 上下文组装（记忆检索 + prompt 拼接）
- Token 预算管理（80% 阈值触发自动压缩）
- 事件分发（UI 渲染 + 持久化记录）
- 返回最终答案

### queryLoop — ReAct 核心

纯函数式循环，不依赖 UI 或存储：

```
while iter < maxIterations (10):
  1. LLM 流式调用
     ├── Think → UI 显示推理过程
     ├── Delta → UI 流式渲染回复文本
     └── Final → 无 tool_calls，返回最终答案

  2. 有 tool_calls 时：
     ├── 权限检查 (CheckPermission)
     │   ├── Allow → 直接执行
     │   └── Deny → Permission 事件 → 阻塞等待用户确认
     ├── 并发分类 (IsConcurrencySafe + IsReadOnly)
     │   ├── 并发组 → goroutine + WaitGroup
     │   └── 串行组 → 逐个执行
     ├── ToolCall / ToolResult 事件 → UI 显示
     └── 结果 push 回 messages → 继续循环

  3. 循环内自动压缩（80% token 阈值）
  4. 重复调用检测 → 注入 warning
  5. 达到 maxIterations → LLM 生成最终摘要
```

## QueryEvent 类型

```go
type QueryEventType int

const (
    QueryEventThink      // LLM 推理 token（reasoning_content）
    QueryEventDelta      // LLM 回复文本增量
    QueryEventToolCall   // 工具调用开始
    QueryEventToolResult // 工具调用结果
    QueryEventPermission // 需要用户确认（含 PermissionCh 用于回复）
    QueryEventFinal      // 最终答案
    QueryEventError      // 错误
)
```

每个事件通过 channel 发送，UI 层订阅并渲染对应组件。

## 并发工具执行

参照 Claude Code 的 StreamingToolExecutor 模式，按 per-input safety 动态分类：

```go
func (lc *queryLoopContext) executeToolCalls(toolCalls) {
    // 1. 分类
    for _, tc := range toolCalls {
        if t.IsConcurrencySafe(args) && t.IsReadOnly(args) && CheckPermission().Allow {
            concurrentItems ← tc   // 并发安全
        } else {
            serialItems ← tc        // 需串行
        }
    }

    // 2. 并发组：goroutine 并行
    if len(concurrentItems) > 0 {
        executeConcurrentTools(concurrentItems)
            ├── WaitGroup 同步
            ├── mutex 保护 fileReads 写入
            └── BeforeHooks / AfterHooks 调用
    }

    // 3. 串行组：逐个执行
    for _, item := range serialItems {
        executeSingleTool(item)
            ├── ReadState 注入 (ReadStateAware)
            ├── BeforeHooks → 可能阻断
            ├── Execute
            ├── AfterHooks (defer + recover)
            └── recordFileRead (mtime 记录)
    }
}
```

### 串行 vs 并发决策树

```
IsConcurrencySafe(args)?
  ├── false → 串行
  └── true → IsReadOnly(args)?
                ├── false → 串行
                └── true → CheckPermission().Allow?
                            ├── false → 串行
                            └── true → 并发
```

设计原则（fail-closed）：任一检查不通过就回退到串行。

## 中断机制

主循环通过 channel 多路复用实现 `/stop` 取消：

```go
select {
case input := <-inputCh:      // 用户新输入
    handle(input)
case result := <-resultCh:    // 查询完成
    display(result)
case <-stopCh:                // /stop 命令
    cancel()                   // 取消 context
}
```

## 权限系统

```go
type PermissionResult struct {
    Allow  bool    // true=直接执行，false=需确认
    Reason string  // 确认时展示给用户的原因
}
```

权限检查流程：
1. queryLoop 在 Execute 前调用 `t.CheckPermission(args)`
2. Allow=true → 直接执行
3. Allow=false → 发送 QueryEventPermission（含 PermissionCh），阻塞等待用户回复
4. 用户 y/N → 写入 PermissionCh → queryLoop 解除阻塞

高风险操作（rm -rf、sudo、chmod 777）会触发二次确认。

## 重复调用检测

当 LLM 连续两轮发出完全相同的 tool_calls（名称 + 参数一致）时，queryLoop 检测到重复并注入 warning 消息：

```
"⚠️ 重复调用检测：你刚才用相同参数调用了同一个工具。
请检查工具结果是否已满足需求，或尝试不同的参数。"
```

这防止 LLM 陷入无效的重复循环。

## Token 预算管理

两层压缩机制：

### 循环内压缩
当 messages 估计 token 超过 80% 模型上下文窗口时：
- 保留 system prompt + 最后 2 轮 tool 对话
- 压缩更早的消息（LLM 生成摘要）

### 记忆压缩
用户可手动触发 `/compress`：
- LLM 合并旧 L2 摘要（保留最新 3 条）
- 减少后续查询的上下文长度

## 关键常量和配置

| 常量 | 值 | 位置 |
|------|-----|------|
| `maxIterations` | 10 | `runner.go` |
| `temperature` | 0.2 | `llm/openai.go` |
| `contextThreshold` | 80% | `query_loop.go` |
| shell timeout | 30s | `tool/shell.go` |

## 文件清单

| 文件 | 职责 |
|------|------|
| `internal/agent/runner.go` | Runner — REPL 主循环 + 异步查询调度 |
| `internal/agent/query_engine.go` | QueryEngine — 上下文构建 + prompt 组装 + 事件消费 |
| `internal/agent/query_loop.go` | queryLoop — ReAct 核心 + 流式调用 + 工具执行 |
| `internal/agent/types.go` | QueryEvent / QueryEventType / queryResult |
| `internal/agent/permission.go` | ToolPermissionChecker / DefaultPermissionChecker / 风险检测 |
| `internal/agent/memory.go` | 记忆提取 + /compress / /memory 命令 |
| `internal/agent/session.go` | 会话命令 + ensurePersisted + cleanOrphanTempDirs |
