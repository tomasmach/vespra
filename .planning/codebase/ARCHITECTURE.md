# Architecture

**Analysis Date:** 2026-02-26

## Pattern Overview

**Overall:** Event-driven message processor with per-channel goroutines and tiered persistence.

**Key Characteristics:**
- One goroutine per active Discord channel (lazy-initialized on first message)
- Synchronous tool-call loop with async background tasks (memory extraction, web search)
- SQLite-backed persistent memory with hybrid search (embeddings + LIKE)
- Hot-configurable agents without restart (except those with custom Discord tokens)
- Graceful shutdown with 30-second drain timeout for in-flight processing

## Layers

**Discord Gateway (Bot):**
- Purpose: Listen to Discord events and route incoming messages
- Location: `bot/bot.go`
- Contains: Gateway session management via discordgo, message filtering (ignores self/bot messages)
- Depends on: discordgo, agent.Router
- Used by: main.go for startup/shutdown

**Message Router (Agent):**
- Purpose: Maintain a registry of per-channel agents and route messages to the correct one
- Location: `agent/router.go`
- Contains: Per-channel ChannelAgent lifecycle management, spam detection, hot-loading of configured agents
- Depends on: config.Store, llm.Client, memory.Store
- Used by: bot.Bot, web.Server for status queries, main.go for initialization

**Channel Agent (Agent):**
- Purpose: Per-channel conversation loop; orchestrates LLM calls with tool execution
- Location: `agent/agent.go`
- Contains: Message buffering with debounce/deadline coalescing, history management, tool dispatch, memory extraction scheduling
- Depends on: config.Store, llm.Client, memory.Store, tools.Registry, soul.Load
- Used by: agent.Router

**Configuration:**
- Purpose: Load and cache TOML config; support hot-reload and per-channel/per-agent overrides
- Location: `config/config.go`
- Contains: Config struct definitions, path resolution, validation, thread-safe Store wrapper
- Depends on: BurntSushi/toml
- Used by: main.go, all subsystems for settings

**Memory Store:**
- Purpose: Persist facts and save conversation history; provide hybrid search
- Location: `memory/store.go`, `memory/search.go`, `memory/rrf.go`
- Contains: SQLite schema, vector embeddings (float32 blobs), soft-delete, conversation logging
- Depends on: llm.Client for embeddings, go-sqlite3
- Used by: ChannelAgent for recall/save, agent.Router for DM-specific memory

**LLM Client:**
- Purpose: HTTP abstraction for chat completions and embeddings across OpenRouter and Z.AI (GLM)
- Location: `llm/llm.go`
- Contains: Request/response marshaling (vision-aware), provider routing, retry logic (3× with backoff)
- Depends on: config.Store for keys and model selection
- Used by: ChannelAgent for chat turns and memory extraction, memory.Store for embeddings

**Tool Registry:**
- Purpose: Define available AI tools and dispatch tool calls
- Location: `tools/tools.go`, `tools/web_fetch.go`, and individual tool files
- Contains: Tool interface, built-in tools (memory_save, memory_recall, memory_forget, reply, react, web_search)
- Depends on: memory.Store, http.Client (for web tools)
- Used by: ChannelAgent for processTurn, tool-call loop

**Web Management UI:**
- Purpose: REST API and embedded static HTML for configuration, memory browsing, agent control
- Location: `web/server.go`, `web/static/`
- Contains: HTTP handlers for config CRUD, memory CRUD, agent lifecycle, soul management, SSE status streaming
- Depends on: config.Store, agent.Router, memory.Store, logstore.Store
- Used by: main.go for startup

**Soul (Personality):**
- Purpose: Load system prompt for the bot personality
- Location: `soul/soul.go`
- Contains: 3-tier resolution (agent-specific soul file → global soul file → built-in default)
- Depends on: config.Config
- Used by: ChannelAgent for system prompt building

**Logging:**
- Purpose: Centralized structured logging with SQLite persistence
- Location: `logstore/logstore.go`
- Contains: slog-based handler that writes to stderr and SQLite, conversation history logging
- Depends on: go-sqlite3
- Used by: main.go for logger setup, ChannelAgent for conversation logging

## Data Flow

**Incoming Discord Message:**

1. Discord gateway delivers event to `bot.Bot.onMessageCreate`
2. Bot filters self/bot messages, forwards to `router.Route(msg)`
3. Router checks server configuration, spam limits, lookup/spawn ChannelAgent
4. Agent receives msg on buffered channel (`msgCh`, size 100)
5. Agent waits for debounce window or deadline, then coalesces messages
6. For each turn: backfill history, recall memories, build system prompt, dispatch to LLM
7. LLM response may include tool calls; loop until plain text received
8. Send reply via `tools.reply` tool or plain-text send, update history, trigger async memory extraction
9. Web SSE subscribers notified of agent status changes

**Memory Save/Recall:**

