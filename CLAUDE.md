# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
# Build
go build -o mnemon-bot .

# Run (config path resolution: MNEMON_CONFIG env → --config flag → ~/.config/mnemon-bot/config.toml)
./mnemon-bot --config ./config.toml

# CLI flags
./mnemon-bot --config /path/to/config.toml --log-level debug --log-format json
```

The project uses CGO (via `go-sqlite3`), so a C compiler is required.

## Architecture

Mnemon-bot is a Discord AI companion (~1,400 lines of Go) with persistent memory. Layers:

**`main.go`** — Wires everything together: config → LLM client → memory store → bot → agent router. Handles SIGTERM/SIGINT with 30s graceful drain.

**`config/`** — TOML config loading. `ResolveResponseMode(serverID, channelID)` implements channel → server → global priority for response mode.

**`bot/`** — Thin `discordgo` wrapper. Ignores self/bot messages, routes everything else to the agent router.

**`agent/`** — Core conversation logic:
- `router.go`: maintains a map of per-channel goroutines. Spawns new agent or routes to existing; respawns if channel buffer full.
- `agent.go`: per-channel conversation loop. Each turn: check response mode → recall memories → build system prompt (soul + memories + history) → call LLM → execute tool calls → send reply. History capped to `HistoryLimit`.

**`llm/`** — HTTP client for OpenRouter chat completions and embeddings. Retry logic: up to 3 attempts with exponential backoff; retries 5xx/timeouts, fails fast on 4xx.

**`memory/`** — SQLite-backed persistent memory scoped by `server_id` (DMs use `"DM:<user_id>"`):
- `store.go`: save/forget/load operations with WAL mode
- `search.go`: hybrid search (cosine similarity on embeddings + SQLite LIKE on content)
- `rrf.go`: Reciprocal Rank Fusion merges the two result sets

**`soul/`** — Loads personality system prompt. Resolution: per-server soul file → global soul file → built-in default.

**`tools/`** — Registry of AI tools: `memory_save`, `memory_recall`, `memory_forget`, `reply`, `react`, `web_search` (disabled if no API key).

**`migrations/`** — Single SQL migration defining the `memories` and `embeddings` tables.

## Code Style

See [GO_CODE_STYLE.md](./GO_CODE_STYLE.md) for naming, error handling, logging, goroutine, and other Go conventions used in this project.

## Key Design Decisions

- One goroutine per active Discord channel; idle timeout after `IdleTimeoutMinutes`
- Conversation history is in-memory only (not persisted across restarts)
- Embeddings stored as little-endian float32 blobs in SQLite
- Soft-delete only for memories (`forgotten=1` flag)
- Tool-call loop repeats until LLM produces plain text (no tool calls)
- Discord messages split at 2000-char limit
- Threads are treated as independent channels
