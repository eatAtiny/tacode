# agentic-1 全项目清理与模块化重构实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在不改变功能的前提下清理全项目死代码（~450 行）、修正 30+ 处误导注释、对 3 个最痛的文件做保守拆分（query_loop / runner / main），随后将审查实证的 6+1 个行为 bug 以独立 fix commit 修复。

**Architecture:** 全部拆分均在同包内进行（不改变任何导出接口）。L2 摘要层按用户决策**原样保留、仅修正注释**。每个模块一个 commit（中文 commit 信息，conventional 前缀），每个 commit 前 `go build + go vet + go test ./...` 全绿 + gofmt 零新增漂移。

**Tech Stack:** Go 1.26.4；验证命令统一为 `go build ./... && go vet ./... && go test ./...` + `gofmt -l .`（口径：**零新增漂移**——基线本就存在约 19 个既有注释风格偏差文件，Task 10 只统一 tool 包的 6 个，其余不强改）。

**预留面政策（与用户 L2 决策同口径）:** 已文档化的用户面旋钮（env/config/命令/导出方法）即使当前无生效路径也**不删除**，仅修注释如实标注"预留"——适用：L2 全链、`OPENAI_CONTEXT_LIMIT`+`config.ContextLimit`+`llm.ContextLimit()`+`inferContextLimit` 整链（其值自 T1 起无消费方）、`ui.ReadInput()`、`Sandbox.AllowsNetwork()`、tool 别名机制。

**基线:** 分支 `refactor/module-cleanup`（自 master 3904a0a 切出），全测试绿。所有死代码结论均经审查 agent grep 全仓验证（含测试文件）。

**执行状态（2026-08-28 更新）:** Task 1-15 已完成并逐任务通过 spec+质量双重审查（每个实现 commit 后附独立审查：字节级移动比对 / helper 逐行等价论证 / TDD 先红后绿 / T15 含攻防测试）。累计 22 个 commit（含 4 个审查返工打磨 commit）。剩余：Task 16-19（fix ④⑤⑥⑦）+ Task 20（文档同步与最终整体审查）。执行期新发现的 7 条已知问题见文末「T20 需一并记录的已知问题」。

**用户决策（已确认）:**
1. L2 层：保持现状，只修注释 —— `summary_store.go`、`BuildContext`、`CheckAndCompress`、`SetCompressThreshold`、config 的 `compress_threshold` 字段、`/compress` 命令、extractor 的 `Summary` 字段**全部保留**，仅把注释改为如实描述（"预留/当前无生产写入路径"）。
2. 行为 bug：全部修复，每个独立 fix commit。
3. 拆分粒度：保守 —— 仅拆 `query_loop.go → tool_exec.go`、`runner.go → repl.go + oneshot.go`、`main.go → bootstrap.go` 三处；其余文件靠删死代码 + 修注释改善。

---

## Task 1: agent 包 — query_loop 死代码删除与注释修正

**Files:**
- Modify: `internal/agent/query_loop.go`
- Modify: `internal/agent/query_loop_test.go`（若引用被删符号）
- Modify: `internal/agent/compress_test.go`
- Modify: `internal/agent/final_summary_test.go`
- Modify: `internal/agent/types.go`
- Modify: `internal/agent/query_engine.go`

- [x] **Step 1: 删除 query_loop.go 988-1138 死代码区**

删除三个函数（生产与测试链路均不经过，仅 compress_test.go 引用后两个）：`estimateMessagesTokens`、`compressMessages`、`compressMessagesWithPersistence`。同步删除 `compress_test.go` 中仅测试它们的测试函数（保留测试 `compactHistory`/`Prepare`/`reactiveCompact` 等活逻辑的用例）。

- [x] **Step 2: 删除死字段链（先复核再删）**

逐项复核后删除（复核方法：grep 该字段全仓读写点）：
- `queryLoopContext.currentIter`（只写 195 行，无读取）
- `queryLoopContext.msgTokens`（生产只写；读取仅 final_summary_test.go —— 同步删测试中的读取）
- `queryLoopContext.contextLimit` 字段 + `queryLoop()` 的 `contextLimit` 参数 + query_engine.go:117 对应传参
- `queryLoopContext.compressThreshold` 字段 + `queryLoopOptions.CompressThreshold` + `defaultCompressThreshold` 常量 + query_engine.go:119 传参
- `Runner.compressThreshold()`（runner.go:201，删后 config 的该字段只剩 main.go:213 的 retriever 注入——按决策保留那条线）

注意：删 `queryLoop` 参数会改签名，检查所有调用点（query_engine.go 一处 + 测试）。

- [x] **Step 3: 删除 types.go 死字段**

`QueryEvent.PermissionRequired`（唯一写入 query_loop.go:782，全仓无读取）—— 删字段 + 删填充行，顺带修 123 行分组注释。

- [x] **Step 4: query_engine.go 微清理**

删 `finalIteration` 三行（139 声明 / 198 赋值 / 235 丢弃）；删 172 行 `ToolCallID: ""` 零值冗余；213-215 行 `go func() { r.queryBalanceWith(r.queryBalance) }()` 简化为 `go r.queryBalanceWith(r.queryBalance)`。

- [x] **Step 5: 修正 query_loop.go 过时注释**

