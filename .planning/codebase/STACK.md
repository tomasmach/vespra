# Technology Stack

**Analysis Date:** 2026-02-26

## Languages

**Primary:**
- Go 1.24.0 - Entire application (Discord bot, LLM integration, SQLite persistence, web server)

## Runtime

**Environment:**
- Go 1.24.0 runtime with CGO enabled (required for `go-sqlite3`)

**Build Target:**
- Linux (Debian bookworm-slim in Docker)
- Requires C compiler for `go-sqlite3` (libsqlite3-dev)

**Package Manager:**
- Go modules via `go.mod`
- Lockfile: `go.sum` (present)

## Frameworks

**Core:**
- `github.com/bwmarrin/discordgo` v0.29.0 - Discord API client (gateway, sessions, message handling)

**Data & Persistence:**
- `github.com/mattn/go-sqlite3` v1.14.34 - SQLite driver (CGO required; used for memory storage with WAL mode)

**Configuration:**
- `github.com/BurntSushi/toml` v1.6.0 - TOML config file parsing and serialization

**Standard Library:**
- `net/http` - HTTP client for LLM API calls and embedded web server
- `log/slog` - Structured logging (text/JSON output)
- `crypto/rand`, `encoding/json`, `encoding/binary` - Utility libraries

## Key Dependencies

**Critical:**
- `github.com/bwmarrin/discordgo` v0.29.0 - Discord gateway and message routing
  - Depends on `github.com/gorilla/websocket` v1.4.2 for Discord WebSocket connection
- `github.com/mattn/go-sqlite3` v1.14.34 - Persistent memory store; WAL mode for concurrent reads
- `golang.org/x/net` v0.50.0 - HTTP networking (html parsing, TLS, system networking)

**Security & Crypto:**
- `golang.org/x/crypto` v0.48.0 - TLS, authentication, JWT support (used by discordgo)
- `golang.org/x/sys` v0.41.0 - OS-level interfaces (signal handling, platform-specific syscalls)

## Configuration

**Environment:**
- Discord bot token: `[bot] token` in config.toml or `VESPRA_CONFIG` env var
- OpenRouter API key: `[llm] openrouter_key` (required for chat completions)
- GLM API key: `[llm] glm_key` (optional; enables GLM provider and web_search tool)
- Database path: `[memory] db_path` or `VESPRA_DB_PATH` env var override

**Build:**
- `Dockerfile` — Multi-stage build (builder: golang:1.24-bookworm → runtime: debian:bookworm-slim)
- `docker-compose.yml` — Orchestration with volume mounts for `/data` and `/config`

**Key Configs:**
- `[llm] model` — Model ID (default: openrouter model)
- `[llm] embedding_model` — Embedding model for memory search (e.g., `openai/text-embedding-3-small`)
- `[llm] request_timeout_seconds` — HTTP timeout for LLM requests
- `[tools] web_timeout_seconds` — Timeout for web_fetch and web_search operations
- `[agent] history_limit` — In-memory conversation history (per channel, default 20)
- `[response] default_mode` — Bot response policy (smart/mention/all/none)

## Platform Requirements

**Development:**
- Go 1.24.0+
- C compiler (gcc/clang) for `go-sqlite3` compilation
- Git (for cloning the repo)

**Production:**
- Debian Linux (or compatible; Docker image uses debian:bookworm-slim)
- CA certificates (`ca-certificates` package for TLS)
- Writable volumes for `/data` (SQLite databases) and `/config` (config.toml)
- Network access to:
  - Discord Gateway WebSocket (`gateway.discord.gg`)
  - OpenRouter API (`openrouter.ai/api/v1`)
  - GLM API (`https://api.z.ai/api/paas/v4` for chat or vision models)
  - Optional: web servers (for web_fetch tool), search APIs

---

*Stack analysis: 2026-02-26*
