# Agentic 代码评审与修改优先级

> 版本：v2.1（2026-08-21）
> 范围：基于对当前代码库的完整阅读（`main.go`、`internal/agent/`、`internal/llm/`、`internal/memory/`、`internal/tool/`、`internal/sandbox/`），针对**当前状态**的新一轮评估
> 前置：`docs/plan/agentic-improvement-plan.md`（v1.0）中的 P0/P1 项（one-shot、项目上下文、git diff --stat、LLM 重试、sandbox、mcp、webfetch）已基本落地，本文不重复

---

## 1. 评审结论

骨架已经相当完整：两层查询架构清晰、三层记忆 + EventStore 真相源、工具并发执行、权限内聚到工具自身、read-before-edit、沙箱、MCP、one-shot 入口都已就位。`go vet ./...` 干净，有 12 个测试文件覆盖工具层和部分 agent 层。

剩余问题集中在三类：
1. **正确性 bug**：两处实现与注释/意图不符，会直接影响用户体验
2. **安全误判**：只读/危险命令的启发式判断有可绕过的漏洞
3. **可配置性 & 估算精度**：硬编码常量散落，token 估算过粗导致压缩判断不准

---

## 2. 修改优先级总表

| 级别 | 编号 | 问题 | 价值 | 成本 | 涉及文件 |
|------|------|------|------|------|----------|
| P0 | ① | `generateFinalSummary` 仍传 `lc.tools`，与注释意图不符 | 高 | 极低 | `internal/agent/query_loop.go` |
| P0 | ② | 项目指令只找 `AGENTS.md/AGENTS`，漏掉 `CLAUDE.md` | 高 | 极低 | `internal/memory/retriever.go` |
| P1 | ③ | 只读/危险命令靠前缀子串匹配，可被绕过 | 高 | 中 | `internal/tool/shell.go` |
| P1 | ④ | 配置化未做（temperature/maxIter/resultLimit 硬编码） | 中 | 中 | 多文件 |
| P1 | ⑤ | `inferContextLimit` 靠模型名字串匹配 | 中 | 低 | `internal/llm/openai.go` |
| P2 | ⑥ | in-loop 压缩是损毁式 stub，与 L2 摘要策略不一致 | 中 | 中 | `internal/agent/query_loop.go` |
| P2 | ⑦ | token 估算过粗（ASCII/4 + CJK/2） | 中 | 中 | `internal/memory/retriever.go` |
| P2 | ⑧ | 跨会话记忆 / 循环内中断（老计划⑥⑦）未做 | 中 | 高 | 多文件 |
| P2 | ⑨ | 核心路径测试覆盖不足（压缩、BuildContext、权限） | 中 | 中 | `*_test.go` |

---

## 3. P0 — 严重（正确性，立即修）

### ① `generateFinalSummary` 仍传入工具定义

**现状**（`internal/agent/query_loop.go` `generateFinalSummary`）：注释和注入的 user 消息都说"不再调用工具"，但调用仍传入 `lc.tools`：

```go
streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, lc.tools)
```

**问题**：LLM 可再次返回 `tool_calls`，而 `generateFinalSummary` 的事件循环只处理 `delta`/`done`/`error`，**忽略 done 事件里的 `toolCalls`**。若 LLM 选择调工具，`finalContent` 可能为空 → `yieldFinal("")` → 用户拿到**空答案**。

**方案**：调用时传 `nil` 作为 tools 参数，从根上禁止工具调用：

```go
streamChan := lc.llmClient.ChatWithToolsStream(lc.ctx, lc.messages, nil)
```

**验收**：
- 构造一个必然撞 maxIter 的场景（如让 LLM 反复调一个会失败的只读工具），最终答案非空
- 新增 `TestGenerateFinalSummary_NoTools`：断言传入 LLM 的请求不含 tools

**级别**：P0｜成本：极低（一行）｜风险：低

---

### ② 项目指令加载漏掉 `CLAUDE.md`

**现状**（`internal/memory/retriever.go` `findProjectInstructions`）：候选名单只有 `AGENTS.md`、`AGENTS`。

