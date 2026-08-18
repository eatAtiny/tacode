# Agentic 完善方案

> 版本：v1.0（2026-08-18）
> 目标：把 Agentic 从"能跑的交互 demo"提升为"可被调用的 Coding Agent"
> 范围：7 项改进，分 3 个优先级批次；每项含现状、问题、方案、验收标准

---

## 概述

基于对代码库的完整阅读（`main.go`、`internal/agent/`、`internal/memory/`、`internal/tool/`、`internal/ui/`、`internal/llm/`），当前项目已完成：ReAct 循环、三层记忆、权限系统、6 个内置工具（含刚完成的 git 工具）、双 UI 框架。骨架齐全，但存在三个结构性问题：

1. **只进不出**：只能进 REPL 交互，`TextUI`（headless，`internal/ui/text/`）已实现但零入口，无法被脚本/CI/子 agent 调用
2. **不认识仓库**：每轮仅靠三层记忆 + 硬编码 prompt，从不读取仓库的指令文件（CLAUDE.md/AGENTS.md 等价物），工具调用质量无上限
3. **全硬编码**：temperature、maxIter、resultLimit、compressThreshold 散落代码各处，调参必须改代码重编译

按"价值/成本"排序，7 项改进分 3 个批次：

| 批次 | 项目 | 价值 | 成本 | 优先级 |
|------|------|------|------|--------|
| P0 | ① CLI 一次运行模式 | 高 | 低 | ⭐⭐⭐ |
| P0 | ② 项目上下文注入 | 高 | 低 | ⭐⭐⭐ |
| P0 | ③ git 工具增强（diff --stat） | 中 | 极低 | ⭐⭐ |
| P1 | ④ LLM 调用重试 | 高 | 低 | ⭐⭐ |
| P1 | ⑤ 配置化（config 文件） | 中 | 中 | ⭐⭐ |
| P2 | ⑥ 跨会话记忆 | 中 | 中 | ⭐ |
| P2 | ⑦ 工具循环内中断 | 中 | 高 | ⭐ |

---

## 批次 P0 — 立即可做，价值最高

### ① CLI 一次运行模式（one-shot）

**现状**：`main.go:162` 写死 `uiInstance := bubble.NewBubbleUI()`。`internal/ui/text/text.go` 已实现 TextUI（headless、callback 驱动、自动批准权限），但没有任何代码路径使用它。整个 agent 只能通过 REPL 手动交互。

**问题**：
- 无法在脚本/CI/子 agent 场景复用 agent
- TextUI 的价值完全浪费——它本来就是为"sub-agent 场景"写的

**方案**：给 `main.go` 加 `-one-shot <任务>` flag。当提供时：
1. 使用 TextUI 而非 BubbleUI（`main.go:162` 处按 flag 分支）
2. 跳过 REPL 主循环（`runner.go:132` 的 `Run()`），直接执行一次查询
3. 输出最终答案到 stdout，非零退出码表示失败
4. 权限自动批准（TextUI 已有此行为）

**涉及文件**：
- `main.go` — flag 解析 + UI 分支 + 调用新入口
- `internal/agent/runner.go` — 新增 `RunOneShot(ctx, input string) (string, error)`，复用 `queryEngine()`（`runner.go:318`）
- `internal/ui/text/text.go` — 确认 OnEvent 回调已能收 Final 事件（只需验证）

**验收标准**：
- `go run . -one-shot "列出当前目录文件"` 在无 TTY 环境输出答案并退出
- 返回码：成功 0，LLM/工具错误非 0
- `-one-shot` 与 `-sessions` 正常组合使用

---

### ② 项目上下文注入（CLAUDE.md / AGENTS.md 等价物）

**现状**：每轮查询由 `Retriever.BuildContext(query)`（`internal/memory/retriever.go`）构建上下文，但只检索三层记忆。system prompt 由 `BuildReActPrompt()`（`internal/prompt/prompt.go`）构建，内容固定。agent 对"我在哪个仓库、仓库约定是什么"一无所知。

**问题**：
- LLM 不知道仓库结构、语言、约定 → 工具调用盲目
- 这是 Coding Agent 与通用 chatbot 的本质区别，目前缺失

**方案**：在 `Retriever.BuildContext` 中加入"项目指令文件注入"：
1. 启动时（`main.go` 初始化 retriever 处）扫描仓库根，按优先级读取：`CLAUDE.md` > `AGENTS.md` > `.cursorrules` 等
2. 读取结果缓存到 Retriever，作为 `BuildContext` 的**第一段上下文**（在 L3 记忆之前）
3. 不存在的文件静默跳过；文件过大时截断（复用 `TruncateResult`，`internal/tool/tool.go:237`）

