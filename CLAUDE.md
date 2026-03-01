# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
# Build
go build -tags sqlite_fts5 -o vespra .

# Run (config path resolution: VESPRA_CONFIG env → --config flag → ~/.config/vespra/config.toml)
./vespra --config ./config.toml

# CLI flags
./vespra --config /path/to/config.toml --log-level debug --log-format json
```

The project uses CGO (via `go-sqlite3`), so a C compiler is required. The `-tags sqlite_fts5` flag is required to enable SQLite FTS5 full-text search support.

## Architecture

Vespra is a Discord AI companion with persistent memory and a web management UI. Layers:

**`main.go`** — Wires everything together: config → LLM client → per-agent memory stores → bots → agent router → web server. Handles SIGTERM/SIGINT with 30s graceful drain.

**`config/`** — TOML config loading and a thread-safe `Store` wrapper (used everywhere for hot-reload). `ResolveResponseMode(serverID, channelID)` implements channel → agent → global priority. `[[agents]]` array configures multiple Discord bots, each with their own `server_id`, optional `token`, `soul_file`, `db_path`, and per-channel overrides.

**`bot/`** — Thin `discordgo` wrapper. Ignores self/bot messages, routes everything else to the agent router. Multiple bots can run simultaneously (one default + one per agent with a custom token).

**`agent/`** — Core conversation logic:
- `router.go`: maintains a map of per-channel goroutines. Spawns new agent or routes to existing; hot-loads newly configured agents from cfgStore without restart (custom-token agents require restart). `UnloadAgent(serverID)` evicts from cache when an agent is removed/updated.
- `agent.go`: per-channel conversation loop. Each turn: check response mode → recall memories → build system prompt (soul + memories + history) → call LLM → execute tool calls → send reply. History capped to `HistoryLimit`.

**`llm/`** — HTTP client for OpenRouter chat completions and embeddings. Retry logic: up to 3 attempts with exponential backoff; retries 5xx and 429 (rate limits)/timeouts, fails fast on other 4xx.

**`memory/`** — SQLite-backed persistent memory scoped by `server_id` (DMs use `"DM:<user_id>"`). Each agent gets its own DB file (derived from `db_path` or `agents/<server_id>/memory.db` under the default db dir):
- `store.go`: save/forget/load/list/update operations with WAL mode
- `search.go`: hybrid search (cosine similarity on embeddings + SQLite LIKE on content)
- `rrf.go`: Reciprocal Rank Fusion merges the two result sets

**`soul/`** — Loads personality system prompt. Resolution: per-agent soul file → global soul file → built-in default.

**`tools/`** — Registry of AI tools: `memory_save`, `memory_recall`, `memory_forget`, `reply`, `react`, `web_search` (disabled if no API key).

**`web/`** — Embedded HTTP management UI (`web/static/`). REST API:
- `GET/POST /api/config` — read/write raw TOML config (validates before applying, hot-reloads)
- `GET/DELETE/PATCH /api/memories` — browse/delete/edit memories by `server_id`
- `GET/POST/PUT/DELETE /api/agents` — CRUD for `[[agents]]` config entries
- `GET/PUT /api/agents/{id}/soul`, `GET/PUT /api/soul` — read/write soul files
- `GET /api/status`, `GET /api/events` (SSE) — live agent status

**`migrations/`** — Single SQL migration defining the `memories` and `embeddings` tables.

## Code Style

See [GO_CODE_STYLE.md](./GO_CODE_STYLE.md) for naming, error handling, logging, goroutine, and other Go conventions used in this project.

## Testing

Write tests for critical user paths and high-risk logic. Focus on behavior and outputs, not implementation details. Prioritize integration tests at external boundaries (SQLite, HTTP). Skip trivial getters, TOML parsing or config file loading, or experimental code. Do test business logic that lives inside config packages (e.g. priority resolution like `ResolveResponseMode`).

It is acceptable to add small test-infrastructure items to production code when they enable dependency injection without a major refactor (e.g. `NewStoreFromConfig` or an unexported URL override field). Mark such additions clearly with a comment like `// for testing`.

Run all tests:
```bash
go test -tags sqlite_fts5 ./...
```

## Z.AI (GLM) API

- The correct base URL for `glm-5` is `https://api.z.ai/api/coding/paas/v4` — this is the coding/agentic endpoint. Do NOT use `https://api.z.ai/api/paas/v4` (the standard endpoint) for `glm-5`.
- Vision models (`glm-4.6v`, `glm-4.6v-flash`) live at the standard endpoint `https://api.z.ai/api/paas/v4`, but currently vision is routed through OpenRouter instead.

## Key Design Decisions

- One goroutine per active Discord channel; idle timeout after `IdleTimeoutMinutes`
- Conversation history is in-memory only (not persisted across restarts)
- Embeddings stored as little-endian float32 blobs in SQLite
- Soft-delete only for memories (`forgotten=1` flag)
- Tool-call loop repeats until LLM produces plain text (no tool calls)
- Discord messages split at 2000-char limit
- Threads are treated as independent channels
- DMs are always served using the default bot session and default memory DB, without needing an `[[agents]]` entry
- Agents without custom tokens can be hot-loaded from config changes; custom-token agents require a restart to open a new Discord session
- Config writes use a temp-file-then-rename pattern with validation before overwriting
- Web server defaults to `:8080`; set `[web] addr` in config to change