**问题**：本仓库根目录就有一份 10KB 的 `CLAUDE.md`（项目指南），但加载逻辑的候选名单里**没有 `CLAUDE.md`**，这份内容从未进入上下文。CLAUDE.md/AGENTS.md/.cursorrules 是同类事物，应统一作为候选。

**方案**：扩展候选名单，按优先级排列：

```go
candidates := []string{"CLAUDE.md", "AGENTS.md", "AGENTS", ".cursorrules"}
```

注意当前逻辑是"找到第一个存在的候选即返回"，加入多个候选后要确认优先级语义符合预期（建议 CLAUDE.md 优先，与 Claude Code 习惯一致）。

**验收**：
- 在有 `CLAUDE.md` 的仓库运行，`Retriever.projectInstrSrc` 指向该文件
- `BuildContext` 输出含 `## 项目指令` 段且内容来自 CLAUDE.md
- 无任何指令文件时行为不变

**级别**：P0｜成本：极低｜风险：低

---

## 4. P1 — 高价值

### ③ 只读/危险命令判断可被绕过

**现状**（`internal/tool/shell.go`）：
- `readOnlyCommands` 把 `sed`、`awk`、`curl`、`wget` 列为只读
- `isReadOnlyShellCommand` 用前缀子串匹配 + 一个粗略的 `>` 重定向检查
- `isDangerousShellCommand` 的危险列表是裸子串匹配

**问题**：
- `sed -i`、`awk` 配合 `print > "file"` 会写文件，却被判为只读 → 并发执行可能引发竞争
- `curl`/`wget` 有网络副作用（外发），被判为"只读并发安全"后绕过权限确认
- `rm  -rf`（多空格）或变量拼接可绕过 `rm -rf` 子串匹配

**方案**（分两步）：
1. **收紧只读列表**：从 `readOnlyCommands` 移除 `sed`、`awk`、`curl`、`wget`；或对它们加参数检查（含 `-i`/输出重定向即不安全）
2. **危险检测增强**：对 command 做空白归一化（`strings.Fields` 重组）后再匹配；改用 token 级匹配而非裸子串

**验收**：
- `sed -i 's/a/b/' file` → `IsReadOnly=false`
- `curl http://x` → `IsReadOnly=false`（且需权限确认）
- `rm  -rf   dir`（多空格）→ `isDangerousShellCommand=true`
- 新增对应单测

**级别**：P1｜成本：中｜风险：中（要避免误伤合法只读命令，需充分测试）

---

### ④ 配置化（老计划⑤，仍未做）

**现状**：硬编码常量散落：
- `temperature 0.2` — `internal/llm/openai.go`（3 处）
- `maxIterations 10` — `internal/agent/runner.go:17`
- `defaultResultLimit 8000` — `internal/agent/query_loop.go:28`
- `compressThreshold 0.8` — `internal/agent/query_loop.go:25` 和 `internal/memory/retriever.go:20`（两处各自定义）
- 各工具 `ResultLimit()` 内联

**问题**：调参必须改代码重编译；`compressThreshold` 在两个包各定义一份，已出现隐性耦合，将来改一处忘另一处会出 bug。

**方案**（保持极简，不引入热加载/多 profile）：
1. 新增 `internal/config/config.go`，`Load(path) (*Config, error)` + `Default()`
2. 字段：`Temperature`、`MaxIterations`、`ResultLimit`、`CompressThreshold`、`ContextLimit`
3. 优先级：env/flag > config 文件 > 代码默认
4. `main.go` 加 `-config <path>` flag，加载后注入各组件
5. 顺手把两处 `compressThreshold` 统一为 config 来源

**验收**：
- 无 config 文件时行为与现状完全一致
- 提供 config 时各参数生效（每字段一个单测）
- `OPENAI_CONTEXT_LIMIT` 保持向后兼容

**级别**：P1｜成本：中｜风险：低

---

### ⑤ `inferContextLimit` 靠模型名字串匹配

