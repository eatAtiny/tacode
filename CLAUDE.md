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
go run . -config config.yaml          # 加载配置文件（temperature/max_iterations/result_limit/compress_threshold/context_limit，参考 config.example.yaml）
go run . -mcp-server "fetch@npx -y @modelcontextprotocol/server-fetch"   # 连接 MCP server（WebFetch）
go test ./...                            # run all tests
```

## Environment Variables

- `OPENAI_API_KEY` (required) — OpenAI API key
- `OPENAI_BASE_URL` (optional) — custom API endpoint (for proxies)
- `OPENAI_MODEL` (optional, default `gpt-4o-mini`) — model to use
- `OPENAI_CONTEXT_LIMIT` (optional) — overrides auto-detected context window size

Variables can be set via shell env or a `.env` file (loaded at startup, does not override existing env vars).

## Architecture

All library code is under `internal/` (unexportable). The dependency graph:

```
main.go
  └─ internal/agent  (runner.go, query_engine.go, query_loop.go, types.go,
  │                    permission.go, memory.go, session.go)
       ├─ internal/llm        (openai.go) — Chat + ChatWithToolsStream
       ├─ internal/memory     (6 files) — 3-tier storage + retrieval + extraction
       ├─ internal/prompt     (prompt.go) — ReAct prompt builder
       ├─ internal/session    (session.go, picker.go) — session CRUD + picker
       ├─ internal/tool       (7 files) — Tool interface + Registry
       └─ internal/ui         (ui.go) — UI interface
            ├─ ui/bubble/     — BubbleUI (terminal, lipgloss + glamour)
            ├─ ui/text/       — TextUI (headless, callback-based)
            └─ ui/components/ — reusable components (conversation, input, status, toolview)
