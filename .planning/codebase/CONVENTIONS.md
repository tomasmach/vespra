# Coding Conventions

**Analysis Date:** 2026-02-26

## Naming Patterns

**Files:**
- Lowercase, single word: `agent.go`, `memory.go`, `router.go`, `store.go`
- Test files use `_test.go` suffix: `agent_test.go`, `store_test.go`
- Export files use `export_test.go` pattern for test helpers that access unexported symbols: `llm/export_test.go`

**Functions:**
- Public: `PascalCase`, action-first: `Route()`, `Save()`, `Recall()`, `New()`, `WaitForDrain()`
- Private: `camelCase`, action-first: `buildMessages()`, `sanitizeHistory()`, `setupRouter()`, `formatMessageContent()`
- Constructors: Named `New` (public) or `new` (private): `NewRouter()`, `newChannelAgent()`, `newTestStore()`

**Variables:**
- `camelCase`, full words: `channelID`, `serverID`, `httpClient`, `extractionRunning`, `turnCount`
- Short variable names for loop iteration and receivers: `i`, `j`, `r` for receiver, `t` for testing, `msg` for messages
- Avoid abbreviations: `serverID` not `sid`, `channelID` not `chID`

**Types:**
- `PascalCase`, descriptive: `ChannelAgent`, `MemoryStore`, `MemoryRow`, `Router`, `Store`
- Interfaces use simple nouns: `Tool` (not `Tooler`), `Store` (not `Storer`)

**Constants:**
- Private: `camelCase`: `maxRetries`, `defaultMode`, `extractionPrompt`, `maxVideoBytes`
- Public: `PascalCase`: `DefaultMode`, `ErrMemoryNotFound`
- Validation maps use lowercase keys: `validModes := map[string]bool{"smart": true, "mention": true}`

**Receivers:**
- One or two letters: `a` for Agent, `r` for Router, `s` for Store, `c` for Client, `m` for memory.MemoryStore, `t` for testing

## Code Style

**Formatting:**
- Default Go formatter (`gofmt`/`go fmt`)
- 80-character line length guideline, up to 100 for complex expressions
- Blank lines between logical sections within functions

**Imports:**
Three groups, separated by blank lines, in this order:
1. Standard library (alphabetical): `"context"`, `"fmt"`, `"log/slog"`, `"net/http"`
2. Third-party packages (alphabetical by module path): `"github.com/bwmarrin/discordgo"`, `_ "github.com/mattn/go-sqlite3"`
3. Internal packages (alphabetical): `"github.com/tomasmach/vespra/config"`, `"github.com/tomasmach/vespra/llm"`

Blank imports (`_`) for side effects only, placed in third-party group.

Example from `agent/agent.go`:
```go
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/memory"
	"github.com/tomasmach/vespra/soul"
	"github.com/tomasmach/vespra/tools"
)
```

## Struct Field Ordering

Order fields by:
1. Identity: `id`, `channelID`, `serverID`
2. Dependencies/shared resources: `cfg`, `llm`, `mem`, `session`, `cfgStore`, `httpClient`, `logger`, `resources`
3. State/data: `history`, `soulText`, `turnCount`, `extractionRunning`
4. Channels: always last: `msgCh`, `internalCh`

Example from `agent/agent.go`:
```go
type ChannelAgent struct {
	channelID string
	serverID  string

	cfgStore   *config.Store
	llm        *llm.Client
	httpClient *http.Client
	resources  *AgentResources
	logger     *slog.Logger

	soulText          string
	history           []llm.Message
	turnCount         int
	lastActive        atomic.Int64
	extractionRunning atomic.Bool

	ctx        context.Context
	msgCh      chan *discordgo.MessageCreate
	internalCh chan string
	cancel     context.CancelFunc
}
```

## Constructors

Format: Named `New` or `new`, return pointer, set defaults explicitly:

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

**Error messages format:** `"<verb> <noun>: <detail>"`, lowercase, no trailing punctuation:
- `"query memories: %w"`
- `"embed failed, skipping embedding"`
- `"response.default_mode %q is invalid (use smart|mention|all|none)"`

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

**Never ignore errors with `_`** unless the value is explicitly not needed (e.g., `defer rows.Close()`). Always handle or log. If ignored, use `//nolint:errcheck` comment when necessary.

## Logging

**Framework:** `log/slog` for all logging. Always use structured key-value pairs.

