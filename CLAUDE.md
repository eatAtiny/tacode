# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A Go ReAct agent with interactive REPL, three-layer memory, streaming LLM, pluggable UI, and multi-session management. Reads user input, decides whether to answer directly or invoke tools (shell, file), loops tool calls until it has enough information, then returns a final answer.

## Build and Run Commands

```bash
go mod tidy                              # install dependencies
go build -o agentic .                    # build binary
go run .                                 # run (requires OPENAI_API_KEY)
go run . -sessions ./data/sessions       # custom sessions directory
go run . -session <id>                   # resume a specific session
go run . -env .env                       # custom env file path
go run . -one-shot "列出当前目录文件"     # headless 单次运行，输出最终答案后退出
go run . -sandbox on                  # 启用 shell 沙箱（网络/文件系统隔离，macOS/Linux）
go run . -config config.yaml          # 加载配置文件（temperature/max_iterations/result_limit/context_char_limit 生效；compress_threshold/context_limit 为预留字段，参考 config.example.yaml）
go run . -mcp-server "fetch@npx -y @modelcontextprotocol/server-fetch"   # 连接 MCP server（WebFetch）
go vet ./...                            # 静态检查
gofmt -l .                              # 检查 gofmt 格式（无输出即通过）
go test ./...                           # run all tests
```

## Environment Variables

- `OPENAI_API_KEY` (required) — OpenAI API key
- `OPENAI_BASE_URL` (optional) — custom API endpoint (for proxies)
- `OPENAI_MODEL` (optional, default `gpt-4o-mini`) — model to use
- `OPENAI_CONTEXT_LIMIT` (optional, 预留) — 覆盖自动推断的上下文窗口大小；当前无消费方（压缩判断已改用 Compactor 字符口径），无生效路径

Variables can be set via shell env or a `.env` file (loaded at startup, does not override existing env vars).

## Architecture

All library code is under `internal/` (unexportable). The dependency graph:

```
main.go + bootstrap.go   (bootstrap.go: buildMemoryStack / buildTools / buildUI / fatal)
  └─ internal/agent  (runner.go, repl.go, query_engine.go, query_loop.go,
  │                    tool_exec.go, oneshot.go, types.go, permission.go,
  │                    memory.go, session.go, compactor.go, compact_tool.go,
  │                    balance.go)
       ├─ internal/llm        (openai.go, balance.go, retry.go) — Chat + ChatWithToolsStream + 余额查询 + 重试
       ├─ internal/memory     (7 files) — 3-tier storage + retrieval + extraction
       ├─ internal/prompt     (prompt.go) — ReAct prompt builder
       ├─ internal/session    (session.go, picker.go) — session CRUD + picker
       ├─ internal/tool       (8 files) — Tool interface + Registry
       └─ internal/ui         (ui.go) — UI interface
            ├─ ui/bubble/     — BubbleUI inline 聊天界面（chat.go ChatModel + bubble.go 包装器 + box.go 框线 + welcome.go 欢迎界面）
            └─ ui/text/       — TextUI (headless, callback-based)
```

`llm`, `memory`, `prompt`, `session`, `tool`, and `ui` are independent leaf packages; only `agent` imports all of them.

### Two-Layer Query Architecture

**QueryEngine** (`query_engine.go`) — upper coordination:
1. Assemble messages: static system + memory preamble (`<system-reminder>`, cached) + accumulated conversation (`Runner.messages`) + per-round user task
2. Run `Compactor.Prepare()` (s08 字符口径管线无损阶段) before the first model call
3. Start `queryLoop()` → get `<-chan QueryEvent`
4. Consume events → dispatch to UI + record in EventStore
5. Return final answer + final message array (for cross-round accumulation)