```

`llm`, `memory`, `prompt`, `session`, `tool`, and `ui` are independent leaf packages; only `agent` imports all of them.

### Two-Layer Query Architecture

**QueryEngine** (`query_engine.go`) — upper coordination:
1. Assemble messages: static system + memory preamble (`<system-reminder>`, cached) + accumulated conversation (`Runner.messages`) + per-round user task
2. Run `Compactor.Prepare()` (s08 4-step pipeline) before the first model call
3. Start `queryLoop()` → get `<-chan QueryEvent`
4. Consume events → dispatch to UI + record in EventStore
5. Return final answer + final message array (for cross-round accumulation)

**queryLoop** (`query_loop.go`) — pure ReAct core (async generator pattern):
1. `while iter < maxIterations (10)` loop
2. Run `prepareIfNeeded()` → `Compactor.Prepare` (s08 pipeline) before each model call
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

**`Compactor`** (`internal/agent/compactor.go`) — s08 4-step pipeline ported to Go's `role=tool` message model:
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
| L2 | `SummaryStore` | `summaries.jsonl` | LLM-extracted summaries (compaction fallback) |
| L3 | `MemoryStore` | `memory/*.md` | Structured long-term memory (frontmatter + content) |

L3 memory is split into **three tiers** (extraction routes by `type`):
- **Global** `data/global-memory/memory/` — `user`/`feedback` entries (cross-project preferences)
- **Project** `data/project-memory/memory/` — `project`/`reference` entries (cross-session, project-specific)
- **Session** — no L3 files (conversation details live in accumulated messages + EventStore)

`MigrateLegacyMemory` (idempotent, runs at startup) relocates legacy session/global memory files to the right tier.

Plus `EventStore` (`events.jsonl`) — append-only full event log, never truncated (truth source).

**Memory as fallback** (decoupled from compaction): With messages accumulated, memory is injected **only** on first query / session switch via `Retriever.BuildContextFallback` (project instructions + L3, **no** L2/digest). L2 summaries are written only on compaction (`compactHistory`) as the "兜底" reference, no longer per-round.

**Context building** (`Retriever.BuildContext` — fallback path only):
0. Project instructions (CLAUDE.md/AGENTS.md/.cursorrules, cached at startup, `/reload` to refresh)
0.5. Project memory (`data/project-memory/`, project/reference entries, cross-session)
0.6. Global memory (`data/global-memory/`, user entries, cross-project)
1. Session L3 memory index + high-importance memories
2. L2 recent summaries (last 10)
3. Fallback: L2 → EventStore digest → HistoryStore digest

**Memory extraction** (`Extractor`): One LLM call per round extracts L3 memory actions (create/update/delete). `user`/`feedback` memories go to global store (`data/global-memory/`), `project`/`reference` go to project store (`data/project-memory/`). Per-round L2 summaries are no longer written (superseded by accumulated messages + compaction summaries).

### Tool System

- `tool.Tool` is one of two interfaces — implements `Name()`, `Description()`, `Parameters()`, `Execute()`.
- `tool.Registry` manages registration and generates OpenAI function-calling definitions.
- Built-in tools: `shell` (bash, 30s timeout), `file` (read/write, 8KB read cap), `edit` (search-and-replace), `grep`, `list`, `git` (status/diff/log/show/branch read-only + add/commit/stash/checkout requiring confirmation), `webfetch` (HTTP GET → Markdown + page metadata, 30s timeout, 5MB body cap, 5 redirect limit), and `compact` (model-initiated context compaction, `internal/agent/compact_tool.go`).
- To add a new tool: implement `tool.Tool`, register it in `main.go`.

### UI System

- `ui.UI` is the second interface — all UI operations go through it (pluggable).
- **BubbleUI** (`ui/bubble/`): Terminal UI with lipgloss styling, glamour markdown rendering, ANSI cursor control for streaming. Uses `bufio.Scanner` for input (main loop is not Bubble Tea).
- **TextUI** (`ui/text/`): Headless mode, dispatches via `OnEvent` callback. For sub-agent scenarios. Auto-approves permissions.
- **Components** (`ui/components/`): Reusable Bubble Tea components (ConversationModel, InputModel, StatusModel, ToolViewModel).

### Permission System

- `ToolPermissionChecker` interface — `CheckPermission(toolName, args) PermissionResult`
- `DefaultPermissionChecker`: `shell` and `file` marked dangerous (need confirm). High-risk operations (`rm -rf`, `sudo`, `chmod 777`, file writes, etc.) trigger confirmation.
- Confirm flows through `UI.ConfirmPermission()` → `PermissionCh` channel → back to queryLoop (blocking).
- Replace via `SetPermissionChecker()` to inject custom policy.

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
| `/compress` | Manual summary compression |
| `/balance` | 查询 DeepSeek 账户余额；成功后每轮对话结束自动展示剩余额度 |
| `/reload` | Reload AGENTS.md project instructions |
| `/memory` | List L3 memories |
| `/memory add <content>` | Add L3 memory |
| `/memory rm <name>` | Delete L3 memory |
| `/interrupt` | During tool execution: abort remaining tools, let LLM adjust (mid-loop) |
| `/stop` | Cancel running query |
| `exit` | Quit |

## ReAct Flow

On each user input:
1. Build context from 3-tier memory
2. Build system + user prompts with tool definitions
3. Call LLM streaming; if LLM returns text directly → done
4. If LLM requests tool calls → enter ReAct loop (max 10 iterations):
   - Permission check → execute tools → feed results back → repeat
5. After final answer: background goroutine saves L1 history, extracts L2 summary and L3 memories

## Key Design Decisions

- **Two interfaces only** — `tool.Tool` and `ui.UI`. Everything else uses concrete pointer types.
- **Async generator pattern** — `queryLoop` returns `<-chan QueryEvent`, decoupling core loop from UI/event logging.
- **Channel-based main loop** — `select` over input channel and query result channel, enabling `/stop` mid-query cancellation.
- **Temporary sessions** — lazy persistence avoids empty sessions in manifest.
- **Hardcoded temperature** — `0.2` in `internal/llm/openai.go`.
- **Hardcoded max iterations** — `10` in `internal/agent/runner.go`.
- **All code comments and README are in Chinese.**

## File Map

### `internal/agent/` (9 files)
- `runner.go` — `Runner` struct, `Run()` REPL, async query dispatch, cross-round message accumulation
- `query_engine.go` — message assembly (system + preamble + conversation + task), compactor prepare, event consumption
- `query_loop.go` — core ReAct loop, streaming, tool execution, compaction wiring, reactive compact
- `types.go` — `QueryEvent`, `QueryEventType` enum, `queryResult`
- `permission.go` — `ToolPermissionChecker`, `DefaultPermissionChecker`, risk detection
- `memory.go` — L3 memory extraction, `/compress` and `/memory` commands
- `session.go` — session commands, `ensurePersisted()`, `cleanOrphanTempDirs()`
- `compactor.go` — s08 4-step compaction pipeline (Go port, pairing protection)
- `compact_tool.go` — model-initiated `compact` tool

### `internal/memory/` (6 files)
- `types.go` — `MemoryEntry`, `Summary`, `ExtractionResult`, `Record`, `MemoryAction`
- `event.go` — `EventStore` (append-only JSONL, truth source)
- `history.go` — `HistoryStore` (L1, max 50 records)
- `summary_store.go` — `SummaryStore` (L2, JSONL)
- `store.go` — `MemoryStore` (L3, .md files with frontmatter)
- `extractor.go` — `Extractor` (LLM-driven memory extraction)
- `retriever.go` — `Retriever` (3-tier retrieval + compression)

### `internal/ui/` (5 files + components)
- `ui.go` — `UI` interface definition
- `bubble/bubble.go` — BubbleUI terminal implementation
- `text/text.go` — TextUI headless implementation
- `components/conversation.go` — scrollable conversation history
- `components/input.go` — text input bar
- `components/status.go` — status bar (session, model, tokens)
- `components/toolview.go` — tool execution popup

## Conventions

- Go 1.26.4
- Dependencies: `go-openai`, `bubbletea`, `bubbles`, `glamour`, `lipgloss`
- Constructor pattern: `NewXxx(...)` for all types
- No Makefile, no CI, no linter config, no Dockerfile