**现状**（`internal/llm/openai.go`）：一个长 `switch`，按模型名 `strings.Contains` 匹配返回固定窗口大小。

**问题**：新模型家族要改代码重编译；子串匹配易误判。

**方案**：
1. 保留 `OPENAI_CONTEXT_LIMIT` 作为最高优先级覆盖（已有）
2. 增加从 API 响应 `usage` 字段或模型列表接口推断的能力（若网关支持）
3. 退化为"未识别模型给一个保守默认（如 32k）"而非 128k，避免误判导致压缩过晚
4. 把模型→窗口的映射表抽到 `config` 或数据文件，便于扩展

**验收**：
- 未识别模型不再默认 128k，改用保守值并打印 warning
- `OPENAI_CONTEXT_LIMIT` 仍能覆盖
- 新增模型只需改数据文件，不改代码

**级别**：P1｜成本：低｜风险：低

---

## 5. P2 — 中等价值

### ⑥ in-loop 压缩是损毁式 stub，与 L2 摘要策略不一致

**现状**（`internal/agent/query_loop.go` `compressMessages`）：in-loop 消息压缩采用**损毁式丢弃**策略——把早期工具调用消息替换为占位符：

```go
// assistant 消息
Content: fmt.Sprintf("[已压缩] 调用工具: %s", strings.Join(toolNames, ", "))
// tool 消息
Content: fmt.Sprintf("[已压缩] 工具执行%s", status)
```

只保留 "调用了什么工具 / 成功失败" 两个位点，工具执行的**实际输出内容被整体丢弃**。

**问题**：这与 L2 摘要策略（`retriever.go` `CompressSummaries`，LLM 合并保留关键信息）不一致，两条压缩路径的"保真度"不在一个量级：

- **信息丢失不可逆**：`checkAndCompressContext` 触发后（`query_loop.go:219`），旧工具结果从 `lc.messages` 永久移除。若 LLM 后续还需要某条早期输出（如第一次 `git status` 的结果），拿不到了，只能重新调用工具 → 与 `detectDuplicateAndWarn` 的"不要再调用工具"直接冲突（`query_loop.go:684`）
- **成功/失败判断是启发式**：靠 `strings.Contains(msg.Content, "出错"/"错误")` 猜状态（`query_loop.go:900`），误判率高（如工具输出了 "错误码说明" 文档也会被判失败）
- **截断提示丢上下文**：`[已压缩]` 消息没有指向 `tool-results/` 持久化文件的提示，LLM 即使想回读也无从下手
- **触发条件本身存疑**：80% 阈值只发生在单轮长对话里 tool 结果堆叠时；一轮 query 内 LLM 最多调 10 次工具，通常达不到阈值（这是 `checkAndCompressContext` 无测试覆盖、不易暴露的原因之一）

**方案**：
1. **压缩时复用 tool-results 持久化**：`compressMessages` 在丢弃前调用 `tool.SaveLargeResult` 把完整结果落盘，占位符带上提示 `（完整结果已保存，可用 file read 读取）`——与 `executeSingleTool` 大结果持久化同一机制（`query_loop.go:580-587`）
2. **状态判断改为结构化字段**：`compressMessages` 目前只收 `[]ChatMessage`，信息不足。改为传入 tool 结果原始（结果里已有 `IsError`），或放宽为中性描述（`[已压缩] 工具结果（完整内容已保存）`），不再猜成功/失败
3. **对齐 L2 压缩策略**（可选，成本高）：把 in-loop 压缩也升级为 LLM 摘要——但每轮压缩都要一次 LLM 调用，延迟不可接受。**建议维持快速 stub，只做 1+2 的保真改进**

**验收**：
- 触发压缩后，被压缩的工具结果可从 `tool-results/` 读到完整内容
- `[已压缩]` 消息不再猜测成功/失败（或判断准确）
- 压缩后 LLM 仍能回答与早期工具输出相关的问题（通过回读文件）

**级别**：P2｜成本：中｜风险：中（压缩路径目前无测试，改动后需补）

---

### ⑦ token 估算过粗（ASCII/4 + CJK/2）