- 83-84 行流程图：`checkAndCompressContext()` → `prepareIfNeeded()`；"token 超过 80% 阈值" → "字符用量超过 contextCharLimit（默认 50K）时压缩"
- 184 行 runLoop 步骤 2 同口径修正
- 261-266 行：删除拼接残留的旧函数注释段（"checkAndCompressContext 检查 token 用量…保留最近 2 轮…"整段），保留 266 行起 prepareIfNeeded 的真实描述
- 397 行结构体字段说明移到字段声明处（当前错挂在 `accumulateTokens` 方法上方）
- 438 行 "三者同时满足" → "以下条件同时满足"（实际列了 4 条）
- 419 行 "按原始顺序" → "并发批内按原始顺序 yield（并发批整体先于串行批）"
- 105 行 `contextLimit` 参数注释随 Step 2 一并消失

- [x] **Step 6: 验证**

Run: `go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: 全部 ok，gofmt 无输出

- [x] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor(agent): 清理 query_loop 死代码与过时注释

- 删除遗留压缩管线 estimateMessagesTokens/compressMessages/
  compressMessagesWithPersistence（生产链路经 Compactor，不经过此处）
- 删除只写不读字段 currentIter/msgTokens/contextLimit/compressThreshold
  及 queryLoopOptions.CompressThreshold 死链
- 删除 QueryEvent.PermissionRequired 死字段
- query_engine 删 finalIteration 占位变量等三处微清理
- 修正调用链注释：checkAndCompressContext→prepareIfNeeded，token 80%→字符口径"
```

---

## Task 2: agent 包 — query_loop 拆出 tool_exec.go（纯移动）

**Files:**
- Create: `internal/agent/tool_exec.go`
- Modify: `internal/agent/query_loop.go`

- [x] **Step 1: 创建 tool_exec.go，逐字迁移以下函数（不改一行实现）**

文件头注释：`// tool_exec.go 工具调用执行子系统：并发/串行分类、权限确认协议、中断注入、read-before-edit 状态维护。`

迁移清单（函数名@原行号）：`executeToolCalls`@425、`peekInterrupt`@493、`injectInterruptNotice`@511、`toolExecResult` 类型@528、`executeConcurrentTools`@545、`executeSingleTool`@659、`checkToolPermission`@761、`extractFilePath`@950、`recordFileRead`@970。

顺带（同文件、零风险）：`extractFilePath` 的 `toolName` 参数函数体内未用 —— 删参数，`recordFileRead` 调用点同步。

- [x] **Step 2: 更新两个文件的文件头职责注释**

query_loop.go 头注释注明"工具执行子系统见 tool_exec.go"；tool_exec.go 头注释如上。

- [x] **Step 3: 验证**（同 Task 1 Step 6）

- [x] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor(agent): query_loop 拆出 tool_exec.go（纯移动）

工具执行子系统（并发/串行执行、权限确认、中断注入、
read-before-edit 状态）独立成文件，query_loop.go 1138→约 700 行，
ReAct 主循环与工具执行的边界更清晰。实现零改动。"
```

---

## Task 3: agent 包 — runner 拆分 + 函数归位 + 小型去重

**Files:**
- Create: `internal/agent/repl.go`、`internal/agent/oneshot.go`
- Modify: `internal/agent/runner.go`、`internal/agent/session.go`、`internal/agent/memory.go`、`internal/agent/balance.go`、`internal/agent/query_loop.go`

- [x] **Step 1: 创建 repl.go，迁移**：`Run`@230、`handleInput`@385、`printPrompt`@173、`isChatUI`@169。文件头注释：`// repl.go REPL 主循环：select 模型 + 输入排队/转发协议。`

- [x] **Step 2: 创建 oneshot.go，迁移**：`RunOnce`@522。文件头注释：`// oneshot.go headless 单次查询执行入口。`

- [x] **Step 3: 函数归位（逐字移动）**：`handleListCommand`@592、`initTempSession`@485 → `session.go`；`singleCallSignature`@633 → `query_loop.go`；删除死代码 `trimArgs`@620（全仓零调用，已验证）。

- [x] **Step 4: 三处去重 helper（行为不变）**

- `setStorePaths(dir string)`：封装 `history/summary/memStore/events SetPath` 四连 + `syncCompactorPaths`，替换 4 处调用（initTempSession、switchSession、/new 临时分支、ensurePersisted）
- `switchToPersisted(id string)`：封装 `/list` 与 `/switch` 共有的 `Switch→isTemporary=false→switchSession→FindMeta→displayName→OnMessage→printSessionHistory` 序列（runner.go:603-615 与 session.go:216-228）
- `moveFile(tempDir, realDir, name, warnPrefix)`：封装 ensurePersisted 四个 `Stat→MkdirAll→Rename` 块，**保持各块现有错误处理语义不变**（前两块完备、后两块忽略——不改）
- displayName 计算（`meta != nil ? meta.Name : id`）随 switchToPersisted 自然收编；session.go:195-199（/new 持久化分支）如仍重复则提 `displayNameOf(meta, id)` 小函数

- [x] **Step 5: 微简化**：runner.go `Run()` 238 行的类型断言改为复用 `r.chatUI`（NewRunner 已缓存）；balance.go `balanceBaseURL`@90 内联删除（唯一调用点 L59）；memory.go `listMemories` 消除重复读盘（`listStore` 返回条目数，去掉 178 行第二次 `ListEntries()`）。T1 质量审查移交三项：① query_loop.go 顶部流程图把 prepareIfNeeded 从「步骤 4e」挪到循环体第一步的位置（实际在 4a 之前执行，与 runLoop doc 一致）；② query_loop.go 约 242 行「与旧 checkAndCompressContext 的差异」处加「（已删除）」标记；③ final_summary_test.go 的 `TestStreamRequest_IncludesUsage` 末尾补一行响应侧断言（如 `lc.lastInputTokens != 0`，恢复 usage 落地的响应侧覆盖）。