**queryLoop** (`query_loop.go`) — pure ReAct core (async generator pattern):
1. `while iter < maxIterations (10)` loop
2. Run `prepareIfNeeded()` → `Compactor.Prepare` (s08 字符口径管线无损阶段) before each model call
3. Call LLM streaming → yield `Think` / `Delta` events
4. If no tool_calls → yield `Final` (carrying full `Messages`) and return
5. If tool_calls → permission check → execute → yield `ToolCall` / `ToolResult`
6. Push results back into messages → continue
7. If any tool call was `compact` → run `compactHistory` on the closed round
8. On `prompt_too_long` / `too many tokens` → `reactiveCompact` + retry once (`maxReactiveRetries=1`)
9. Detect duplicate tool calls → inject warning
10. At max iterations → LLM generates final summary

### Message Accumulation + Compaction (s08 port)

Messages now **accumulate across rounds** in `Runner.messages` (cleared on session switch `/new`). The prompt layout is prefix-cache-optimized:

```
[0] system     — fully static (tool descriptions + guides)
[1] preamble   — <system-reminder> memory reference (cached, rebuilt only on switch/first query)
[2..]          — accumulated conversation
[last]         — 轮次: N + 用户任务 (only per-round-changing message)
```

**`Compactor`** (`internal/agent/compactor.go`) — s08 5-step pipeline（4 步无损 + 1 步 LLM 摘要）ported to Go's `role=tool` message model:
1. `toolResultBudget` — persist >30K results in the latest tool batch (total >200K) to `<session>/tool-results/`, keep `<persisted-output>` 2000-char preview
2. `snipCompact` — >50 messages: keep head 3 + tail 46, archive middle to `<session>/transcripts/`, insert marker; **pairing protection** keeps `assistant(ToolCalls)`↔`tool` pairs intact (else API 400)
3. `microCompact` — shorten consumed results (keep last 3, >120 chars → path reference) toward 80% of `context_char_limit` (default 50K)
4. `fitToolResults` — persist oversized results largest-first (1000-char preview)
5. `compactHistory` — LLM state summary (only lossy step, preserves system + injects `activeRequest`)

Plus `reactiveCompact` (API rejection salvage) and the `compact` tool (model-initiated, runs after the closed tool batch).

### Three-Layer Memory System

| Layer | Store | File | Purpose |
|-------|-------|------|---------|
| L1 | `HistoryStore` | `history.jsonl` | Raw conversation records (max 50) |
| L2 | `SummaryStore` | `summaries.jsonl` | 预留层：当前无生产写入路径，仅 `/compress` 手动路径读写 |
| L3 | `MemoryStore` | `memory/*.md` | Structured long-term memory (frontmatter + content) |

L3 memory is split into **three tiers** (extraction routes by `type`):
- **Global** `data/global-memory/memory/` — `user`/`feedback` entries (cross-project preferences)
- **Project** `data/project-memory/memory/` — `project`/`reference` entries (cross-session, project-specific)
- **Session** — no L3 files (conversation details live in accumulated messages + EventStore)

`MigrateLegacyMemory` (idempotent, runs at startup) relocates legacy session/global memory files to the right tier.

Plus `EventStore` (`events.jsonl`) — append-only full event log, never truncated (truth source).

**Memory as fallback** (decoupled from compaction): With messages accumulated, memory is injected **only** on first query / session switch via `Retriever.BuildContextFallback` (project instructions + L3, **no** L2/digest). L2 摘要当前无生产写入路径：`compactHistory` 仅在压缩时于对话内生成摘要消息，不写 `summaries.jsonl`（`ExtractionResult.Summary` / `CheckAndCompress` 均为预留）。

**Context building** — 生产路径走 `Retriever.BuildContextFallback`（仅项目指令 + L3 记忆，无 L2/digest）；`Retriever.BuildContext` 为预留路径（当前无生产调用），其构建步骤包括：
0. Project instructions (CLAUDE.md/AGENTS.md/AGENTS/.cursorrules, cached at startup, `/reload` to refresh)
0.5. Project memory (`data/project-memory/`, project/reference entries, cross-session)
0.6. Global memory (`data/global-memory/`, user entries, cross-project)
1. Session L3 memory index + high-importance memories
2. L2 recent summaries (last 10)
3. Fallback: L2 → EventStore digest → HistoryStore digest

