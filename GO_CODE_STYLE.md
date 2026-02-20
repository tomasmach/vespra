# Go Code Style Guide

Conventions for Mnemon-bot. Follow these exactly. When in doubt, consistency with existing code wins over personal preference.

## Project Structure

Single binary. No workspace, no sub-modules. Entry point is `main.go`.

```
.
├── main.go             — wires everything together: config → LLM → memory → bot → router
├── agent/
│   ├── agent.go        — per-channel conversation loop
│   └── router.go       — maps channel IDs to running agent goroutines
├── bot/
│   └── bot.go          — thin discordgo wrapper
├── config/
│   └── config.go       — TOML loading and validation
├── llm/
│   └── llm.go          — HTTP client for OpenRouter (chat + embeddings)
├── memory/
│   ├── store.go        — SQLite save/forget/load
│   ├── search.go       — hybrid cosine + LIKE search
│   └── rrf.go          — Reciprocal Rank Fusion
├── migrations/         — SQL migration files
├── soul/
│   └── soul.go         — personality prompt resolution
└── tools/
    └── tools.go        — Tool interface and registry
```

Prefer adding to existing files over creating new ones. New files only for new logical components.

## Imports

Three groups, separated by blank lines:

```go
import (
    // 1. Standard library (alphabetical)
    "context"
    "fmt"
    "log/slog"
    "net/http"

    // 2. Third-party packages (alphabetical by module path)
    "github.com/bwmarrin/discordgo"
    _ "github.com/mattn/go-sqlite3"

    // 3. Internal packages (alphabetical)
    "github.com/tomasmach/mnemon-bot/config"
    "github.com/tomasmach/mnemon-bot/llm"
)
```

Use blank imports (`_ "pkg"`) only for side-effect registration (e.g., database drivers). Place them in the third-party group.

## Naming

| Kind | Convention | Examples |
|------|-----------|----------|
| Packages | short, lowercase, no underscores | `agent`, `llm`, `memory` |
| Types | `PascalCase`, descriptive | `ChannelAgent`, `MemoryStore`, `ToolRegistry` |
| Interfaces | noun or noun phrase | `Tool`, `Store` |
| Functions (public) | `PascalCase`, verb-first for actions | `Route`, `Save`, `WaitForDrain` |
| Functions (private) | `camelCase`, verb-first for actions | `buildMessages`, `splitMessage` |
| Methods | same as functions | `(a *ChannelAgent) run(...)` |
| Variables | `camelCase`, full words | `channelID`, `serverID`, `httpClient` |
| Constants | `camelCase` (private) or `PascalCase` (public) | `maxRetries`, `DefaultMode` |
| Receiver names | short, one or two letters | `a` for Agent, `r` for Router, `s` for Store, `c` for Client |

Never abbreviate: `channelID` not `chID`, `serverID` not `sid`. Common short forms are fine when universally understood: `cfg`, `ctx`, `err`.

## Struct Definitions

Field ordering:

1. Identity (`id`, `channelID`, `serverID`)
2. Dependencies and shared resources (`cfg`, `llm`, `mem`, `session`)
3. State and data (`history`, `soulText`)
4. Channels (always last: `msgCh`)

```go
type ChannelAgent struct {
    channelID string
    serverID  string

    cfg     *config.Config
    llm     *llm.Client
    mem     *memory.Store
    session *discordgo.Session

    soulText string
    history  []llm.Message

    msgCh chan *discordgo.MessageCreate
}
```

Fields are private by default. Only export what forms the package's public API.

## Constructors

Name constructors `New` or `new` (private). Return a pointer. Set defaults explicitly.

```go
func newChannelAgent(channelID, serverID string, cfg *config.Config, llmClient *llm.Client, mem *memory.Store, session *discordgo.Session, soulText string) *ChannelAgent {
    return &ChannelAgent{
        channelID: channelID,
        serverID:  serverID,
        cfg:       cfg,
        llm:       llmClient,
        mem:       mem,
        session:   session,
        soulText:  soulText,
        msgCh:     make(chan *discordgo.MessageCreate, 100),
    }
}
```

## Error Handling

**Wrap errors with context using `%w`:**
```go
if err := os.MkdirAll(dir, 0o755); err != nil {
    return nil, fmt.Errorf("create db dir: %w", err)
}
```

