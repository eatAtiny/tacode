# CLI 一次运行模式（one-shot）设计文档

- 日期：2026-08-18
- 状态：已批准
- 范围：`internal/agent/runner.go` 新增 `RunOnce()` + 抽 `initTempSession()` + `main.go` 加 `-one-shot` flag + `internal/agent/runner_test.go` + `CLAUDE.md`

## 背景

Agent 目前只能通过 REPL 交互（`Runner.Run()`），`TextUI`（`internal/ui/text/text.go`）已实现 headless 模式但零入口。无法在脚本/CI/子 agent 场景复用 agent。本功能激活 TextUI，提供 `-one-shot "<任务>"` 一次性运行模式。

## 决策

| 决策点 | 结论 |
|--------|------|
| 触发方式 | `-one-shot "<任务>"` flag |
| UI | TextUI（headless，权限默认放行） |
| 输出 | 只打印最终答案到 stdout，中间事件丢弃 |
| 记忆 | 保存（L1/L2/L3 + 临时会话落盘），与 REPL 一致 |
| 退出码 | 成功 0，LLM/工具/查询错误 → 1 |
| 错误输出 | 错误信息到 stderr |

## 架构

```
main.go
  ├─ flag 解析: -one-shot 存在?
  │    ├─ yes → text.NewTextUI() → runner.RunOnce(ctx, task)
  │    │        → answer → stdout；err → stderr；退出码 0/1
  │    └─ no  → bubble.NewBubbleUI() → runner.Run()（现有 REPL）
  └─ RunOnce 内部（runner.go 新增）:
       ├─ 1. initTempSession()（从 Run() 抽出，两处共用）
       ├─ 2. 同步调 queryEngine(ctx, 1, input, nil)
       │       └─ inputForward=nil → TextUI.ConfirmPermission 默认放行
       ├─ 3. 成功 → history.Append + extractMemory + ensurePersisted
       └─ 4. 返回 (answer, error)
```

## Multi-agent 演进路径（设计预留）

`RunOnce(ctx, input) (string, error)` 就是子 agent 的正确原语——"一个子 agent"的本质是一次 headless、可编程消费结果的查询。本设计为 multi-agent 预留了三个接缝：

### 已兼容的点（无需改动）

1. **同步 + 结构化返回**：`RunOnce` 返回 `(string, error)`，父 agent 可拿到子 agent 答案并聚合。若只输出到 stdout 就断了这条路。
2. **独立会话目录**：`initTempSession()` 每次 `GenerateID()`，每个子 agent 跑在自己的 `data/sessions/<id>/` 下，L1/L2/L3 天然隔离，并行写不冲突。
3. **Headless UI**：TextUI 无终端、权限自动放行，子 agent 无需人工介入。

### 未来接缝（实现时需满足，当前不实现）

1. **`Runner` 实例工厂**：并行 N 个 agent 需要 N 个独立 `Runner` 实例（各自 store 指向不同会话目录）。需要一个 `NewRunner` 工厂函数（当前 `main.go` 手动拼装）。**约束**：`RunOnce` 不得依赖全局单例，保持实例方法。
2. **`agent` 工具注入点**：父 agent 要发起子 agent，需注册 `agent` 工具（类似 Claude Code 的 Task 工具），其 `Execute` 内调用 `RunOnce`。`tool.Registry.Register`（`tool.go:287`）是开放接口，加 `NewAgentTool()` 即可，无需改框架。
3. **事件流暴露（可选）**：`RunOnce` 目前只返回最终答案。multi-agent 下父 agent 可能订阅子 agent 进度——TextUI 的 `OnEvent` 回调（`text.go:50`）已存在，未来可给 `RunOnce` 加可选回调参数。不影响当前实现。

### 并行执行通道

现有 `executeToolCalls` 已有并行分类机制（`query_loop.go:334-373`）：`IsConcurrencySafe && IsReadOnly && Allow` 的工具自动 goroutine 并行执行。未来的 `agent` 工具声明 `IsConcurrencySafe=true` 后，多个子 agent 自动并行跑——框架层面的并行通道已存在。

