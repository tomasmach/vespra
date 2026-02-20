# Mnemon-bot

A Discord bot written in Go that converses using AI and maintains persistent memory about users and topics — per server, with configurable personality and response behavior.

---

## Philosophy

Keep the codebase as small as possible. No extra boilerplate, no speculative abstractions, no unnecessary indirection. When in doubt about patterns or implementation decisions, refer to `../spacebot` as a reference.

---

## Goals

- AI-powered Discord conversations via OpenRouter
- Persistent memory per server (what was said, by whom, about what)
- Semantic memory recall using vector embeddings
- Configurable response behavior per server/channel
- Per-server personality via `soul.md` files
- Simple single-binary deployment, no daemon, no HTTP API

---

## Language & Stack

| Concern | Choice |
|---------|--------|
| Language | Go |
| Discord | `github.com/bwmarrin/discordgo` |
| LLM | OpenRouter (default), via HTTP |
| Database | SQLite via `github.com/mattn/go-sqlite3` |
| Vector search | Embeddings stored as blobs, cosine similarity in Go |
| Config | TOML via `github.com/BurntSushi/toml` |

---

## Project Structure

```
mnemon-bot/
├── main.go
├── config/        # TOML config loading and validation
├── bot/           # Discord gateway, event handling, message routing
├── agent/         # AI conversation loop, tool dispatch, channel goroutines
├── memory/        # SQLite store, embedding generation, hybrid search
├── tools/         # memory_save, memory_recall, memory_forget, reply, react, web_search
├── llm/           # OpenRouter HTTP client (chat completions + embeddings)
├── soul/          # soul.md loading → system prompt
└── migrations/    # SQL migration files
```

---

## Configuration

Single `config.toml` file, default path `~/.config/mnemon-bot/config.toml`.

Required fields (`bot.token`, `llm.openrouter_key`) are validated at startup. Missing or invalid required fields log a clear error and exit with a non-zero status code. Missing optional fields (e.g. `tools.web_search_key`) log a warning and disable the relevant feature.

```toml
[bot]
token = "..."
soul_file = "~/.config/mnemon-bot/soul.md"   # global fallback

[llm]
openrouter_key = "..."
model = "anthropic/claude-3.5-sonnet"
embedding_model = "openai/text-embedding-3-small"
request_timeout_seconds = 60   # HTTP timeout for all OpenRouter calls; timeouts are retried

[memory]
db_path = "~/.local/share/mnemon-bot/mnemon.db"

[agent]
history_limit = 20          # messages kept in-memory per channel
idle_timeout_minutes = 10   # goroutine shuts down after this idle period
max_tool_iterations = 10    # max tool-call cycles per turn before sending fallback reply

[response]
# Global default mode: "smart" | "mention" | "all" | "none"
default_mode = "smart"

# Per-server configuration (overrides global defaults)
[[servers]]
id = "123456789"
soul_file = "~/.config/mnemon-bot/souls/my-server.soul.md"
response_mode = "mention"

[[servers]]
id = "987654321"
# No soul_file — falls back to global soul.md
response_mode = "all"

[[servers.channels]]
id = "111222333"
response_mode = "none"   # silence bot in this channel only

[[servers.channels]]
id = "444555666"
response_mode = "mention"

[tools]
web_search_key = "..."   # optional; web_search tool disabled if absent
```

Response mode resolution order: per-channel → per-server → global default.

### Response Modes

| Mode | Behavior |
|------|----------|
| `smart` | AI decides whether the message warrants a response |
| `mention` | Only responds when @mentioned or in a DM |
| `all` | Responds to every message in configured channels |
| `none` | Bot is silent (useful for temporarily disabling) |

---

## soul.md

A markdown file written freely by the user. It becomes the system prompt prefix for all conversations on that server, defining the bot's name, personality, tone, and behavioral guidelines.

Example:
```markdown
You are Mnemon, a thoughtful and curious AI companion on this Discord server.
You remember everything people tell you and bring it up naturally in conversation.
You are warm but not sycophantic. You never pretend to know things you don't.
```

Resolution order: per-server `soul_file` → global `soul_file` → built-in default.

---

## Memory System

### Storage

SQLite database with two tables:

```sql
CREATE TABLE memories (
    id          TEXT PRIMARY KEY,
    content     TEXT NOT NULL,
    importance  REAL DEFAULT 0.5,
    server_id   TEXT NOT NULL,
    user_id     TEXT,               -- Discord user this memory is about (nullable)
    channel_id  TEXT,
    created_at  DATETIME NOT NULL,
    updated_at  DATETIME NOT NULL,
    forgotten   INTEGER DEFAULT 0
);

CREATE TABLE embeddings (
    memory_id   TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
    vector      BLOB NOT NULL       -- float32 array, little-endian serialized
);

CREATE INDEX idx_memories_server ON memories(server_id);
CREATE INDEX idx_memories_user   ON memories(server_id, user_id);
```

### Scoping

Memories are always scoped to a `server_id`. A bot instance running on multiple servers never mixes their memories. Within a server, memories can optionally be tagged to a specific `user_id`.