**现状**（`internal/memory/retriever.go` `EstimateTokens`）：

```go
for _, r := range text {
    if r <= 127 {
        asciiCount++
    } else {
        cjkCount++
    }
}
return asciiCount/4 + cjkCount/2
```

**问题**：
- **不区分语言/字符类型**：所有非 ASCII 一律按 2 字符/token，CJK 实际约 1-1.5 字符/token，而 emoji、数学符号、带变音符的拉丁字母按 CJK 估，高估 2 倍以上
- **未计入消息结构性开销**：`estimateMessagesTokens`（`query_loop.go:847`）只拼 `Content` + 工具名/参数，system prompt 的 JSON schema、role 标记、tool_calls 分片格式都不算——真实用量比估算高
- **一处估算、三处依赖**：`CheckAndCompress`（`retriever.go:171`）、`checkAndCompressContext`（`query_loop.go:219`）、`queryEngine` 步骤 2（`query_engine.go:65`）共用此估算，误差会被放大三份

**方案**：
1. **引入真实 tokenizer 或按模型族分档**：go-openai 不内置 tokenizer，可引入 `github.com/pkoukk/tiktoken-go`（对应 OpenAI/BERT 系），或至少按语言分档：ASCII/4 + CJK/1.5 + 其他/2
2. **估算结果加系数**：`EstimateTokens` 返回值乘 1.1-1.2 的安全系数，覆盖消息结构性开销，避免压缩判断过于乐观
3. **合并重复实现**：`estimateMessagesTokens` 目前拼接字符串再估算（重复分配），改为流式累加计数

**验收**：
- 中英混合文本的估算值与真实 tokenizer 误差 < 15%
- 压缩触发阈值在长对话实测中不再"过晚"（触发时上下文已接近溢出）

**级别**：P2｜成本：中｜风险：低

---

### ⑧ 跨会话记忆 / 循环内中断（老计划⑥⑦）未做

**现状**：老计划 `agentic-improvement-plan.md` 的 P2 ⑥⑦ 两项未进入本轮范围，代码中无对应实现：

- **跨会话记忆**：三层 store 全部指向会话子目录 `data/sessions/<id>/`，切换会话后 L3 结构化记忆完全丢失（"用户偏好"、"项目约定"无跨会话载体）
- **循环内中断**：`Runner.Run` 的 select 循环只支持 `/stop` 全停；工具执行走偏时（如 LLM 反复 ls 错误目录），只能等 10 步撞 `generateFinalSummary` 或 `/stop` 重来

**问题**：
- 跨会话记忆缺失削弱了三层记忆的"记忆"属性——实际是三层"会话内"记忆
- 无细粒度中断，"LLM 走错路"时体验差，且浪费 token

**方案**：完整方案见老计划对应章节（`agentic-improvement-plan.md` ⑥⑦），这里只列要点：

1. **跨会话记忆**：新增全局记忆目录 `data/global-memory/`，`Extractor` 提取时按"项目级 vs 会话级"分类，`BuildContext` 合并全局 + 会话记忆
2. **循环内中断**：`queryLoopContext` 增加 interrupt channel，`inputForward` 增加 `/interrupt`（中断当前工具并回退）、`/retry <新参数>`（重发上一步调用）协议，UI 层绑定 Esc

**验收**：
- 会话 A 建立的"项目级"记忆在会话 B 可检索到；会话级记忆不泄漏
- 工具执行中按 Esc → 当前工具立即中断回退；`/stop` 行为不受影响

**级别**：P2｜成本：高（两项独立迭代，建议拆开排期）｜风险：低

---

### ⑨ 核心路径测试覆盖不足

**现状**：12 个测试文件集中在工具层和部分 agent 层：

