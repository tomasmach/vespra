# Codebase Structure

**Analysis Date:** 2026-02-26

## Directory Layout

```
vespra/
├── agent/               # Message routing and per-channel conversation loops
├── bot/                 # Discord gateway wrapper
├── config/              # TOML config loading and thread-safe caching
├── docs/                # Documentation (not code)
├── llm/                 # OpenRouter/GLM HTTP client for chat and embeddings
├── logstore/            # SQLite-backed structured logging
├── memory/              # SQLite memory store with hybrid search
├── migrations/          # SQL migration files (referenced but embedded in code)
├── soul/                # Personality system prompt loading
├── tools/               # AI tool implementations and registry
├── web/                 # REST API and embedded static HTML UI
├── main.go              # Entry point; wires all components
├── go.mod              # Go module definition
├── go.sum              # Go dependency checksums
├── config.toml         # Example configuration (not production)
├── CLAUDE.md           # Project-specific Claude Code instructions
├── GO_CODE_STYLE.md    # Go naming/error/logging conventions
├── README.md           # Project overview
├── Dockerfile          # Container image for deployment
├── docker-compose.yml  # Local deployment configuration
└── .planning/          # GSD planning documents (generated)
```

## Directory Purposes

**agent/:**
- Purpose: Router for incoming messages, per-channel agent lifecycle, turn processing
- Contains: Go files for router, agent structs, turn logic, message formatting, tool execution
- Key files: `router.go` (message routing, spam detection), `agent.go` (turn loop, history, LLM dispatch)

**bot/:**
- Purpose: Thin wrapper around Discord gateway (discordgo)
- Contains: Bot struct, session management, message event handler
- Key files: `bot.go` (session lifecycle, onMessageCreate handler)

**config/:**
- Purpose: Configuration structure, loading, validation, hot-reload support
- Contains: Config structs, path resolution, thread-safe Store wrapper
- Key files: `config.go` (definitions, validation, hot-reload logic)

**llm/:**
- Purpose: HTTP client for LLM APIs (OpenRouter, Z.AI/GLM)
- Contains: Message/tool marshaling, provider routing, retry logic
- Key files: `llm.go` (client, chat completions), vision and embedding support files

**memory/:**
- Purpose: SQLite-backed persistent memory with embeddings and search
- Contains: Database schema, CRUD operations, hybrid search (vector + LIKE), RRF ranking
- Key files: `store.go` (Save/Recall/Forget/LogConversation), `search.go` (hybrid search), `rrf.go` (ranking)

**tools/:**
- Purpose: AI tool implementations and registry
- Contains: Tool interface, built-in tools (memory, reply, react, web_search, web_fetch)
- Key files: `tools.go` (registry, dispatch), individual tool files

**web/:**
- Purpose: Embedded HTTP REST API and static UI for configuration and memory management
- Contains: HTTP handlers, static files (HTML/JS/CSS)
- Key files: `server.go` (handlers, routing), `static/index.html` (UI), `static/app.js` (frontend logic)

**logstore/:**
- Purpose: Structured logging with SQLite persistence
- Contains: slog handler that writes to both stderr and SQLite
- Key files: `logstore.go` (handler implementation)

**soul/:**
- Purpose: Load and resolve personality system prompt
- Contains: Resolution logic (agent-specific → global → built-in default)
- Key files: `soul.go` (Load function, default prompt)

## Key File Locations

**Entry Points:**
- `main.go`: Program entry point; initializes all subsystems (config, LLM, memory, bot, router, web)

**Configuration:**
- `config/config.go`: Config struct definitions and loading
- `config.toml`: Example configuration (not production; see CLAUDE.md for run instructions)

**Core Logic:**
- `agent/router.go`: Message routing, channel-agent mapping, hot-loading
- `agent/agent.go`: Per-channel turn loop, LLM dispatch, tool execution
- `memory/store.go`: Memory persistence and recall