**Memory extraction** (`Extractor`): One LLM call per round extracts L3 memory actions (create/update/delete). `user`/`feedback` memories go to global store (`data/global-memory/`), `project`/`reference` go to project store (`data/project-memory/`). `ExtractionResult.Summary` 字段当前无消费者（预留）；L2 摘要不再每轮写入（对话细节由跨轮累积消息承载）。

### Tool System

- `tool.Tool` is one of two interfaces — 10 个方法：`Name` / `Aliases` / `Description` / `Parameters` / `Execute` / `CheckPermission`（权限内聚）/ `PromptGuide`（注入使用指南）/ `IsConcurrencySafe` / `IsReadOnly` / `ResultLimit`。
- `tool.Registry` manages registration and generates OpenAI function-calling definitions.
- Built-in tools: `shell` (bash, 30s timeout), `file` (read/write, 8KB read cap), `edit` (search-and-replace), `grep`, `list`, `git` (status/diff/log/show/branch read-only + add/commit/stash/checkout requiring confirmation), `webfetch` (HTTP GET → Markdown + page metadata, 30s timeout, 5MB body cap, 5 redirect limit), and `compact` (model-initiated context compaction, `internal/agent/compact_tool.go`).
- To add a new tool: implement `tool.Tool`, register it in `bootstrap.go` 的 `buildTools()` 中。

### UI System

- `ui.UI` is the second interface — all UI operations go through it (pluggable).
- **BubbleUI** (`ui/bubble/`): inline 聊天界面（参照 j178/chatgpt 模式）。`View()` 只渲染活区（查询状态行 + 权限确认弹层 + `bubbles.Textarea` 输入框 + footer）原地重绘；对话内容经 `commit()` → `tea.Println` 定稿，打印于活区上方滚入终端原生 scrollback。无 alt screen、无鼠标捕获（终端原生选择/复制/滚轮保留）；流式逐段定稿（增量遇换行冲刷）；最终回答经 `glamour` 渲染 markdown。`bubble.go` 是 tea 包装器——UI 事件方法（OnThink/OnDelta/OnFinal 等）→ `Program.Send` 投递消息 → ChatModel.Update 定稿。输入由 textarea 接管（Enter 提交 → submitCh → Runner）。`box.go` 提供工具框线。
- **TextUI** (`ui/text/`): Headless mode, dispatches via `OnEvent` callback. For sub-agent scenarios. Auto-approves permissions.

### Permission System

- 权限内聚在工具自身：`Tool.CheckPermission(args) → PermissionResult`（工具声明自身操作的权限判定）。
- `shell`/`file` 的写操作、`edit` 等高风险操作触发确认；`grep`/`list`/`webfetch` 等只读操作默认放行。
- 可选全局注入点 `ToolPermissionChecker`（`CheckPermission(toolName, args) bool`）：通过 `SetPermissionChecker()` 注入自定义策略时覆盖所有工具判定（默认 nil = 用工具自身 `CheckPermission`）。另有全局 `ForbiddenTools` 禁止列表（管理级完全禁用某工具，当前无代码向其追加）。
- Confirm flows through `UI.ConfirmPermission()` → `PermissionCh` channel → back to queryLoop (blocking).
- BubbleUI 下权限确认显示为输入框上方的弹层（黄色警告框，含工具/参数/原因），用户在 textarea 输入 y/N。

### Session Management

- Each session is a subdirectory: `data/sessions/<id>/` containing `history.jsonl`, `summaries.jsonl`, `events.jsonl`, `memory/`.
- `manifest.json` tracks all sessions + active session.
- **Temporary sessions**: On startup, creates temp session NOT in manifest. Only persisted after first real dialogue via `ensurePersisted()` (prevents empty session clutter).
- `cleanOrphanTempDirs()` removes stale temp dirs from crashes.
- Session picker uses Bubble Tea for interactive selection.