- [x] **Step 6: 修正注释**（runner/session/memory/balance）：

- runner.go 33-57 调用链注释整体重写为现行链路：`BuildContextFallback()（仅首轮/切换）→ compactor.Prepare() → prompt.BuildReActSystemPrompt/BuildUserTask → queryLoop()（内联 ReAct 循环）`；删除对 `BuildContext`/`CheckAndCompress`/`BuildReActPrompt`/`runLoop` 四个不存在符号的引用
- runner.go 50/57/226、memory.go 52、main.go 176 的「L2 摘要」表述统一改为：「L2 摘要仅在压缩时生成（当前未持久化到 summaries.jsonl），对话细节由跨轮累积 messages + EventStore 承载」
- memory.go 30-31 补一句：「`Summary` 字段当前无消费者（预留），仅 `Memories` 被处理」
- runner.go 169-172：「全 tea 界面 / alt screen」→「inline 聊天界面；写 os.Stdout 会打乱活区渲染（非 alt screen 原因）」
- session.go 277-278 `/balance`「成功后开启每轮展示」→「每轮结束自动查询余额（无条件），此命令用于立即刷新/排查」
- session.go 139/290、main.go 221 「AGENTS.md」→「项目指令（CLAUDE.md/AGENTS.md/AGENTS/.cursorrules，CLAUDE.md 优先）」
- session.go 文件头「会话管理函数」补注：`handleSessionCommand` 实为全量斜杠命令路由器
- permission.go 38 行 ForbiddenTools 注释改为：「预留扩展点：当前无任何代码向此切片追加，禁用检查恒为 false」
- runner.go 414 与 query_loop.go 45 对 inputForward 的矛盾注释：统一为「权限确认期间转发输入；/interrupt 修复后亦转发控制命令（见后续 fix commit）」——本 commit 先如实描述现状（权限确认转发），fix commit ② 再改

- [x] **Step 7: 验证**（同 Task 1 Step 6；重点确认 runner_input_test/session_name_test/interrupt_test 全绿）

- [x] **Step 8: Commit**

```bash
git add -A
git commit -m "refactor(agent): runner 拆分 repl/oneshot，会话函数归位，去重

- runner.go 拆出 repl.go（主循环+输入分发）与 oneshot.go（RunOnce），
  结构体/构造器/Setter 留在 runner.go
- handleListCommand/initTempSession 归位 session.go，singleCallSignature
  归位 query_loop.go，删除死代码 trimArgs
- 提取 setStorePaths/switchToPersisted/moveFile 消除 4+2+4 处重复
- 修正调用链注释（4 个不存在的函数引用）与 L2/alt-screen 等误导表述"
```

---

## Task 4: agent 包 — compactor 微简化与注释修正

**Files:**
- Modify: `internal/agent/compactor.go`

不拆分（用户选保守；高内聚可接受）。

- [x] **Step 1: 合并双胞胎方法**：删私有 `estimateMessagesChars`@146，5 个调用点（115/118/121/448/491）直接调 `estimateChars` 或统一走公开 `EstimateMessagesChars`；修正 145 行「带 system 保护」虚假注释（函数体无任何保护）。

- [x] **Step 2: 提取两个重复扫描 helper（行为不变）**：
- `lastToolBatchAssistant(messages) int`：toolResultBudget 165-171 与 trailingUnseenToolIDs 409-415 共用
- `pullBackToPairStart(messages, tailStart, lowerBound) int`：snipCompact 316-331 与 reactiveCompact 569-585 共用（两处仅下界不同：headEnd vs 1）。**配对保护语义逐字保留**（API 400 敏感区，compactor_test 有锚定）

- [x] **Step 3: 正则提为包级 var**：`saveOutput`@236 与 `isArchiveMarker`@392 内的 `regexp.MustCompile` 移到包级（每次调用都编译）。

- [x] **Step 4: 注释修正**：头注释 21/30 行「四步压缩管线」列出 5 步 → 改为「五步压缩管线（4 步无损 + 1 步 LLM 摘要）」如实计数。

- [x] **Step 5: 验证**（同 Task 1 Step 6；compactor_test 的 PairingProtection/KeepsLast5 必须全绿）

- [x] **Step 6: Commit**

```bash
git add internal/agent/compactor.go
git commit -m "refactor(agent): compactor 去重与注释修正

- 合并重复的字符估算包装，修正虚假的 system 保护注释
- 提取 lastToolBatchAssistant/pullBackToPairStart 消除两处逐句重复
  （配对保护语义不变，API 400 敏感区）
- 正则提为包级 var，避免每次调用重编译
- 头注释四步→五步如实计数"
```

---

## Task 5: llm 包 — openai 死代码与简化

**Files:**
- Modify: `internal/llm/openai.go`、`internal/llm/retry.go`

- [x] **Step 1: 删除死代码**：`ChatWithTools`@180-243（全仓零调用，连带消除与 ChatWithToolsStream 的 20 行消息转换重复）、`ChatResponse` 类型@144（仅被它使用）、`StreamEventTool` 常量@155（零引用；若为 iota 连续定义，确认删除不破坏其余常量值——事件常量仅进程内使用，无序列化依赖，重编号安全）、`Temperature()`@452（零调用）。**保留** `ContextLimit()`/`contextLimit` 字段/`inferContextLimit` 整链（预留面政策），但 48 行字段注释「用于压缩判断」改为「预留：当前无消费方（压缩判断已改用 compactor 字符口径）」。