**涉及文件**：
- `internal/memory/retriever.go` — 新增 `LoadProjectInstructions(path)` + 在 `BuildContext` 前置
- `main.go` — 初始化时调用 `LoadProjectInstructions`
- `internal/prompt/prompt.go` — 无需改（指令内容作为 user context 注入）

**验收标准**：
- 仓库有 `CLAUDE.md` 时，agent 的回复能引用其中的约定（如"项目用中文注释"）
- 无指令文件时行为与现状完全一致（向后兼容）

---

### ③ git 工具增强（diff --stat 概览）

**现状**：`doDiff`（`internal/tool/git.go:283`）只输出完整 unified diff。大改动（如批量重构）很容易撞 `ResultLimit`（12000 字符）被 `TruncateResult` 截断，LLM 看不到全貌。

**问题**：
- 大 diff 被截断后，LLM 无法判断改动范围
- Coding Agent 审阅大改动的正确姿势是：先看 `--stat` 概览 → 再钻取单个文件

**方案**：`doDiff` 支持 `stat` 参数（`Parameters` 加布尔字段）：
- `stat: true` → `git diff --stat`，输出文件级变更统计（`X files changed, +N/-N`）
- 默认行为不变（完整 diff）

**涉及文件**：
- `internal/tool/git.go` — `Parameters()` 加 `stat` 字段、`Execute` 解析、`doDiff` 分支
- `internal/tool/git_test.go` — 加 `TestGitDiff_Stat`

**验收标准**：
- `{"action":"diff","stat":true}` 输出文件级统计，含 `files changed` 行
- 现有 `TestGitDiff` 不受影响

---

## 批次 P1 — 可靠性与可配置性

### ④ LLM 调用重试

**现状**：`internal/llm/openai.go` 的 `Chat`（:77）和 `ChatWithToolsStream`（:250）对 API 错误零重试。网络抖动、429 限流、5xx 直接导致整轮失败。用户已实测撞到过 401（key 无效直接崩）。

**问题**：
- 单次 API 错误 = 整轮查询失败，即使错误是瞬时的
- 无退避策略，放大了限流问题

**方案**：在 `OpenAIClient` 加一个重试封装：
1. 对**可重试错误**（429、5xx、网络错误、超时）重试，指数退避 + 抖动（如 500ms × 2^n）
2. 对**不可重试错误**（401 认证失败、400 参数错误、403）直接返回，不重试
3. `ChatWithToolsStream` 是流式接口，重试逻辑放在创建 stream 之前（`openai.go:291` 的 `CreateChatCompletionStream` 调用处）

**涉及文件**：
- `internal/llm/openai.go` — 新增 `isRetryableError(err)` + `retryWithBackoff()` 辅助
- 可选：环境变量 `OPENAI_MAX_RETRIES`（默认 3）

**验收标准**：
- 模拟 429/5xx（测试中注入）→ 自动重试后成功
- 401 → 立即失败，不重试
- 现有测试全绿

---

### ⑤ 配置化（config 文件）

**现状**：散落的硬编码常量：
- `temperature 0.2` — `internal/llm/openai.go:84,181,282`
- `maxIterations 10` — `internal/agent/runner.go:17`
- `defaultResultLimit 8000` — `internal/agent/query_loop.go:28`
- `compressThreshold 0.8` — `internal/agent/query_loop.go:25`
- `inferContextLimit` 靠猜模型名 — `internal/llm/openai.go:420`
- `ResultLimit` 各工具内联 — `internal/tool/*.go`

**问题**：调参必须改代码重编译，且上下文窗口靠"模型名匹配"，新增模型家族要改代码。

**方案**：引入轻量配置层，**不改架构**（CLAUDE.md 明确"无 Makefile 无 CI"，保持极简）：
1. `main.go` 增加 `-config <path>` flag，读取 JSON 配置
2. 配置项：`temperature`、`max_iterations`、`result_limit`、`compress_threshold`、`context_limit`（覆盖模型推断）
3. 环境变量/flag 优先级 > config 文件 > 代码默认值
4. **不做**：热加载、多 profile、嵌套复杂配置（YAGNI）

**涉及文件**：
- 新增 `internal/config/config.go` — `Load(path) (*Config, error)` + `Default()`
- `main.go` — flag + 加载 + 传入各组件
- `internal/llm/openai.go` — `temperature` 参数化
- `internal/agent/` — `maxIterations`、`resultLimit`、`compressThreshold` 参数化