### REPL Commands

| Command | Function |
|---------|----------|
| `/new [name]` | Create and switch to new session |
| `/list` | Interactive session picker (↑↓/Enter/Esc) |
| `/switch <id>` | Switch by ID prefix |
| `/delete <id>` | Delete session (not active one) |
| `/rename <name>` | Rename current session |
| `/current` | Show current session info |
| `/compress` | Manual summary compression（当前无 L2 写入路径，实际为空操作；L2 层预留） |
| `/balance` | 查询 DeepSeek 账户余额；成功后每轮对话结束自动展示剩余额度 |
| `/reload` | Reload AGENTS.md project instructions |
| `/memory` | List L3 memories |
| `/memory add <content>` | Add L3 memory |
| `/memory rm <name>` | Delete L3 memory |
| `/interrupt` | 查询运行中（含工具执行间隙）输入 `/interrupt` 或 `/retry` → 经 `inputForward` 转发给运行中的循环，中止剩余工具并注入中断提示，让 LLM 调整策略 |
| `/stop` | Cancel running query |
| `exit` | Quit |

## ReAct Flow

On each user input:
1. 组装消息：静态 system + 记忆 preamble（`<system-reminder>`，仅首轮/会话切换时注入，会话内缓存稳定）+ 跨轮累积对话（`Runner.messages`）+ 用户任务
2. 运行 `Compactor` 字符口径压缩管线（5 步：4 无损 + 1 LLM 摘要）后调用 LLM 流式生成
3. LLM 直接返回文本 → 完成
4. 否则进入 ReAct 循环（最多 10 轮）：权限检查（工具自检 + 可选全局注入点）→ 执行工具 → 结果反馈 → 重复
5. 最终回答后：后台 goroutine 保存 L1 历史、提取 L3 记忆（L2 摘要当前无生产写入路径）、临时会话落盘

## Key Design Decisions

- **Two interfaces only** — `tool.Tool` and `ui.UI`. Everything else uses concrete pointer types.
- **Async generator pattern** — `queryLoop` returns `<-chan QueryEvent`, decoupling core loop from UI/event logging.
- **Channel-based main loop** — `select` over input channel and query result channel, enabling `/stop` mid-query cancellation.
- **Temporary sessions** — lazy persistence avoids empty sessions in manifest.
- **Hardcoded temperature** — `0.2` in `internal/llm/openai.go`.
- **Hardcoded max iterations** — `10` in `internal/agent/runner.go`.
- **All code comments and README are in Chinese.**

## File Map

### 根目录 (2 files)
- `main.go` — 入口：flag 解析 → 配置加载 → 装配（bootstrap.go）→ 模式分支（REPL / one-shot）
- `bootstrap.go` — 依赖装配：`buildMemoryStack` / `buildTools` / `buildUI` / `fatal`

### `internal/agent/` (13 files)
- `runner.go` — `Runner` struct, `Run()` REPL, async query dispatch, cross-round message accumulation
- `repl.go` — REPL 主循环：select 模型 + 输入排队/转发协议（/interrupt、/retry 控制命令经 `inputForward` 转发）
- `query_engine.go` — message assembly (system + preamble + conversation + task), compactor prepare, event consumption
- `query_loop.go` — core ReAct loop, streaming, tool execution, compaction wiring, reactive compact
- `tool_exec.go` — 工具调用执行子系统：并发/串行分类、权限确认协议、中断注入、read-before-edit 状态
- `oneshot.go` — headless 单次查询入口（`RunOnce`，配合 TextUI / 子 agent）
- `types.go` — `QueryEvent`, `QueryEventType` enum, `queryResult`
- `permission.go` — `ToolPermissionChecker` 接口、`globalPermissionChecker` 注入点、`ForbiddenTools` 列表
- `memory.go` — L3 memory extraction, `/compress` and `/memory` commands
- `session.go` — session commands, `ensurePersisted()`, `cleanOrphanTempDirs()`
- `compactor.go` — s08 5-step compaction pipeline (Go port, pairing protection)
- `compact_tool.go` — model-initiated `compact` tool
- `balance.go` — 余额查询辅助 + 格式化 + `/balance` 命令处理