- [x] **Step 2: inferContextLimit 冗余分支合并（行为不变，context_limit_test 锚定）**：删 `gpt-4o-mini`（被 gpt-4o 同值覆盖）、`gpt-3.5-turbo-16k`（被 gpt-3.5-turbo 同值覆盖）、`mimo-v2-omni/flash`（被 mimo 兜底覆盖）、三个 claude 具体分支（被 claude 兜底同值覆盖）、`mimo-v2.5-pro`（被 mimo-v2.5 前缀包含）；保留 `mimo-v2-pro`（无包含关系）。跑 `go test ./internal/llm/ -run ContextLimit -v` 确认逐值不变。

- [x] **Step 3: NewOpenAIClientFromEnv 双重 TrimRight 合一**（65 行与 69-72 行对同一串做相同 Trim；一次 Trim 一个变量）。

- [x] **Step 4: 注释修正**：openai.go 176 行「已较少使用」随删除消失；17 行 defaultTemperature 注释改「默认采样温度（可被 config 覆盖）」；37-39 行头注释调用方补 compactor（compactHistory 也调 Chat，共三处）；retry.go 28 行 `// ±20%` → `// 单向抖动 [0, +20%)`（与函数级注释一致）。

- [x] **Step 5: 验证**（同 Task 1 Step 6）

- [x] **Step 6: Commit**

```bash
git add internal/llm/
git commit -m "refactor(llm): 删除 ChatWithTools 等死代码，简化上下文窗口推断

- 删 ChatWithTools/ChatResponse/StreamEventTool/Temperature()（全仓零调用），
  连带消除与流式路径重复的消息转换
- inferContextLimit 合并 5 处被兜底分支覆盖的冗余匹配（逐值不变）
- NewOpenAIClientFromEnv 双重 TrimRight 合一
- 修正 retry 抖动注释（单向 [0,+20%)，非 ±20%）"
```

---

## Task 6: prompt 包 — 死函数删除与包注释重写

**Files:**
- Modify: `internal/prompt/prompt.go`

- [x] **Step 1: 删除**：`RoundPrompt` 类型@17、`BuildRoundPrompt`@23、`BuildReActUserPrompt`@122（均零调用，连带消除两者间的模板重复）。

- [x] **Step 2: 重写包注释（5-7 行）**为现行组装链：

```go
// prompt 包构建 ReAct 提示词。
//
// 现行消息组装（见 agent.queryEngine）：
//   messages[0]     = BuildReActSystemPrompt（静态 system，含工具指南）
//   messages[1]     = BuildSystemReminder（记忆 preamble，仅首轮/切换注入）
//   messages[末尾]  = BuildUserTask（每轮变化的用户任务）
```

- [x] **Step 3: 验证**（同 Task 1 Step 6）+ **Commit**

```bash
git add internal/prompt/
git commit -m "refactor(prompt): 删除未使用的 BuildRoundPrompt/BuildReActUserPrompt，重写包注释

包注释原描述已废弃的两消息调用链，改为现行
system + system-reminder + user-task 三段组装。"
```

---

## Task 7: memory 包 — 死方法删除与注释修正（L2 按决策保留）

**Files:**
- Modify: `internal/memory/event.go`、`internal/memory/history.go`、`internal/memory/store.go`、`internal/memory/extractor.go`、`internal/memory/retriever.go`、`main.go`（构造器签名联动）

- [x] **Step 1: 删除非 L2 死方法**（已 grep 验证零引用，含测试）：`EventStore.ReadRound`@118、`RecentEvents`@133、`Count`@145；`HistoryStore.ReadHistory`@57。**保留**：`BuildContext`、`CheckAndCompress`、`SetCompressThreshold`、summary_store 全部、两个 `Digest`、`trimText`、`FormatRecent`、`LoadRecent`（L2 决策：原样保留）。

- [x] **Step 2: 构造器去 err**：`NewMemoryStore`/`NewHistoryStore`/`NewSummaryStore` 实现永不返回 error —— 删 error 返回值；main.go 与 migration 内调用点、相关测试同步。

- [x] **Step 3: 注释修正**：
- retriever.go 24-33 头注释调用链改为如实：「生产路径仅使用 BuildContextFallback（queryEngine 首轮/切换调用）；BuildContext/CheckAndCompress 为预留路径，当前无生产调用」；124 行「核心方法」、271 行「QueryEngine 步骤 2」两处死方法自称同步改「预留」
- retriever.go 46 行 `memory *MemoryStore` 注释：「会话级 L3 store；feedback 类已路由到全局 store，此 store 仅为 globalMem 为 nil 时的回退目标」
- store.go 258 行拼写 `frontatter`→`frontmatter`；260-261 行「从文件名推断 name（调用方应设置）」改为如实：「frontmatter 缺 name 时 entry.Name 为空，SaveEntry 会写出 `.md` 空名文件——手工编辑旧文件时的已知隐患」
- extractor.go 21 行调用链注释 Summary 分支标注「（预留，当前无消费者）」；50 行 doc 同步
- event.go 153 行 Digest 注释加「（预留路径，仅 BuildContext 使用）」
- retriever.go 358 行 EstimateTokens doc 中「（见 estimateMessagesTokens）」悬空引用（Task 1 已删该函数）——删去该括号注或改为指向 compactor.estimateChars

- [x] **Step 4: 验证**（同 Task 1 Step 6；migration_test/retriever_test 全绿）

- [x] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(memory): 删除零引用查询方法，构造器去恒空 error，修正注释

- 删 EventStore.ReadRound/RecentEvents/Count、HistoryStore.ReadHistory
  （grep 验证全仓零引用）
