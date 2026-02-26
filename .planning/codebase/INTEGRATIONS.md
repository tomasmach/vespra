# External Integrations

**Analysis Date:** 2026-02-26

## APIs & External Services

**LLM Providers:**
- **OpenRouter** - Primary LLM provider
  - SDK/Client: `github.com/bwmarrin/discordgo` (custom HTTP wrapper in `llm/llm.go`)
  - Endpoint: `https://openrouter.ai/api/v1` (configurable via `[llm] base_url`)
  - Auth: Bearer token in `[llm] openrouter_key`
  - Used for: chat completions, function calling (tool calls)
  - Configuration: `[llm] model` (e.g., `anthropic/claude-3.5-sonnet`)

- **GLM (Z.AI)** - Alternative LLM provider and web search backend
  - Endpoint: `https://api.z.ai/api/paas/v4` (configurable via `[llm] glm_base_url`)
  - Auth: Bearer token in `[llm] glm_key` (optional; disables web_search if absent)
  - Used for: chat completions (provider override per-agent), web search (asynchronous)
  - Configuration: `[llm] glm_key` enables GLM provider; `[llm] glm_base_url` sets endpoint
  - Vision models: GLM vision models (`glm-4.6v`, `glm-4.6v-flash`) at configurable `[llm] vision_base_url`
  - Note: When vision content present, vision model takes priority; if vision endpoint matches GLM endpoint, GLM key is used

- **OpenRouter Embeddings** - Vector embeddings for memory search
  - Endpoint: Same as OpenRouter chat (or overridable via `[llm] embedding_base_url`)
  - Auth: Bearer `[llm] openrouter_key`
  - Configuration: `[llm] embedding_model` (e.g., `openai/text-embedding-3-small`)
  - Used for: Semantic search of saved memories via cosine similarity

**Web Tools:**
- **Web Fetch** - Synchronous HTTP page retrieval
  - Location: `tools/web_fetch.go`
  - Functionality: Fetch arbitrary URL, extract readable text content (strips scripts, styles, nav, footer, etc.)
  - Max response size: 2 MB
  - Max output: 8000 characters
  - Timeout: Configurable via `[tools] web_timeout_seconds` (default 15s)
  - Used by: LLM tool `web_fetch` for reading article content, inspecting URLs

- **Web Search** - Asynchronous web search via GLM native tools
  - Location: `tools/tools.go` (webSearchTool)
  - Requirement: `[llm] glm_key` must be configured (disabled if absent)
  - Mechanism: Submits search via GLM's native `web_search` tool (search_pro engine)
  - Timeout: Configurable via `[tools] web_timeout_seconds`
  - Returns: Summaries and URLs injected back into agent via internal channel
  - Note: Async operation — agent acknowledges search to user, results arrive in follow-up message

## Data Storage

**Databases:**
- **SQLite** (local filesystem)
  - Provider: `github.com/mattn/go-sqlite3`
  - Connection: File-based at `[memory] db_path` (default: `~/.local/share/vespra/vespra.db`)
  - Mode: WAL (Write-Ahead Logging) enabled for concurrent reads
  - Tables: `memories`, `embeddings`, `conversations`
  - Per-agent override: `[[agents]]` can specify custom `db_path` per server
  - DMs: Use synthetic server ID `DM:<user_id>`

**File Storage:**
- Local filesystem only (no cloud storage)
  - Config file: TOML at path resolved by `config.Resolve()` or `--config` flag
  - Soul files: Markdown per-agent (`[[agents]] soul_file`) or global (`[bot] soul_file`)
  - Database files: SQLite files in `[memory] db_path` or per-agent `db_path`

**Caching:**
- In-memory conversation history per channel (capped to `[agent] history_limit`)
- No persistent caching layer (not applicable)

## Authentication & Identity

**Discord:**
- Auth Provider: Discord (OAuth2-like bot token)
- Implementation: Bot token-based connection via `github.com/bwmarrin/discordgo`
- Token location: `[bot] token` in config.toml (required)
- Per-agent tokens: `[[agents]] token` (custom bot token per agent; requires restart)
- Session management: One default session + one per custom-token agent
- Intents: `IntentsGuildMessages | IntentsDirectMessages | IntentsMessageContent`