**验收标准**：
- 无 config 文件时行为与现状完全一致
- 提供 config 时所有参数生效（单测验证每个字段）
- `OPENAI_CONTEXT_LIMIT` 已存在，保持向后兼容

---

## 批次 P2 — 体验增强

### ⑥ 跨会话记忆（用户/项目画像）

**现状**：三层记忆（L1 history / L2 summary / L3 memory）全部存放在会话子目录 `data/sessions/<id>/`（`main.go:109-132`）。切换会话后，L3 结构化记忆（"用户偏好"、"项目约定"）完全丢失。`Retriever.BuildContext` 只读当前会话的 store。

**问题**：
- "我是谁、项目偏好是什么"这类跨会话知识没有载体
- 三层记忆听上去强，实际是三层"会话内"记忆

**方案**（最小可行版）：
1. 新增全局记忆目录 `data/global-memory/`（独立于会话）
2. `Extractor` 提取 L3 记忆时，同时把"项目级"条目（如项目约定）写入全局记忆（需要给 `Extractor` 加个分类提示，`internal/memory/extractor.go`）
3. `Retriever.BuildContext` 在构建上下文时合并全局记忆 + 当前会话记忆
4. **不做**：跨会话的用户画像学习、全局记忆编辑 UI（后续迭代）

**涉及文件**：
- 新增 `internal/memory/global_store.go` — 复用 `MemoryStore` 逻辑，指向全局目录
- `internal/memory/extractor.go` — 分类提示（"项目级 vs 会话级"）
- `internal/memory/retriever.go` — 合并全局 + 会话记忆
- `main.go` — 初始化全局 store

**验收标准**：
- 会话 A 建立的"项目级"记忆，在会话 B 可被检索到
- 会话级记忆（对话细节）不跨会话泄漏
- 无全局记忆时行为与现状一致

---

### ⑦ 工具循环内中断（Esc 回退）

**现状**：`Runner.Run` 的 select 循环（`internal/agent/runner.go:171`）只支持 `/stop` 全停。工具执行走偏时（如 LLM 在错误目录反复 ls），用户只能：
- 等它跑完 10 步撞 `generateFinalSummary`（`query_loop.go:707`）
- `/stop` 全停，重来

**问题**：没有"中断当前工具、回退一步、改参数重试"的精细控制，LLM 走错路时体验差。

**方案**（成本高，建议作为独立迭代）：
1. `queryLoopContext` 增加 `interrupt` channel（`query_loop.go:35`）
2. `Runner` 的 inputForward 通道增加命令协议：`/interrupt`（中断当前工具执行并回退）、`/retry <新参数>`（重新发起上一步工具调用）
3. UI 层（`internal/ui/bubble/`）绑定 Esc 键 → `/interrupt`

**涉及文件**：
- `internal/agent/query_loop.go` — interrupt channel + 回退逻辑
- `internal/agent/runner.go` — 输入协议扩展
- `internal/ui/bubble/bubble.go` — Esc 键绑定
- `internal/ui/ui.go` — 接口扩展（如需要）

**验收标准**：
- 工具执行中按 Esc → 当前工具立即中断，回到上一步
- `/retry` 可带修改后的参数重新执行
- `/stop` 行为不受影响

---

## 推荐执行顺序

```
批次 P0（本周）：
  ③ git diff --stat   → 0.5 天（含测试）
  ① CLI one-shot      → 1 天（含测试）
  ② 项目上下文注入     → 1 天（含测试）

批次 P1（下周）：
  ④ LLM 重试          → 1 天
  ⑤ 配置化            → 1.5 天

批次 P2（按需）：
  ⑥ 跨会话记忆         → 2 天
  ⑦ 循环内中断         → 3 天
```

每项按既有流程执行：brainstorming（确认需求）→ spec（`docs/superpowers/specs/`）→ plan（`docs/superpowers/plans/`）→ subagent-driven 实现 → 双审查。

---

## 附录：与既有能力的关系

| 既有能力 | 本次改进 |
|----------|----------|
| TextUI（`internal/ui/text/`） | ① 激活它，从"死代码"变"入口" |
| Retriever（`internal/memory/retriever.go`） | ②⑥ 扩展上下文来源 |
| Tool 接口的 ResultLimit | ③ 避免大 diff 截断盲区 |
| go-openai 的 `client` 字段 | ④ 在创建 stream 前加重试 |
| 硬编码常量（runner.go:17 等） | ⑤ 参数化 |
| 会话目录结构（`data/sessions/`） | ⑥ 增加全局记忆目录 |
| `/stop` 取消机制 | ⑦ 升级为细粒度中断 |