- 三个 store 构造器不再返回恒为 nil 的 error
- L2 相关代码按决策原样保留，注释改为如实标注预留状态
- 修正 store.go 误导注释（frontmatter 缺 name 的空名文件隐患）"
```

---

## Task 8: session 包 — 死 helper 删除

**Files:**
- Modify: `internal/session/session.go`、`internal/session/picker.go`

- [x] **Step 1: 删除零引用方法**：`ActivePath`@199、`TempPath`@209、`MemoryDir`@189、`ActiveMemoryDir`@194、`generateID` 别名@306（包内 5 处调用改用 `GenerateID`，删 305 行「保持兼容」注释——私有名无兼容问题）。

- [x] **Step 2: picker.go 样式去重**：`pickerActiveStyle`@17 与 `pickerActiveCurrentStyle`@34 逐字相同 —— 合并为 `pickerActiveStyle` 一份，View 145-150 两处引用同步。渲染结果不变。**不动 IsDone/Chosen 协议**（cf998b2 修复区）。

- [x] **Step 3: 验证**（同 Task 1 Step 6）+ **Commit**

```bash
git add internal/session/
git commit -m "refactor(session): 删除零引用路径 helper 与 generateID 私有别名，合并重复样式"
```

---

## Task 9: ui 包 — 死消息删除、口径正名、注释重写

**Files:**
- Modify: `internal/ui/ui.go`、`internal/ui/bubble/chat.go`、`internal/ui/bubble/bubble.go`、`internal/ui/bubble/box.go`、`internal/ui/bubble/welcome.go`、`internal/ui/text/text.go`

- [x] **Step 1: 删除死代码**：`chatPickerResultMsg` 类型@95 + Update case 分支@322-325（全仓无发送点；323 行「保留兜底」注释一并删）。

- [x] **Step 2: token→chars 口径正名（包内私有，渲染文本不变）**：`ChatModel.contextUsedTokens`→`contextUsedChars`（135 行注释改「上下文已用字符数」）、`chatContextMsg.usedTokens`→`usedChars`、`formatToken`→`formatChars`（460 行注释同步）。测试引用点同步。

- [x] **Step 3: 微简化**：`chatHistoryEvent` 结构（仅一个 text 字段）→ 直接用 `[]string`（4 构造点 + 测试同步）；text.go 提取 `emit(event, data)` 消除 16 处 `if t.OnEvent != nil` 样板；welcome.go 提取 `infoLine(emoji, value)`；box.go `finalAnswerText` 两分支缩进循环提 `indentLines(s)`、`toolCallBox/toolResultBox` 骨架提 `drawBox(titleStyle, title, bodyLines, width)`（**结尾带 \n 契约与 15 行截断保持**）、删 107-109 行 nil 接收者防御（唯一调用点传非 nil 方法接收者）。**绝不把一次 case 拆成多次 commit（Println 保序边界）**。

- [x] **Step 4: 重写 ui.go 接口文档**：57 行 OnThink、64-66 行 OnDelta 的「打印/ANSI 光标控制」描述改为现行机制（「BubbleUI：投递消息更新活区状态行 / 增量进 streamBuf，遇换行经 commit→tea.Println 定稿」）；103-104 行删「分隔线」虚构描述；138-141 行 ShowHistory 的「TextUI 转文本行」改「TextUI 转发 OnEvent("history", events)」；44 行 ReadInputChan 契约改为如实的两实现行为（BubbleUI 不关闭；TextUI 立即关闭）；8-12 行分组列举补 UpdateContext/RunSessionPicker/ShowHistory。

- [x] **Step 5: 其余注释修正**：bubble.go 21-24 文件组织补 welcome.go、45-46「持有 footer 组件」→「footer 由 renderFooter 即时渲染」；text.go 44-54 OnEvent 事件清单补 context/session_picker/history、139 行 RunSessionPicker 注释补「生产 one-shot 路径不可达，仅编排完整性保留」。

- [x] **Step 6: 验证**（同 Task 1 Step 6；bubble 包 42 用例 + statusbar/session_history 测试全绿）+ **Commit**

```bash
git add internal/ui/
git commit -m "refactor(ui): 删除死消息，token→chars 口径正名，重写接口文档

- 删 chatPickerResultMsg（全仓无发送点的确定性死代码）
- contextUsedTokens 等三个名实不符的 token 命名改为 chars（实存字符数）
- ui.go 接口文档中旧版 ANSI 追加式实现的描述全部改为现行 tea 机制
- chatHistoryEvent 收敛为 []string，text.go 提取 emit，box.go 提取
  drawBox/indentLines 消除骨架重复"