**Format:**
```go
slog.Info("config loaded", "path", cfgPath)
slog.Warn("embed failed, skipping embedding", "error", err)
slog.Error("failed to open memory store", "error", err)
slog.Debug("tool call executed", "tool_name", "memory_recall", "channel_id", a.channelID)
```

**Log levels:**
- `error` — something broke; attention required
- `warn` — a failure that the system can tolerate (background task failed, buffer full)
- `info` — significant lifecycle events (startup, shutdown, config loaded)
- `debug` — operational detail (tool calls, per-message state)

**Include context in errors:**
- Always include `"error", err` as the first key-value pair when logging an error
- Include identifying context: `"channel_id"`, `"server_id"`, `"user_id"` where relevant
- Example: `slog.Error("llm chat error", "error", err, "channel_id", a.channelID)`

## Context

Pass `ctx context.Context` as the first parameter to any function that does I/O:

```go
func (s *Store) Save(ctx context.Context, content string, ...) (string, error)
func (c *Client) Chat(ctx context.Context, msgs []Message, ...) (*Response, error)
func (r *Router) Route(ctx context.Context, msg *discordgo.MessageCreate) error
```

Use `context.WithTimeout()` and `context.WithCancel()` to bound operations. Propagate cancellation through goroutines via `ctx.Done()`.

## Goroutines

**Track with `sync.WaitGroup`:**
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

**Buffered channels** for agent mailboxes:
```go
msgCh: make(chan *discordgo.MessageCreate, 100),
internalCh: make(chan string, 50),
```

**Non-blocking send** to detect full or gone channel:
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

Define interfaces where:
- The caller needs to vary implementation
- Needed for testability
- Keep them small: one to three methods ideal

Example from `tools/tools.go`:
```go
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Call(ctx context.Context, args json.RawMessage) (string, error)
}
```

Implement with private structs and public registration:
```go
type memorySaveTool struct {
	store    *memory.Store
	serverID string
}

func (t *memorySaveTool) Name() string { return "memory_save" }
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

## Inline Structs for One-Off JSON

Use anonymous structs for decoding JSON that doesn't need a named type:

```go
var p struct {
	Content    string  `json:"content"`
	Importance float64 `json:"importance"`
}
if err := json.Unmarshal(args, &p); err != nil {
	return "", err
}
```

## String Building

Use `strings.Builder` for multi-line output:

```go
var sb strings.Builder
for _, r := range rows {
	fmt.Fprintf(&sb, "[%s] (importance: %.1f) %s\n", r.ID, r.Importance, r.Content)
}
return sb.String(), nil
```

## SQL

Use raw string literals. Align columns for readability. Always use `ExecContext` / `QueryContext`:

```go
_, err = s.db.ExecContext(ctx, `
	INSERT INTO memories (id, content, importance, server_id, user_id, channel_id, created_at, updated_at, forgotten)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
	id, content, importance, serverID, userID, channelID, now, now,
)
```

Always `defer rows.Close()` immediately after successful `QueryContext`.

## Comments

Comments explain **why**, not **what**. Avoid summarizing what code already says.

**Package-level doc comments** on every package:
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

## Configuration Defaults

Set defaults **after** loading, not in struct tags. Group them clearly:

```go
if cfg.Agent.HistoryLimit == 0 {
	cfg.Agent.HistoryLimit = 20
}
if cfg.Agent.IdleTimeoutMinutes == 0 {
	cfg.Agent.IdleTimeoutMinutes = 10
}
```

**Validate with maps** for discrete values:
```go
validModes := map[string]bool{"smart": true, "mention": true, "all": true, "none": true}
if !validModes[cfg.Response.DefaultMode] {
	return nil, fmt.Errorf("response.default_mode %q is invalid (use smart|mention|all|none)", cfg.Response.DefaultMode)
}
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

Don't panic in production code. Use `log/slog` + `os.Exit(1)` at startup for unrecoverable errors:

```go
cfg, err := config.Load(cfgPath)
if err != nil {
	slog.Error("failed to load config", "error", err)
	os.Exit(1)
}
```

## Project Structure

Single binary. No workspace, no sub-modules. Entry point is `main.go`.

```
.
├── main.go             — wires everything together
├── agent/              — per-channel conversation logic
├── bot/                — Discord session wrapper
├── config/             — TOML loading and validation
├── llm/                — OpenRouter HTTP client
├── memory/             — SQLite persistence
├── migrations/         — SQL migration files
├── soul/               — personality prompt loading
└── tools/              — Tool registry and implementations
```

Prefer adding to existing files over creating new ones. New files only for new logical components.

---

*Convention analysis: 2026-02-26*
