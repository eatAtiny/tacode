# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

A minimal Go AI agent that runs an interactive REPL loop: reads user input from stdin, builds a prompt with conversation memory context, calls the OpenAI Chat Completions API, prints the response, and persists the round to a local JSONL memory file.

## Build and Run Commands

```bash
go mod tidy                              # install dependencies
go build -o agentic .                    # build binary
go run .                                 # run (requires OPENAI_API_KEY env var)
go run . -memory ./data/memory.jsonl     # run with custom memory path
go test ./...                            # (no tests exist yet)
```

## Environment Variables

- `OPENAI_API_KEY` (required) — OpenAI API key
- `OPENAI_BASE_URL` (optional) — custom API endpoint
- `OPENAI_MODEL` (optional, default `gpt-4o-mini`) — model to use

## Architecture

All library code is under `internal/` (unexportable to external modules). The dependency graph is a clean star:

```
main.go
  └─ internal/agent  (runner.go) — REPL loop orchestrator
       ├─ internal/llm      (openai.go) — OpenAI API wrapper
       ├─ internal/memory   (memory.go) — JSONL conversation store
       └─ internal/prompt   (prompt.go) — prompt template builder
```

The three leaf packages (`llm`, `memory`, `prompt`) are independent of each other; only `agent` imports all three.

## Key Design Decisions

- **No interfaces** — concrete pointer types throughout (`*llm.OpenAIClient`, `*memory.Store`). Keeps code minimal but makes unit testing require refactoring to introduce interfaces.
- **Environment-driven config** — LLM client reads from env vars; memory path comes from `-memory` CLI flag.
- **JSONL memory** — one JSON object per line, re-read and rewritten on each append, trimmed to the last 20 records. `Digest()` produces a text summary injected into prompts.
- **Hardcoded temperature** — `0.2` in `internal/llm/openai.go`.
- **All code comments and the README are in Chinese.**

## Conventions

- Go 1.26.4, single external dependency (`github.com/sashabaranov/go-openai`)
- Constructor pattern: `NewXxx(...)` for all types
- No Makefile, no CI, no linter config, no Dockerfile