### `internal/memory/` (7 files)
- `types.go` — `MemoryEntry`, `Summary`, `ExtractionResult`, `Record`, `MemoryAction`
- `event.go` — `EventStore` (append-only JSONL, truth source)
- `history.go` — `HistoryStore` (L1, max 50 records)
- `summary_store.go` — `SummaryStore` (L2, JSONL)
- `store.go` — `MemoryStore` (L3, .md files with frontmatter)
- `extractor.go` — `Extractor` (LLM-driven memory extraction)
- `retriever.go` — `Retriever` (3-tier retrieval + compression)

### `internal/ui/` (6 files)
- `ui.go` — `UI` interface definition
- `bubble/bubble.go` — BubbleUI tea 包装器（事件→Program.Send、输入桥接、生命周期）
- `bubble/chat.go` — ChatModel（inline 聊天界面：活区渲染状态行/权限弹层/textarea/footer，对话经 commit→tea.Println 定稿）
- `bubble/box.go` — 工具调用/结果框线共享渲染
- `bubble/welcome.go` — Claude Code 风格欢迎界面（ASCII logo + 版本/模型/目录）
- `text/text.go` — TextUI headless implementation

### `internal/llm/` (3 files)
- `openai.go` — OpenAI 客户端封装（Chat + ChatWithToolsStream）；`inferContextLimit` 上下文窗口推断为预留路径（无消费方）
- `balance.go` — DeepSeek 余额查询
- `retry.go` — LLM 请求重试（429/5xx/网络错误）

### `internal/prompt/` (1 file)
- `prompt.go` — ReAct System/User 提示词模板 + `<system-reminder>` 构建

### `internal/session/` (2 files)
- `session.go` — SessionManager：会话 CRUD + manifest 管理
- `picker.go` — 交互式会话选择器（Bubble Tea）

### `internal/tool/` (8 files)
- `tool.go` — `Tool` 接口 + `Registry`（生成 Function Calling 定义）
- `shell.go` — Shell 工具（bash，30s 超时，危险命令检测，可选沙箱）
- `file.go` — File 工具（读写文件，读 8KB 上限，写需确认）
- `edit.go` — search-and-replace 编辑工具（唯一性校验 + diff 输出）
- `grep.go` — 结构化文本搜索工具（纯只读）
- `list.go` — 结构化目录列表工具（纯只读）
- `git.go` — 结构化 git 操作工具（status/diff/log/show/branch 只读 + 写操作需确认）
- `webfetch.go` — 网页抓取工具（GET → Markdown + 元信息，默认放行）

### `internal/config/` (1 file)
- `config.go` — 运行时配置（YAML 加载，优先级 config > 环境变量 > 默认）

### `internal/sandbox/` (4 files)
- `sandbox.go` — 沙箱抽象：网络/文件系统隔离 + 资源限制
- `linux.go` — Linux 实现
- `macos.go` — macOS 实现（seatbelt 临时 profile）
- `sandbox_other.go` — 其他平台（noop 回退）

### `internal/mcp/` (2 files)
- `client.go` — MCP `Manager` + stdio 客户端（连接外部 MCP server）
- `adapter.go` — MCP 工具 → `tool.Tool` 适配器

## Conventions

- Go 1.26.4
- Dependencies: `go-openai`, `bubbletea`, `bubbles`, `glamour`, `lipgloss`
- Constructor pattern: `NewXxx(...)` for all types
- No Makefile, no CI, no linter config, no Dockerfile