1. `ChannelAgent.processTurn` calls LLM with tool definitions
2. LLM may invoke `memory_save(content, user_id, importance)` or `memory_recall(query, top_n)`
3. Tool dispatch → `memory.Store.Save` (generates embedding via LLM, inserts to SQLite)
4. Recall uses hybrid search: cosine similarity on embeddings + SQLite LIKE, merged via RRF
5. Memory scoped by `server_id` (DMs use `"DM:<user_id>"`)

**Configuration Hot-Reload:**

1. Web UI POST to `/api/config` with new TOML
2. Server validates, writes temp file, renames to replace original
3. `config.Store.Set()` updates in-memory cache (thread-safe RWMutex)
4. Running agents see new config on next `cfgStore.Get()` call
5. Newly added agents without custom tokens are hot-loaded on first message
6. Custom-token agents require restart to open new Discord session

**Graceful Shutdown:**

1. SIGTERM/SIGINT → `router.WaitForDrain()` with 30-second timeout
2. Each active ChannelAgent finishes current turn, waits for extraction/search goroutines
3. Message channel is drained during shutdown context (30-second deadline)
4. Bot sessions closed, web server stopped, log store closed

## Key Abstractions

**ChannelAgent:**
- Purpose: Encapsulate per-channel conversation state and turn processing
- Examples: `agent/agent.go` (struct), spawned per unique Discord channel ID in `agent/router.go`
- Pattern: Long-lived goroutine with buffered message channel, local history, idle timeout

**Router:**
- Purpose: Map channel IDs to active agents; handle server-level config and resource management
- Examples: `agent/router.go` (Router struct)
- Pattern: Mutex-protected map, lazy initialization, hot-load support for config changes

**Memory.Store:**
- Purpose: Abstraction over SQLite for memory operations
- Examples: `memory/store.go` (Store struct with methods Save, Recall, Forget, LogConversation)
- Pattern: Single DB instance per agent, transaction-based consistency, WAL mode for concurrency

**Tool.Registry:**
- Purpose: Pluggable tool system for LLM function calls
- Examples: `tools/tools.go` (Registry, Tool interface)
- Pattern: Tool interface implementation (Name, Description, Parameters, Call), dispatch via reflection

**Config.Store:**
- Purpose: Thread-safe, hot-reloadable configuration
- Examples: `config/config.go` (Store struct with Get/Set methods)
- Pattern: RWMutex protecting in-memory cache, lazy-reloaded on every Get()

## Entry Points

**main.go:**
- Location: `main.go`
- Triggers: Program startup (go build output or docker run)
- Responsibilities: Wire dependencies (config → LLM → memory stores → bot → router → web), signal handling, graceful shutdown

**bot.Bot.onMessageCreate:**
- Location: `bot/bot.go` (event handler)
- Triggers: Every non-self Discord message across all servers/DMs
- Responsibilities: Filter bots and self, dispatch to router

**router.Route:**
- Location: `agent/router.go`
- Triggers: Bot forwards a message
- Responsibilities: Lookup/spawn ChannelAgent, check spam, send to agent's message channel

**ChannelAgent.run:**
- Location: `agent/agent.go`
- Triggers: Router spawns new agent goroutine
- Responsibilities: Message buffering loop, idle timeout, coalescing, handleMessage dispatch

**ChannelAgent.processTurn:**
- Location: `agent/agent.go`
- Triggers: handleMessage/handleMessages after preparing inputs
- Responsibilities: LLM chat loop with tool dispatch, history update, memory extraction trigger

**web.Server handlers:**
- Location: `web/server.go`
- Triggers: HTTP requests to `/api/*` endpoints
- Responsibilities: Config CRUD, memory management, agent control, status streaming

## Error Handling

**Strategy:** Structured logging with graceful degradation; errors logged but don't crash the agent.

**Patterns:**
- LLM errors: Log, send "I encountered an error" reply to channel, return from turn
- Memory errors: Log warning, continue (missing embeddings are acceptable)
- Tool dispatch errors: Log, return error string to LLM as tool result
- Discord send errors: Log, don't retry (message may be too long or channel may be deleted)
- Config validation: Early exit on startup; web API rejects invalid TOML before applying

## Cross-Cutting Concerns

**Logging:**
- Central slog setup in main.go with optional SQLite persistence
- All packages use `slog.Info/Warn/Error` with structured fields
- ChannelAgent logs include `server_id` and `channel_id` via context logger

**Validation:**
- Config validation in `config.Load()` before returning to caller
- Web API validates config before writing and applying
- Tool dispatch validates arguments via JSON unmarshaling

**Authentication:**
- Discord token from config, passed to discordgo
- LLM API keys (OpenRouter, GLM) from config, never logged
- Web server has no built-in auth (assumed running on internal network or behind proxy)

**Message Size Limits:**
- Discord 2000-char limit enforced in `tools.SplitMessage`
- Video attachments capped at 50MB before download attempt
- Image/video fetched as base64 data URLs for vision models

**Concurrency:**
- One goroutine per active channel (async by default)
- Tool-call loop is synchronous within a turn (no concurrent LLM calls per channel)
- Memory extraction and web search run in background (async, tracked via WaitGroup)
- Config updates use RWMutex for thread-safe reads
- Web server broadcasts SSE status to multiple subscribers
