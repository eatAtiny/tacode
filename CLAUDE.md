# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A minimal Go ReAct agent with an interactive REPL. Reads user input, decides whether to answer directly or invoke tools (shell, file read/write), loops tool calls until it has enough information, then returns a final answer. Each round is persisted to a JSONL memory file for conversation context.

## Build and Run Commands

```bash
go mod tidy                              # install dependencies
go build -o agentic .                    # build binary
go run .                                 # run (requires OPENAI_API_KEY)
go run . -memory ./data/memory.jsonl     # custom memory path
go run . -env .env                       # custom env file path
go test ./...                            # (no tests exist yet)
```

## Environment Variables

- `OPENAI_API_KEY` (required) — OpenAI API key
- `OPENAI_BASE_URL` (optional) — custom API endpoint (for proxies)
- `OPENAI_MODEL` (optional, default `gpt-4o-mini`) — model to use

Variables can be set via shell env or a `.env` file (loaded at startup, does not override existing env vars).

## Architecture

All library code is under `internal/` (unexportable). The dependency graph:

```
main.go
  └─ internal/agent  (runner.go) — REPL loop + ReAct orchestrator
       ├─ internal/llm      (openai.go) — OpenAI Chat Completions wrapper
       ├─ internal/memory   (memory.go) — JSONL conversation store
       ├─ internal/prompt   (prompt.go) — prompt template builder
       └─ internal/tool     (tool.go, shell.go, file.go) — tool registry + implementations
```

`llm`, `memory`, `prompt`, and `tool` are independent leaf packages; only `agent` imports all of them.

### ReAct Flow

The agent does NOT simply forward every question to the LLM. On each user input:
1. Calls LLM with tool definitions; if LLM returns text directly → done.
2. If LLM requests tool calls → enters a ReAct loop (max 10 iterations): execute tools, feed results back, repeat until LLM gives a final answer.

### Tool System

- `tool.Tool` is the **one interface** in the project — all tools implement `Name()`, `Description()`, `Parameters()`, `Execute()`.
- `tool.Registry` manages registration and generates OpenAI function-calling definitions.
- Built-in tools: `shell` (runs bash commands, 30s timeout) and `file` (read/write files, 8KB read cap).
- To add a new tool: implement `tool.Tool`, register it in `main.go`.

## Key Design Decisions

- **Minimal interfaces** — only `tool.Tool` is an interface; everything else uses concrete pointer types (`*llm.OpenAIClient`, `*memory.Store`).
- **JSONL memory** — one JSON object per line, re-read and rewritten on each append, trimmed to the last 20 records. `Digest()` produces a text summary injected into prompts.
- **Hardcoded temperature** — `0.2` in `internal/llm/openai.go`.
- **All code comments and the README are in Chinese.**

## Conventions

- Go 1.26.4, two external dependencies (`github.com/sashabaranov/go-openai`, `github.com/chzyer/readline`)
- Constructor pattern: `NewXxx(...)` for all types
- No Makefile, no CI, no linter config, no Dockerfile