```

---

## Task 10: tool + sandbox + mcp 包 — 死代码与共享 helper

**Files:**
- Modify: `internal/tool/tool.go`、`internal/tool/shell.go`、`internal/tool/git.go`、`internal/tool/list.go`、`internal/tool/webfetch.go`、`internal/tool/edit.go`、`internal/sandbox/sandbox.go`、`internal/sandbox/macos.go`、`internal/sandbox/sandbox_other.go`、`internal/mcp/client.go`

- [x] **Step 1: tool.go 拆内务**：`parseArgs`@29 产出统一前缀的完整错误（"解析参数失败: …"），7 个工具 Execute 首行的 `"parse args: %w"` 双重包装删除（错误文本变为单前缀，面向 LLM 的提示语义不变）；`Tool` 接口注释 12/52 行「6 个方法」→「10 个方法」。

- [x] **Step 2: 共享 helper**：shell.go 退出码提取（103-106）改用 git.go `exitCodeOf`；超时+CombinedOutput 包装提 `runCmd(ctx, name, args, timeout, displayArgs)`（git.go 158-167 与 shell.go 98-111 同构；git 超时错误消息改用 `t.timeout` 字段而非常量，且 displayArgs 不含内部 `-c` 注入参数——用户可见错误文本更干净）；30s 超时共享常量。

- [x] **Step 3: 死代码删除**：list.go `dirEntry.path` 只写不读字段 + 重复 Join；webfetch.go `truncate` 不可达分支@286-288；sandbox_other.go 的 `os/exec` 哑 import + 哑变量（2 行）、macos.go 的 `path/filepath` 哑 import + 哑变量（2 行）。

- [x] **Step 4: 注释修正**：git.go 19 行「status/log/branch 输出格式化」→「仅 status 格式化；log 靠 --format 参数，branch 原样返回」；edit.go 15/201 行删除「弯引号」虚假描述（全文件无弯引号处理，改为如实描述仅处理直引号）；shell.go 危险命令表删冗余的 `"rm -rf"`/`"rm -r"`（被 `"rm "` 前缀包含）+ 161-163 改用 `parseArgs`；list.go 123 行计数注释留待 fix ⑥；sandbox.go 19 行 `AllowsNetwork` 注释「供 shell 工具判权限」→「预留接口方法，当前生产无调用（shell 权限判定解析 args 的 network 字段）」；mcp client.go 删 `Manager.Tools()`@107-114 + `serverConn.tools`@42（零调用），`conns` 字段补「非并发安全（当前 main 顺序连接）」注释。

- [x] **Step 5: gofmt -w** 覆盖 6 个不合规文件（edit/file/grep/list/tool/tool_test）。

- [x] **Step 6: 验证**（同 Task 1 Step 6；tool 包 732 行测试全绿）+ **Commit**

```bash
git add internal/tool/ internal/sandbox/ internal/mcp/
git commit -m "refactor(tool/sandbox/mcp): 提取共享执行 helper，删除死代码与哑 import

- parseArgs 统一错误前缀，7 处调用点去双重包装
- git/shell 共享 exitCodeOf 与 runCmd（超时错误不再暴露内部 -c 参数）
- 删 list.go 死字段、webfetch 不可达分支、sandbox 两处哑 import、
  mcp Manager.Tools 死方法
- 修正弯引号虚假注释、rm 危险表冗余项、AllowsNetwork 预留说明"
```

---

## Task 11: main.go — bootstrap 抽取

**Files:**
- Create: `bootstrap.go`（package main）
- Modify: `main.go`

- [x] **Step 1: 抽取三个装配函数到 bootstrap.go**（纯移动 + 错误处理经 `fatal(msg, err)` helper 收敛 7 处 `Fprintf+Exit` 重复）：
- `buildMemoryStack(...)`：main.go 147-224（三层 store + 全局/项目记忆 + 迁移 + EventStore/Extractor/Retriever）
- `buildTools(...)`：main.go 226-301（内置工具 + 沙箱 + MCP 注册，返回 Registry + 清理函数）。**沙箱 nil 判空顺序及其注释（243-251）原样保留**——`sandbox.IsActive(nil)` 对 nil 接口返回 true 的陷阱注释必须跟随移动
- `buildUI(...)`：main.go 303-313

main() 只剩：flag 解析 → 配置 → 装配 → 模式分支。defer 链与 one-shot `return`（非 os.Exit）设计原样保留。`loadEnvFile`/`mcpServerFlags` 留在 main.go（保守决策）。

- [x] **Step 2: 注释修正**：63-80 步骤总览对齐现实（75 行删「L2 摘要」；208-209 行「每轮查询前构建上下文/80% 触发 L2 合并」→「仅首轮/切换注入 preamble；压缩由 Compactor 字符管线负责」；212 行「注入 config 的压缩阈值」→「注入 L2 压缩阈值（预留路径，当前无生产调用）」；176 行 L2 表述同 Task 3；221 行项目指令文件清单同 Task 3）。

- [x] **Step 3: 验证**（同 Task 1 Step 6 + `go build -o /tmp/agentic-smoke . && /tmp/agentic-smoke -h` 冒烟）+ **Commit**

```bash
git add main.go bootstrap.go
git commit -m "refactor(main): 依赖装配抽到 bootstrap.go，fatal helper 收敛错误退出

main() 从 266 行收敛为 flag→配置→装配→分支；buildTools 内保留
沙箱 nil 接口陷阱注释与 MCP name@command 解析。修正步骤注释中
已不存在的每轮 BuildContext/L2 自动压缩描述。"
```

---

## Task 12: config 包 — 注释修正（收尾重构）

**Files:**
- Modify: `internal/config/config.go`

- [x] **Step 1:** 100 行 `Apply` 注释反直觉句式改写：「c 中显式设置（非 nil）的字段覆盖 base 默认值」；14 行删「或 Config.Load 显式指定」（同一通道）；`Default()` 内 `ContextCharLimit` 为 nil 处补注释：「真实默认 50000 定义在 agent/compactor.go（双源，改动需同步）」。

- [x] **Step 2: 验证** + **Commit**

```bash
git add internal/config/
git commit -m "refactor(config): 修正 Apply 优先级注释与默认值双源标注"
```

---

## Task 13: fix ① — tool.go 遍历排序（前缀缓存）

**Files:**
- Modify: `internal/tool/tool.go`
- Test: `internal/tool/tool_test.go`

- [x] **Step 1: 写失败测试**：注册 8 个工具后连续调用 `Names()` 10 次，断言每次结果完全相等且为字典序。同法断言 `Descriptions()` 首行顺序与 `FunctionDefinitions()` 的 name 序。

- [x] **Step 2: 跑测试确认失败**（map 随机序，多次运行顺序不稳定）

- [x] **Step 3: 实现**：三个方法内收集到 slice 后 `sort.Strings`（FunctionDefinitions/Descriptions 按 name 排序后输出）。同步把 391-400 行 Descriptions 文档「按注册顺序」改为「按名称字典序（保证 system prompt 跨轮字节稳定，命中前缀缓存）」。

- [x] **Step 4: 全量验证** + **Commit**

```bash
git add internal/tool/
git commit -m "fix(tool): 工具清单遍历排序，恢复 system prompt 跨轮稳定性