DMs have no real server — they use a synthetic `server_id` of `"DM:<user_id>"`, giving each user an isolated DM memory space. The global `soul.md` is used for DM conversations.

### Cleanup

Forgotten memories are soft-deleted only and remain in the DB indefinitely. Hard-deletion is a known limitation for MVP. A future `--vacuum` CLI flag could hard-delete forgotten rows and run SQLite's `VACUUM`.

### Capacity

No memory cap per server. SQLite handles large row counts without issue. The AI uses `memory_forget` to prune irrelevant memories. Unbounded growth is a known limitation for MVP.

### Importance

`importance` (0.0–1.0) is stored and returned in recall results so the AI can reason about it. It is not used in search ranking and does not decay. No automated importance management for MVP.

### Deduplication

No automatic deduplication in code. The system prompt instructs the AI to check existing memories via `memory_recall` before saving. Saving near-identical memories is a known limitation.

### Recall

Hybrid search combining two signals:

1. **Semantic** — cosine similarity between query embedding and stored embeddings
2. **Keyword** — SQLite `LIKE` match on `content`

Results from both sources are merged using **Reciprocal Rank Fusion (RRF)** with `k=60`: each source contributes `1/(60+rank)` per result; scores are summed when a memory appears in both. Results are sorted by fused score descending and top-N returned (default 10). Forgotten memories are excluded from all searches.

### Embeddings

Generated by calling the configured embedding model via OpenRouter/OpenAI at save time. Stored in the `embeddings` table as serialized `float32` arrays. Never regenerated unless memory content changes.

If embedding generation fails, the memory is saved without an embedding row and a warning is logged. The memory remains findable via keyword search only. No retry mechanism for MVP.

---

## Agent & Conversation Loop

One goroutine per active Discord channel. Goroutines are created on the first message that passes routing rules and shut down after `idle_timeout_minutes` of no activity.

Each channel goroutine has a buffered message channel (default size 100, configurable). The main Discord event handler routes incoming messages to the appropriate goroutine's channel. If the buffer is full, the message is dropped with a log warning.

Shutdown race: the router uses a non-blocking send when forwarding messages. If the send fails (goroutine has exited), the router removes the entry from its map and spawns a fresh goroutine for the message. No explicit done channel is needed — send failure is the shutdown signal.

**Per-turn flow:**

1. Receive Discord message
2. Evaluate response rules (mode + server/channel config)
3. If not responding, return
4. Recall relevant memories via hybrid search on message content
5. Build prompt: `soul.md` system prompt + recalled memories + recent channel history (last N messages)
6. Call OpenRouter with tool definitions
7. Execute any tool calls, feed results back into the loop
8. Repeat until the model produces a plain reply (no tool call)
9. Send reply to Discord

**Conversation history** is kept in-memory per goroutine. It is not persisted — only memories survive restarts.

No token budget is enforced. Discord's 2000-character message limit means 20 messages ≈ 40k characters, well within typical model context windows. Known limitation for servers with very long messages.

---

## Tools

| Tool | Description |
|------|-------------|
| `memory_save` | Save a fact or observation, tagged with user and importance score |
| `memory_recall` | Search memories by query string, returns top-N matches |
| `memory_forget` | Soft-delete a memory (excluded from future searches, stays in DB) |
| `reply` | Send a text message to the channel |
| `react` | Add an emoji reaction to a message |
| `web_search` | Search the web (disabled if no API key configured) |

---

## Deployment

### LLM Error Handling

Transient errors (5xx, timeouts, rate limits, connection errors) are retried up to 3 times with exponential backoff (500ms, 1000ms, 2000ms). Permanent errors (4xx auth/bad request) fail immediately without retry. On final failure, a brief error message is sent to the channel.

---

## Deployment

Single binary, no external services required beyond Discord and OpenRouter.

```
# Build
go build -o mnemon-bot .

# Run
MNEMON_CONFIG=./config.toml ./mnemon-bot
```

### Config Changes

Config is loaded once at startup. Changes to `config.toml` or `soul.md` files require a restart to take effect.

### Logging

Structured logging via Go's stdlib `log/slog`. Text handler by default, JSON via `--log-format=json`. Log level via `--log-level` flag (default `info`; options: `debug`, `info`, `warn`, `error`).

### Config path resolution order:
1. `MNEMON_CONFIG` environment variable
2. `--config` CLI flag
3. `~/.config/mnemon-bot/config.toml`

---

## Shutdown

On `SIGTERM`/`SIGINT`: stop the Discord gateway, signal all channel goroutines to drain via context cancellation, wait up to 30s for in-flight LLM calls to complete, then exit. New messages are not accepted during shutdown.

---

## Discord Behavior

- **Message edits** — ignored; the bot does not re-process edited messages
- **Message deletions** — ignored; orphaned history entries expire naturally with the goroutine
- **Threads** — each thread is treated as an independent channel with its own goroutine and history
- **Long replies** — responses exceeding Discord's 2000-character limit are split into sequential messages

---

## Out of Scope

The following are explicitly excluded to keep the project small:

- HTTP control API
- Daemon / process management (no start/stop/status commands)
- Cron / scheduled messages
- File ingestion
- Slack / Telegram / other platforms
- Web UI
- Multi-agent routing