**Testing:**
- `*_test.go` files colocated with implementation (e.g., `agent/agent_test.go`, `config/config_test.go`)
- Test infrastructure includes mocking HTTP clients and in-memory SQLite instances

**Utilities:**
- `tools/tools.go`: Tool registry, dispatch, shared helper functions (SplitMessage)
- `soul/soul.go`: Soul file loading and resolution

## Naming Conventions

**Files:**
- Implementation: `package_name.go` (e.g., `router.go`, `agent.go`, `store.go`)
- Tests: `{package}_test.go` (e.g., `router_test.go`)
- Internal tests (whitebox): `export_test.go` when accessing unexported types
- Packages are lowercase single-word or underscore-separated (e.g., `logstore`, `web`)

**Functions:**
- Exported functions: PascalCase (e.g., `New`, `Load`, `Recall`)
- Unexported functions: camelCase (e.g., `newChannelAgent`, `sanitizeHistory`)
- Interface implementations: method name follows interface exactly

**Types:**
- Struct names: PascalCase (e.g., `ChannelAgent`, `Router`, `Store`)
- Interface names: PascalCase (e.g., `Tool`)
- Errors: Named constants with `Err` prefix or wrapped with `fmt.Errorf`

**Variables:**
- Package-level constants: UPPER_SNAKE_CASE (e.g., `spamWindow`, `spamThreshold`, `maxVideoBytes`)
- Local variables: camelCase
- Abbreviations acceptable when clear from context (e.g., `msg`, `cfg`, `ctx`)

**Directories:**
- All lowercase, no underscores (except `web/static` for embedded files)
- One responsibility per directory (cohesive package)

## Where to Add New Code

**New Feature (e.g., new tool):**
- Primary code: `tools/{tool_name}.go` implementing the `Tool` interface
- Registration: Add to `tools.NewDefaultRegistry()` in `tools/tools.go`
- Tests: `tools/{tool_name}_test.go`

**New Subsystem (e.g., new storage backend):**
- Implementation: New package directory (e.g., `cache/`, `metrics/`)
- Entry point: `main.go` initialization
- Tests: Colocated in same package

**Utilities and Helpers:**
- Shared helpers within a package: Add as unexported functions in existing files
- Shared across packages: Consider creating a new small package (e.g., `util/`) or add to `tools/` if tool-related

**Configuration and Constants:**
- Feature flags or tunable parameters: Add to `config/config.go` struct, set defaults in `Load()`
- Magic numbers (timeouts, limits): Define as package-level const in the file where used (e.g., `spamWindow` in `agent/router.go`)

**Web API Endpoints:**
- Handlers: Add method to `Server` type in `web/server.go`
- Register: Add `mux.HandleFunc()` call in `web.New()`
- Static UI: Update `web/static/app.js` for frontend logic

## Special Directories

**web/static/:**
- Purpose: Embedded static files served as SPA (Single Page Application)
- Generated: No (hand-written)
- Committed: Yes
- Files: `index.html` (UI template), `app.js` (frontend logic), `style.css` (styling)
- Build: Files are embedded in binary via `//go:embed static`

**migrations/:**
- Purpose: SQL migration files (documented but embedded in `memory/store.go`)
- Generated: No
- Committed: Yes
- Note: Currently a single migration defined inline; directory exists for future expansion

**docs/:**
- Purpose: Documentation and planning files (not code)
- Generated: Partially (GSD plans go in `docs/plans/`)
- Committed: Yes

**.planning/codebase/:**
- Purpose: GSD codebase analysis documents (ARCHITECTURE.md, STRUCTURE.md, etc.)
- Generated: Yes (by GSD agents)
- Committed: Yes

---

**Code Organization Summary:**

The codebase follows a layered architecture with clear separation of concerns:
1. **Bottom layers**: SQLite storage (memory, logstore)
2. **Middle layers**: HTTP clients (LLM), message routing (bot, agent router)
3. **Top layers**: Per-channel logic (ChannelAgent), REST API (web server)

Each package is independently testable. Tests use table-driven patterns where appropriate. No circular dependencies between packages.
