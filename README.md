<h1 align="center">Vespra</h1>

<p align="center">
  <strong>A Discord AI companion with persistent memory.</strong>
</p>

<p align="center">
  <img src="https://img.shields.io/badge/language-Go-00ADD8" />
</p>

<p align="center">
  <a href="#how-it-works">How It Works</a> •
  <a href="#quick-start">Quick Start</a> •
  <a href="#architecture">Architecture</a> •
  <a href="#tech-stack">Tech Stack</a>
</p>

---

## How It Works

Vespra runs one goroutine per active Discord channel. When a message arrives, the bot checks the configured response mode, recalls relevant memories via hybrid search, builds a system prompt from the agent's soul file and recalled memories, then calls an LLM through OpenRouter. The LLM can invoke tools — saving or recalling memories, reacting to messages, searching the web — in a loop until it produces a plain text reply.

Conversation history lives in memory per channel goroutine and is not persisted across restarts. Memories are different: they survive restarts, are scoped per server, and are stored in SQLite with vector embeddings for semantic recall. Each server gets its own database file and can have its own personality via a `soul.md` file.

DMs are handled automatically by the default bot using a synthetic server ID (`DM:<user_id>`), giving each user an isolated memory space without any extra config.

---

## Quick Start

### Prerequisites

- **Go** 1.21+ with CGO enabled (a C compiler is required for `go-sqlite3`)
- A Discord bot token
- An [OpenRouter](https://openrouter.ai) API key

### Build

```bash
git clone https://github.com/tomasmach/vespra
cd vespra
go build -o vespra .
```

### Minimal Config

Create `config.toml`:

```toml
[bot]
token = "your-discord-bot-token"

[llm]
openrouter_key = "your-openrouter-key"
model = "anthropic/claude-3.5-sonnet"
embedding_model = "openai/text-embedding-3-small"

[memory]
db_path = "~/.local/share/vespra/vespra.db"

[response]
default_mode = "mention"
```

### Run

```bash
# Config path resolution: VESPRA_CONFIG env → --config flag → ~/.config/vespra/config.toml
./vespra --config ./config.toml

# With debug logging
./vespra --config ./config.toml --log-level debug --log-format json
```

---

## Architecture

```
vespra/
├── main.go             — wires everything together: config → LLM → memory → bot → router → web
├── agent/
│   ├── agent.go        — per-channel conversation loop
│   └── router.go       — maps channel IDs to running agent goroutines
├── bot/
│   └── bot.go          — thin discordgo wrapper
├── config/
│   └── config.go       — TOML loading, validation, thread-safe Store for hot-reload
├── llm/
│   └── llm.go          — HTTP client for OpenRouter (chat + embeddings)
├── memory/
│   ├── store.go        — SQLite save/forget/load
│   ├── search.go       — hybrid cosine + LIKE search
│   └── rrf.go          — Reciprocal Rank Fusion
├── migrations/         — SQL migration files
├── soul/
│   └── soul.go         — personality prompt resolution
├── tools/
│   └── tools.go        — Tool interface and registry
└── web/                — embedded HTTP management UI
```

| Package | Responsibility |
|---------|---------------|
| `agent` | Per-channel goroutines; conversation loop; tool dispatch |
| `bot` | Discord gateway; ignores self/bot messages; routes to agent router |
| `config` | TOML loading; thread-safe hot-reload; response mode resolution |
| `llm` | OpenRouter HTTP client; retry logic (3 attempts, exponential backoff) |
| `memory` | SQLite store; hybrid search; RRF merging; WAL mode |
| `soul` | Soul file resolution: per-agent → global → built-in default |
| `tools` | `memory_save`, `memory_recall`, `memory_forget`, `reply`, `react`, `web_search` |
| `web` | Embedded management UI; REST API for config, memories, agents, soul, status |

---

## Memory

Memories are stored in SQLite with vector embeddings for semantic recall. Each agent gets its own database file.

**Schema:**

```sql
CREATE TABLE memories (
    id          TEXT PRIMARY KEY,
    content     TEXT NOT NULL,
    importance  REAL DEFAULT 0.5,
    server_id   TEXT NOT NULL,
    user_id     TEXT,
    channel_id  TEXT,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    forgotten   INTEGER DEFAULT 0
);

CREATE TABLE embeddings (
    memory_id   TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
    vector      BLOB NOT NULL   -- float32 array, little-endian
);
```

**Scoping:** Memories are scoped to `server_id`. DMs use `"DM:<user_id>"` as a synthetic server ID. Servers never share memories.

**Recall:** Hybrid search combining cosine similarity on embeddings and SQLite `LIKE` on content, merged via Reciprocal Rank Fusion (`k=60`). Forgotten memories are excluded.

**Soft-delete:** `memory_forget` sets `forgotten=1`. Memories remain in the database indefinitely.

---

## Tools

| Tool | Description |
|------|-------------|
| `memory_save` | Save a fact or observation, tagged with user and importance score |
| `memory_recall` | Search memories by query string, returns top-N matches |
| `memory_forget` | Soft-delete a memory (excluded from future searches) |
| `reply` | Send a text message to the channel |
| `react` | Add an emoji reaction to a message |
| `web_search` | Search the web (disabled if no `tools.web_search_key` configured) |

---

## Configuration

Full `config.toml` reference:

```toml
[bot]
token = "..."                                    # default bot token (required)
soul_file = "~/.config/vespra/soul.md"       # global personality fallback

[llm]
openrouter_key = "..."
model = "anthropic/claude-3.5-sonnet"
embedding_model = "openai/text-embedding-3-small"
request_timeout_seconds = 60

[memory]
db_path = "~/.local/share/vespra/vespra.db"  # default DB; agents can override

[agent]
history_limit = 20          # messages kept in-memory per channel
idle_timeout_minutes = 10   # goroutine shuts down after this idle period
max_tool_iterations = 10    # max tool-call cycles per turn

[response]
default_mode = "smart"      # smart | mention | all | none

[tools]
web_search_key = "..."      # optional; web_search disabled if absent

[web]
addr = ":8080"              # management UI address (default :8080)

# Per-server agents (optional; multiple allowed)
[[agents]]
server_id = "123456789"
soul_file = "~/.config/vespra/souls/my-server.md"
response_mode = "mention"
db_path = "~/.local/share/vespra/my-server.db"   # optional

[[agents.channels]]
channel_id = "111222333"
response_mode = "none"      # silence bot in this channel

[[agents]]
server_id = "987654321"
response_mode = "all"
token = "..."               # custom bot token (requires restart to apply)
```

**Response mode resolution:** channel override → agent override → global default.

| Mode | Behavior |
|------|----------|
| `smart` | AI decides whether the message warrants a response |
| `mention` | Only responds when @mentioned or in a DM |
| `all` | Responds to every message |
| `none` | Bot is silent (useful for temporarily disabling) |

### soul.md

A markdown file defining the bot's name, personality, and tone. It becomes the system prompt prefix for all conversations on that server.

```markdown
You are Vespra, a thoughtful and curious AI companion on this Discord server.
You remember everything people tell you and bring it up naturally in conversation.
You are warm but not sycophantic. You never pretend to know things you don't.
```

Resolution order: per-agent `soul_file` → global `soul_file` → built-in default.

---

## Web UI

Vespra ships an embedded HTTP management UI accessible at `http://localhost:8080` by default. It provides:

- **Config editor** — read and write the raw TOML config; changes are validated before applying and hot-reloaded without restart
- **Memory browser** — browse, search, edit, and delete memories by server
- **Agent manager** — CRUD for `[[agents]]` config entries; view live agent status
- **Soul editor** — read and write soul files per agent or globally
- **Live status** — SSE stream of agent activity

---

## Tech Stack

| Layer | Technology |
|-------|-----------|
| Language | **Go** |
| Discord | `github.com/bwmarrin/discordgo` |
| LLM | OpenRouter (chat completions + embeddings) via HTTP |
| Database | SQLite via `github.com/mattn/go-sqlite3` (CGO) |
| Vector search | Cosine similarity in Go; embeddings as float32 blobs in SQLite |
| Config | TOML via `github.com/BurntSushi/toml` |
| Web | Embedded static UI; stdlib `net/http` |

---

## Contributing

Read [CONTRIBUTING.md](./CONTRIBUTING.md) before sending a PR, and [GO_CODE_STYLE.md](./GO_CODE_STYLE.md) before writing any code.