| 路径 | 覆盖情况 |
|------|---------|
| `internal/tool/` | ✅ 较充分（`tool_test.go` 716 行覆盖读写/截断/危险命令分类，`webfetch_test.go` 405 行） |
| `internal/memory/` | ⚠️ `retriever_test.go` 242 行只覆盖项目指令查找、截断、`BuildContext` 基本段；**`EstimateTokens`、`CompressSummaries`、`CheckAndCompress` 均无测试** |
| `internal/agent/` | ⚠️ `query_loop_test.go` 390 行覆盖并行执行/大结果持久化/权限阻塞；**`compressMessages`、`checkAndCompressContext`、`generateFinalSummary`、`detectDuplicateAndWarn` 均无测试**；`runner_test.go` 只有 3 个轻量测试 |

**问题**：
- **压缩路径无任何测试**：`compressMessages`（in-loop）、`CompressSummaries`（L2）、`EstimateTokens`（估算）全部裸奔。压缩是最易出"信息丢失"bug 的地方，且 ⑥⑦ 的改动都压在无测试路径上，改动风险不可控
- **权限路径无测试**：`DefaultPermissionChecker` 已被重构删除（内聚到工具自身），但 `CheckPermission` 在各工具的行为、`checkToolPermission` 的阻塞确认流程（`query_loop.go:628`）没有直接测试
- **`generateFinalSummary` 无测试**：P0 ① 修完后，恰好是验收依赖的测试点，应与修复同时补齐

**方案**：
1. **补压缩三件套**：`TestCompressMessages`（保留 system + 最近 2 轮、早期消息被占位、非工具消息不动）、`TestEstimateTokens`（纯 ASCII / 纯中文 / 混合 / 空串）、`TestCompressSummaries`（注入 mock LLM 或短路路径，验证合并与 `ReplaceAll`）
2. **补权限分类**：`TestIsDangerousShellCommand` 的绕过用例（多空格、变量拼接）——P1 ③ 的验收正好落在这里
3. **补 `TestGenerateFinalSummary_NoTools`**：随 P0 ① 一并提交（文档已在①中列出此测试）

**验收**：
- 上述函数各有 ≥1 个表驱动测试，`go test ./...` 全绿
- ⑥⑦ 改动前后跑通这些测试，作为回归基线

**级别**：P2｜成本：中｜风险：低

---

## 6. 版本变更说明

### v2.5.4（2026-08-24）

- **修复压缩孤儿 tool 消息（400 错误）**：`compressMessages` 压缩 assistant 消息时改为保留原始 `ToolCalls` 结构（仅替换 Content 为占位符），避免产生"无配对 assistant 的 tool 消息"——OpenAI API 会因此返回 400 `Messages with role 'tool' must be a response to a preceding message with 'tool_calls'`。新增 `validateToolPairing` 校验 + 2 个测试（普通压缩 + 持久化压缩路径）。

### v2.5.3（2026-08-24）

- **首条对话自动命名会话**：`ensurePersisted` 接收首条用户输入，会话名优先级：`/new <name>` 指定 > 首条输入自动生成（`deriveSessionName`：去换行/压缩空白/截断 40 runes）> 兜底"新会话"。REPL 与 one-shot 两处调用均已接线。新增 2 个测试。

### v2.5.2（2026-08-21）

- **每轮对话后展示 token 统计**：`UI.OnFinal` 接口增加 token 参数（input/output/total），`queryEngine` 把 `QueryEventFinal` 的精确 token（来自 include_usage 校准）传给 UI。BubbleUI 渲染完答案后打印 `⚡ 本轮 N tokens（输入 X / 输出 Y）`（total=0 时不显示）；TextUI 通过 OnEvent 转发 `{"answer", "input_tokens", ...}`。新增 2 个测试。

### v2.5.1（2026-08-21）

- **记忆保存提示静默化**：`extractMemory` 后台执行时，成功提示（"🌐 项目记忆已保存"等）不再打印——避免每轮刷屏覆盖用户正在输入的输入框。失败提示（⚠️）保留（低频异常值得打断）。三级分流逻辑不变。

### v2.5（2026-08-21）