Names/Descriptions/FunctionDefinitions 直接 range map 导致每轮
system prompt 中工具顺序随机（实测 8 工具 10 次遍历 5-7 种顺序），
击穿 messages[0] 全静态的前缀缓存设计。排序后跨轮字节稳定。"
```

---

## Task 14: fix ② — /interrupt 可达性

**Files:**
- Modify: `internal/agent/repl.go`（handleInput，Task 3 后的所在文件）
- Test: `internal/agent/runner_input_test.go`

- [x] **Step 1: 写失败测试**：模拟查询运行中（queryResultCh 非 nil、permWaiting=false）输入 `/interrupt`，断言 inputForward 收到该命令而非进入 pendingInputs。

- [x] **Step 2: 确认失败**（现状：排队）

- [x] **Step 3: 实现**：handleInput 查询运行分支增加——输入为 `/interrupt` 或 `/retry` 前缀时写入 inputForward（非阻塞 select+default 丢弃旧值，容量 1 已保证），其余输入维持排队。同步更新 repl.go 与 query_loop.go 45/175 行 inputForward 语义注释（两处统一为「权限确认输入 + 控制命令（/interrupt、/retry）转发通道」）。

- [x] **Step 4: 全量验证**（interrupt_test/runner_input_test 必绿）+ **Commit**

```bash
git add internal/agent/
git commit -m "fix(agent): /interrupt 在工具执行期间可达

原实现仅在权限确认等待时写 inputForward，/interrupt 被静默排队到
查询结束，queryLoop 的 peekInterrupt 收不到——CLAUDE.md 承诺的
mid-loop 中断实际不可达。现查询运行期间 /interrupt、/retry 直接
转发。"
```

---

## Task 15: fix ③ — shell 只读判定收紧

**Files:**
- Modify: `internal/tool/shell.go`
- Test: `internal/tool/tool_test.go`

- [x] **Step 1: 写失败测试**（表驱动，含实证用例）：`cat a > b 2>&1`、`grep foo bar > out 2>/dev/null`、`cat f | tee g` 断言 IsReadOnly=false；`ls`、`cat a 2>/dev/null`（收紧后为 false，一并断言）、`git status`、`echo hi | grep foo`（管道两侧白名单内，保持 true）按新语义断言。

- [x] **Step 2: 确认失败**（现状三个写命令误判 true）

- [x] **Step 3: 实现**：(a) 命令含 `>` 即返回 false（fail-closed，含 `2>` 重定向场景）；(b) 含 `|` 时按段拆分，每段首词必须在 `readOnlyCommands` 白名单内，否则 false。266 行 fail-closed 注释保持（现已成立）。

- [x] **Step 4: 全量验证** + **Commit**

```bash
git add internal/tool/shell.go internal/tool/tool_test.go
git commit -m "fix(tool): shell 只读判定堵住重定向与管道绕过

实证：cat a > b 2>&1 / grep foo bar > out 2>/dev/null / cat f | tee g
均被判 read-only（免确认+可并发）。收紧为：含 > 一律非只读；
管道逐段白名单校验。fail-closed 承诺自此成立。"
```

---

## Task 16: fix ④ — 并发路径补禁止列表与全局检查器

**Files:**
- Modify: `internal/agent/tool_exec.go`（executeToolCalls 分类处）
- Test: `internal/agent/query_loop_test.go`

- [ ] **Step 1: 写失败测试**：向 `ForbiddenTools` 追加一个自报 IsConcurrencySafe+IsReadOnly=true 的工具，断言其落入串行路径（现状：进并发）。

- [ ] **Step 2: 确认失败** → **Step 3: 实现**：并发分类条件补 `!isToolForbidden(t.Name())` 与 `globalPermissionChecker.CheckPermission(...).Allow`（与串行路径 661/763 行同一判定来源）。注意测试后清理全局切片。

- [ ] **Step 4: 全量验证** + **Commit**

```bash
git add internal/agent/
git commit -m "fix(agent): 并发工具分类补 ForbiddenTools 与全局权限检查

原分类只查工具自报的并发安全+只读+自身 CheckPermission 放行，
绕过全局禁止列表与全局检查器（当前 ForbiddenTools 恒空属潜伏缺口）。"
```

---

## Task 17: fix ⑤ — grep 长行扫描错误暴露

**Files:**
- Modify: `internal/tool/grep.go`
- Test: `internal/tool/tool_test.go`

- [ ] **Step 1: 写失败测试**：构造含 >64KB 单行且行内含匹配串的文件，断言输出包含匹配或明确的扫描错误提示（现状：静默漏配）。

- [ ] **Step 2: 确认失败** → **Step 3: 实现**：`searchFile` 循环后检查 `scanner.Err()`，非 nil 时向结果追加 `（该文件扫描中断: %v）` 提示行。

- [ ] **Step 4: 全量验证** + **Commit**

```bash
git add internal/tool/
git commit -m "fix(tool): grep 检查 scanner.Err，超长行不再静默漏配