**LLM API Keys:**
- OpenRouter: `[llm] openrouter_key` (required)
- GLM: `[llm] glm_key` (optional; gates web_search feature)
- Vision overrides: `[llm] vision_base_url` for custom vision endpoints (defaults to main chat endpoint)

## Monitoring & Observability

**Error Tracking:**
- None (no Sentry, Rollbar, or similar integration)
- Errors logged via `log/slog` to stderr

**Logs:**
- Standard streams: Text or JSON to stderr (configured via `--log-format` flag)
- Optional persistent logging: Stores logs in SQLite table `logs` (in `logs.db` alongside memory DB)
- Log levels: debug, info, warn, error (set via `--log-level` flag)
- Structured logging: `slog.Info()`, `slog.Warn()`, `slog.Error()` with key-value context

**Metrics:**
- None (no Prometheus, CloudWatch, or metrics export)

## CI/CD & Deployment

**Hosting:**
- Self-hosted (Docker container or bare Go binary)
- Docker image builds on `golang:1.24-bookworm` (builder) → `debian:bookworm-slim` (runtime)
- Multi-stage build with CGO enabled (`CGO_ENABLED=1`)

**CI Pipeline:**
- GitHub Actions (if configured; not examined in codebase)
- Deployment: Docker Compose or manual binary execution

**Deployment:**
- Docker: `docker-compose.yml` with volume mounts for `/config` and `/data`
- Graceful shutdown: 5-second timeout (SIGTERM/SIGINT handler in `main.go`)
- Goroutine drain: Router waits for in-flight agent operations before exiting

## Environment Configuration

**Required env vars:**
- None strictly required (can be set in config.toml)
- Optional: `VESPRA_CONFIG` — override config file path
- Optional: `VESPRA_DB_PATH` — override database path

**Secrets location:**
- `.env` file support: Not detected; secrets live in `config.toml` or environment
- Env var priority: Environment variables override config.toml values (see `config.Load()`)
- Security note: Do NOT commit config.toml with real tokens to git

## Webhooks & Callbacks

**Incoming:**
- Discord message events via gateway WebSocket (real-time)
- No HTTP webhooks consumed

**Outgoing:**
- Discord API calls to send messages, add reactions (via `bwmarrin/discordgo`)
- Web server exposes REST API at `[web] addr` (default `:8080`):
  - `GET/POST /api/config` — config read/write with hot-reload
  - `GET/DELETE/PATCH /api/memories` — memory management
  - `GET/POST/PUT/DELETE /api/agents` — agent CRUD
  - `GET/PUT /api/agents/{id}/soul`, `GET/PUT /api/soul` — soul file management
  - `GET /api/status`, `GET /api/events` (SSE) — live status stream
- No external webhooks triggered (all outputs are Discord messages or memory stores)

## Provider-Specific Details

**OpenRouter:**
- Base URL: `https://openrouter.ai/api/v1` (customizable)
- Endpoints used:
  - `POST /chat/completions` — chat completions with function calling
  - `POST /embeddings` — vector embeddings
- Retry logic: Up to 3 attempts with exponential backoff
- Retried on: 5xx errors, 429 (rate limit), timeouts
- Fails fast on: Other 4xx errors (auth, malformed requests)
- Timeout: `[llm] request_timeout_seconds` (default: depends on config)

**GLM (Z.AI):**
- Agentic endpoint: `https://api.z.ai/api/paas/v4` (per CLAUDE.md note — NOT the standard endpoint)
- Vision endpoint: Separate `vision_base_url` (defaults to main if not set)
- Native tools: GLM provides `web_search` as native tool (used for async search)
- Model reset: When provider="glm", model defaults to `glm-4.7` unless overridden
- Vision override: When vision content present and vision_model configured, uses vision endpoint/key

---

*Integration audit: 2026-02-26*