- **三级记忆架构**（全局 / 项目 / 会话）：
  - 目录结构：`data/global-memory/`（user 类，跨项目）、`data/project-memory/`（project/reference 类，跨会话）、会话不落 L3（靠 L2 摘要 + EventStore）
  - `extractMemory` 按 type 分流；`BuildContext` 合并顺序：项目指令 → 项目记忆 → 全局记忆 → 会话摘要
  - `MigrateLegacyMemory` 幂等迁移旧数据（会话目录 + 旧全局目录 → 按 type 归位）；`/memory` 命令展示三级记忆
  - 新增 6 个测试（分流路由、层级顺序、迁移幂等）

### v2.4（2026-08-21）

- **Token 精确统计**（补充于 P2⑦ 之后的深入改进）：
  - 修复真实 bug：`ChatWithToolsStream` 未设置 `stream_options.include_usage`，OpenAI 流式响应默认不返回 usage → 累计 token 统计恒 0、压缩判断退化为纯估算。现在流式请求带 `IncludeUsage: true`
  - `queryLoopContext.msgTokens` 账本：每次 LLM 调用的 `usage.prompt_tokens`（API 精确值，含 role 标记/JSON schema 结构开销）校准消息数组 token 数；`checkAndCompressContext` 优先用精确值、未知（-1）或压缩后回退估算
  - 新增 5 个测试（含 bug 注入验证：移除校准逻辑测试即失败）

### v2.3（2026-08-21）

- **P2 全部实施**：
  - ⑥ `compressMessages` 保真改进：tool 结果占位符改中性描述（不再猜成功/失败），新增 `compressMessagesWithPersistence` 在压缩前把完整结果持久化到 `tool-results/` 并带回读提示；新增 4 个测试
  - ⑦ `EstimateTokens` 区分 ASCII/CJK/其他非 ASCII（CJK 从 2 字符/token 改为 1.5），`estimateMessagesTokens` 改流式累加 + 1.2 安全系数覆盖结构开销；新增 4 个测试
  - ⑧ 跨会话记忆：`data/global-memory/` 全局目录，`extractMemory` 按类型分流（project/reference → 全局，user/feedback → 会话），`BuildContext` 合并全局记忆索引 + 高重要性内容；新增 4 个测试。循环内中断：queryLoop 支持 inputForward 接收 `/interrupt`/`/retry`，串行工具执行前检查并注入中断提示；新增 4 个测试
  - ⑨ `CompressSummaries`/`CheckAndCompress` 补 5 个测试（httptest mock LLM），并验证测试能捕获注入 bug

### v2.2（2026-08-21）

- **P0 ①② 已实施**：
  - ① `generateFinalSummary` 传 nil tools，新增 `internal/agent/final_summary_test.go`（httptest mock 流式接口断言请求不含 tools）
  - ② 项目指令候选扩展为 `CLAUDE.md > AGENTS.md > AGENTS > .cursorrules`，新增 3 个测试
- **P1 ③④⑤ 已实施**：
  - ③ `readOnlyCommands` 移除 sed/awk/curl/wget；`isDangerousShellCommand` 空白归一化（`strings.Fields` 重组）防多空格/制表符绕过；新增测试用例
  - ④ 新增 `internal/config`（`Default`/`Load`/`Apply`/`Validate`），`-config` flag 注入 llm（Temperature/ContextLimit）、Runner（maxIter/resultLimit/compressThreshold）、Retriever（compressThreshold）；两处 `compressThreshold` 统一为可配置；`config.example.yaml` 参考文件；CLAUDE.md 补充 `-config` 用法
  - ⑤ 未识别模型上下文窗口默认从 128k 改为保守 32k（`unknownModelContextLimit`），新增 4 个测试

### v2.1（2026-08-21）

- 补充 P2 ⑥⑦⑧⑨ 四个条目的完整方案（原 v2.0 只列了总表，未展开）
- ⑥ 方案收紧为"维持快速 stub + 保真改进"（不升级为 LLM 摘要），避免每轮压缩的 LLM 调用延迟
- ⑨ 明确了当前测试矩阵（按 `internal/` 三层逐包核对），补测顺序与 P0 ①、P1 ③ 的验收对齐

### v2.0（2026-08-20）

- 初版：P0 ①②、P1 ③④⑤（P2 仅列总表）