bufio.Scanner 默认 64KB 行上限，超限文件扫描中断且无提示。
现中断时在输出中明示。"
```

---

## Task 18: fix ⑥ — list 条目计数如实

**Files:**
- Modify: `internal/tool/list.go`
- Test: `internal/tool/tool_test.go`

- [ ] **Step 1: 写失败测试**：构造 150 条目目录 + max_entries=50，断言输出明示截断（现状显示「共 100 个条目」失真——maxEntries*2 上限）。

- [ ] **Step 2: 确认失败** → **Step 3: 实现**：`collectEntries` 返回 `(entries, truncated bool)`（Walk 中因上限提前终止时置位）；输出截断时显示「共 %d 个条目（已达扫描上限，可能不完整）」，并给 `maxEntries*2` 双倍采集补动机注释（排序前留余量）。

- [ ] **Step 4: 全量验证** + **Commit**

```bash
git add internal/tool/
git commit -m "fix(tool): list 目录条目计数明示截断

原实现在大目录下只显示双倍采集上限的数字（如 5000 条目显示
共 100 个），现 truncated 时明示不完整。"
```

---

## Task 19: fix ⑦ — box 宽度显示口径统一（审查附加发现）

**Files:**
- Modify: `internal/ui/bubble/box.go`
- Test: `internal/ui/bubble/statusbar_test.go` 或新用例

- [ ] **Step 1: 写失败测试**：`toolResultBox("✅ 结果", ...)`（中文标题）断言标题行补线长度按显示宽计算（`lipgloss.Width`），框线右端对齐。

- [ ] **Step 2: 确认失败**（现状按字节数补线，中文标题多画约 3 个 `─`）

- [ ] **Step 3: 实现**：46/81 行 `len(name)`/`len(titleText)` 改 `lipgloss.Width(...)`（与 boxWidth 的 rune 口径统一）。

- [ ] **Step 4: 全量验证** + **Commit**

```bash
git add internal/ui/bubble/
git commit -m "fix(ui): 框线标题补线改用显示宽度，中文标题不再多画横线

字节宽（\"✅ 结果\"=10 字节）与显示宽（7）混用导致补线超长。"
```

---

## Task 20: 文档同步

**Files:**
- Modify: `CLAUDE.md`、`README.md`（若有相同过时描述）

- [ ] **Step 1: CLAUDE.md 更新**：
- File Map：agent 包补 `tool_exec.go`/`repl.go`/`oneshot.go`，main.go 补 `bootstrap.go`，文件数与实际一致（含此前未记录的 mcp/config/sandbox 包补全）
- 删除 permission.go 的 `DefaultPermissionChecker` 表述（代码中已不存在该类型）
- `/compress` 命令描述补「当前无 L2 写入路径，实际为空操作（L2 层预留）」
- config 的 `compress_threshold` 标注「预留，当前无生效路径」
- ReAct Flow 与 Memory 章节对齐「消息跨轮累积 + Compactor 字符管线 + preamble 仅首轮注入」的现行架构；`/interrupt` 描述对齐 fix ② 后行为
- 补记 `go vet`/`gofmt` 到验证命令

- [ ] **Step 2: 全量验证** + **Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: CLAUDE.md 同步重构后的文件地图与现行架构描述"
```

---

## 任务依赖与顺序

Task 1→2→3→4（agent 包内有序：先删后拆再归位）；Task 5-12 相互独立（可并行/任意序）；Task 13-19 依赖对应模块重构完成（fix ② 依赖 Task 3 的 repl.go、fix ④ 依赖 Task 2 的 tool_exec.go）；Task 20 收尾。

## 明确不做（记录在案）

- compactor.go / memory.go / retriever.go / store.go / chat.go / tool.go / session.go(internal) / openai.go 的文件拆分（用户选保守拆分）
- L2 层任何删除或接通（用户选保持现状）
- `ReadInput()` 接口方法、`Sandbox.AllowsNetwork()`、别名机制（接口预留面，保留；仅修注释）
- compactor `summaryInput` 字节/rune 口径统一、`microCompact` O(n²) 估算优化（收益边际，避免近似行为变化）
- `migrateOldFormat` 旧格式迁移（存量数据保守保留）
- 跨 Update Println 排序边界（已知边界，正解是注入同步 printer，超出本次范围）

## T20 需一并记录的已知问题（执行期审查发现）

1. shell 只读判定存量绕过（fix ③ 修复范围外，T15 审查攻防实证）：`;` / `&&` / 换行命令链（`echo hi; touch x` 判 true）、`$()`/反引号命令替换、`find -exec`/`-delete`、`command <cmd>` 内建、白名单命令写参数（`sort -o out`、`git branch -D`）。建议后续：命令链套用管道同款逐段校验 + 白名单收敛。
2. 危险命令表 `"rm "` 尾随空格匹配不到串尾裸 `rm`（`cat f | xargs rm` 不触发危险判定）。
3. /interrupt 在纯文本轮（无工具调用）静默蒸发——inputForward 里的控制命令随查询结束被丢弃，无用户反馈。
4. inputForward 连续两条控制命令后者被静默丢弃（容量 1 + default），无提示。
5. peekInterrupt 会吞掉 permWaiting 清除后迟到的权限应答（存量边界）。
6. `gpt-3.5-turbo` 实际 4K 窗口但推断返回 16K；claude 兜底把 claude-2 也判 200K（近似值，存量）。
7. T11 panic 路径理论差异：buildTools 内 panic 时沙箱临时 profile 不再被 unwind 清理（原 defer 在 MCP 循环前注册）。