Error messages follow the pattern `"<verb> <noun>: <detail>"`. No capital letters, no trailing punctuation.

**Return early on error:**
```go
rows, err := s.db.QueryContext(ctx, query, args...)
if err != nil {
    return nil, fmt.Errorf("query memories: %w", err)
}
defer rows.Close()
```

**Log and continue for non-fatal failures:**
```go
vec, err := s.llm.Embed(ctx, content)
if err != nil {
    slog.Warn("embed failed, skipping embedding", "error", err)
    return id, nil
}
```

**Never ignore errors with `_`** unless the value is explicitly not needed (e.g., `defer rows.Close()`). Always handle or log.

**Validation errors** follow `"<field> <problem>"` format:
```go
return nil, fmt.Errorf("response.default_mode %q is invalid (use smart|mention|all|none)", mode)
```

## Logging

Use `log/slog` for all logging. Always use structured key-value pairs.

```go
slog.Info("config loaded", "path", cfgPath)
slog.Warn("embed failed, skipping embedding", "error", err)
slog.Error("failed to open memory store", "error", err)
```

Log levels:
- `error` — something broke; attention required
- `warn` — a failure that the system can tolerate (background task failed, buffer full)
- `info` — significant lifecycle events (startup, shutdown, config loaded)
- `debug` — operational detail (tool calls, per-message state)

Always include `"error", err` as the first key when logging an error. Include identifying context (`"channel_id"`, `"server_id"`) where relevant.

```go
slog.Error("llm chat error", "error", err, "channel_id", a.channelID)
slog.Warn("memory recall error", "error", err, "channel_id", a.channelID)
slog.Info("channel agent idle timeout", "channel_id", a.channelID)
```

## Context

Pass `ctx context.Context` as the first parameter to any function that does I/O.

```go
func (s *Store) Save(ctx context.Context, content string, ...) (string, error)
func (c *Client) Chat(ctx context.Context, msgs []Message, ...) (*Response, error)
```

Use `context.WithTimeout` or `context.WithCancel` to bound operations. Propagate cancellation through goroutines via `ctx.Done()`.

```go
select {
case msg := <-a.msgCh:
    a.handleMessage(ctx, msg)
case <-time.After(idleTimeout):
    return
case <-ctx.Done():
    // drain remaining messages then exit
}
```

## Goroutines

**Track goroutines with `sync.WaitGroup`:**
```go
r.wg.Add(1)
go func() {
    defer r.wg.Done()
    a.run(r.ctx)
    // cleanup after goroutine exits
    r.mu.Lock()
    delete(r.agents, channelID)
    r.mu.Unlock()
}()
```

**Protect shared state with `sync.Mutex`:**
```go
r.mu.Lock()
defer r.mu.Unlock()
agent, ok := r.agents[channelID]
```

**Graceful shutdown with timeout:**
```go
func (r *Router) WaitForDrain() {
    done := make(chan struct{})
    go func() {
        r.wg.Wait()
        close(done)
    }()
    select {
    case <-done:
    case <-time.After(30 * time.Second):
        slog.Warn("drain timeout: some agents did not finish within 30s")
    }
}
```

**Buffered channels** for agent mailboxes. Size them generously to absorb bursts without blocking:
```go
msgCh: make(chan *discordgo.MessageCreate, 100),
```

**Non-blocking send** to detect a full or gone channel:
```go
select {
case agent.msgCh <- msg:
    return
default:
    // buffer full or agent gone — respawn
    slog.Warn("agent buffer full or gone, respawning", "channel_id", channelID)
    delete(r.agents, channelID)
}
```

## Interfaces

Define interfaces where the caller needs to vary the implementation or for testability. Keep them small — one to three methods is ideal.

```go
type Tool interface {
    Name() string
    Description() string
    Parameters() json.RawMessage
    Call(ctx context.Context, args json.RawMessage) (string, error)
}
```

Implement interfaces with private structs and a public registration function:

```go
type memorySaveTool struct {
    store    *memory.Store
    serverID string
}

func (t *memorySaveTool) Name() string { return "memory_save" }
```

## Inline Structs for One-Off JSON

Use anonymous structs for decoding JSON arguments that don't need a named type:

```go
var p struct {
    Content    string  `json:"content"`
    Importance float64 `json:"importance"`
}
if err := json.Unmarshal(args, &p); err != nil {
    return "", err
}
```

## Comments

Comments explain **why**, not **what**. Avoid summarizing what the code already says.

**Package-level doc comments** (`// Package ...`) on every package:
```go
// Package memory provides SQLite-backed persistent memory storage and hybrid search.
package memory
```

**Exported symbols** get doc comments:
```go
// Route delivers a message to the appropriate channel agent, spawning one if needed.
func (r *Router) Route(msg *discordgo.MessageCreate) {
```

**Inline comments** for non-obvious behavior:
```go
// guaranteed to succeed — buffer just created, size 100
a.msgCh <- msg
```

No section-divider comments. No comments on removed code. Write comments as timeless documentation, not changelog entries.

## SQL

Use raw string literals for SQL. Align columns for readability:

```go
_, err = s.db.ExecContext(ctx, `
    INSERT INTO memories (id, content, importance, server_id, user_id, channel_id, created_at, updated_at, forgotten)
    VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
    id, content, importance, serverID, userID, channelID, now, now,
)
```

Always use `ExecContext` / `QueryContext` — never the non-context variants. Always `defer rows.Close()` immediately after a successful `QueryContext`.

## Configuration Defaults

Set defaults after loading, not in struct tags. Group them clearly:

```go
if cfg.Agent.HistoryLimit == 0 {
    cfg.Agent.HistoryLimit = 20
}
if cfg.Agent.IdleTimeoutMinutes == 0 {
    cfg.Agent.IdleTimeoutMinutes = 10
}
```

Validate using a map for discrete values:

```go
validModes := map[string]bool{"smart": true, "mention": true, "all": true, "none": true}
if !validModes[cfg.Response.DefaultMode] {
    return nil, fmt.Errorf("response.default_mode %q is invalid (use smart|mention|all|none)", cfg.Response.DefaultMode)
}
```

## Closures for Dependency Injection

Pass behavior as `func` values rather than creating new interfaces when there's only one call site:

```go
sendFn := func(content string) error {
    _, err := a.session.ChannelMessageSend(msg.ChannelID, content)
    return err
}
reactFn := func(emoji string) error {
    return a.session.MessageReactionAdd(msg.ChannelID, msg.ID, emoji)
}
reg := tools.NewDefaultRegistry(a.mem, a.serverID, sendFn, reactFn)
```

## String Building

Use `strings.Builder` for multi-line output assembly:

```go
var sb strings.Builder
for _, r := range rows {
    fmt.Fprintf(&sb, "[%s] (importance: %.1f) %s\n", r.ID, r.Importance, r.Content)
}
return sb.String(), nil
```

## Retry Logic

Use a slice of delays to drive retry loops. Fail fast on non-transient errors:

```go
var retryDelays = []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond}

for attempt, delay := range retryDelays {
    resp, err := c.httpClient.Do(req)
    if err != nil {
        time.Sleep(delay)
        continue // network errors are always transient
    }
    if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
        resp.Body.Close()
        time.Sleep(delay)
        continue
    }
    if resp.StatusCode != http.StatusOK {
        resp.Body.Close()
        return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
    }
    // success
}
```

## Panics

Don't panic in production code. Use `log/slog` + `os.Exit(1)` at startup for unrecoverable errors. Prefer returning errors over panicking everywhere else.

`.unwrap()`-equivalent panics (immediate `log.Fatal`) are acceptable at startup only:
```go
cfg, err := config.Load(cfgPath)
if err != nil {
    slog.Error("failed to load config", "error", err)
    os.Exit(1)
}
```

## Testing

Test files live alongside the code they test (`store_test.go` next to `store.go`). Use `_test` package suffix for black-box tests.

```go
func TestMemoryRoundtrip(t *testing.T) {
    store := openTestStore(t)
    id, err := store.Save(context.Background(), "test fact", ...)
    if err != nil {
        t.Fatal(err)
    }
    rows, err := store.Recall(context.Background(), "test", ...)
    // ...
}
```

Use `t.Fatal` / `t.Fatalf` for setup failures. Use `t.Error` / `t.Errorf` for assertion failures that allow the test to continue.