## 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/agent/runner.go` | 抽 `initTempSession()`；新增 `RunOnce(ctx, input) (string, error)` |
| `main.go` | `-one-shot` flag；UI 分支；one-shot 执行路径 |
| `internal/agent/runner_test.go` | 新增（initTempSession / RunOnce 空输入 / 权限自动放行） |
| `CLAUDE.md` | Build/Run 命令加 one-shot 示例 |

## RunOnce 流程

复用 `Run()`（`internal/agent/runner.go:132-146`）的现有逻辑：

1. **initTempSession()**：`isTemporary=true`、`session.GenerateID()`、`history/summary/memStore/events.SetPath(tempDir)`、`cleanOrphanTempDirs()`。从 `Run()` 抽出，两处共用避免复制。
2. **设置 UI 状态**：`SetSessionName("new")` + `SetModel()`（TextUI 中为空操作，但保持调用统一）。
3. **同步调用**：`answer, err := r.queryEngine(ctx, 1, input, nil)`。
4. **保存记忆**：成功时与 `Run()` 分支 B 一致——`history.Append` → `extractMemory` → `ensurePersisted`。

## nil inputForward 安全性

`queryEngine` 仅在 `QueryEventPermission` 分支使用 `inputForward`（`query_engine.go:172`），传给 `ui.ConfirmPermission`。TextUI 的 `ConfirmPermission`（`text.go:137-142`）**不读**该参数，直接返回 `(true, nil)` —— nil 安全，权限默认放行。

## main.go 分支

```go
// UI 实例按模式分支
var uiInstance ui.UI
if *oneShot != "" {
    uiInstance = text.NewTextUI()
} else {
    uiInstance = bubble.NewBubbleUI()
}
defer uiInstance.Close()

// ── 步骤 12: one-shot 模式 ──
// flag 非空时跳过 REPL，同步执行单次查询后退出。
if *oneShot != "" {
    runner := agent.NewRunner(...)
    answer, err := runner.RunOnce(context.Background(), *oneShot)
    if err != nil {
        fmt.Fprintln(os.Stderr, "one-shot 执行失败:", err)
        os.Exit(1)
    }
    fmt.Println(answer)
    return // 让 defer uiInstance.Close() 正常执行
}
```

注意：one-shot 成功路径用 `return`（而非 `os.Exit(0)`）让 `defer uiInstance.Close()` 执行；失败路径 `os.Exit(1)` 跳过 defer——TextUI 的 `Close()` 为空操作（`text.go:145`），无资源泄漏。

## 初始化时序

`main.go` 现有初始化链（步骤 3-10：LLM client / sessions / memory stores / extractor / retriever / tools）对两种模式完全一致，**不分支**。仅步骤 11（UI 实例）和步骤 12（执行入口）按模式分支：

- REPL：步骤 11 = BubbleUI，步骤 12 = `runner.Run()`
- one-shot：步骤 11 = TextUI，步骤 12 = `runner.RunOnce()`

`RunOnce` 的临时会话初始化发生在方法内部（复用 `initTempSession()`），依赖 `sessions` 管理器已就绪（步骤 4 完成）。

## 错误处理

- LLM 调用失败 / 工具致命错误 / 查询循环错误 → `RunOnce` 返回 error → 打印 stderr → 退出码 1
- 空输入 → `RunOnce` 返回错误（"输入为空"），不调用 LLM

## 测试计划（`internal/agent/runner_test.go`）

| 测试 | 覆盖点 |
|------|--------|
| `TestRunOnce_EmptyInput` | 空输入 → 返回错误，不调用 LLM |
| `TestRunOnce_InitTempSession` | initTempSession 后目录被创建、store 路径指向 tempDir |
| `TestRunOnce_TextUIPermissionAutoApprove` | TextUI.ConfirmPermission 返回 true（权限自动放行） |

LLM 全链路（真实 API 调用）不做单测，靠冒烟测试验证（`go run . -one-shot "..."`）。

## 集成

- `CLAUDE.md` Build/Run 命令加：
  ```bash
  go run . -one-shot "列出当前目录文件"   # 一次性运行，headless，输出最终答案
  ```
- 完成标准：`go build ./...` + `go test ./...` 全绿
