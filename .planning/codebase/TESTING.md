# Testing Patterns

**Analysis Date:** 2026-02-26

## Test Framework

**Runner:**
- Go built-in `testing` package
- Run all tests: `go test ./...`
- Run with verbose output: `go test -v ./...`
- Watch mode: Use external tool like `entr` or `reflex` (not built-in)

**Assertion Library:**
- Manual assertions using `if` statements and `t.Error()` / `t.Errorf()` / `t.Fatal()` / `t.Fatalf()`
- No assertion library (no `testify`, `goconvey`, etc.)

## Test File Organization

**Location:**
- Co-located with source code: `store.go` → `store_test.go`, `agent.go` → `agent_test.go`
- Use `package_test` suffix for black-box testing when accessing only public APIs
- Use internal package (no `_test` suffix) when test must access unexported symbols
- Use `export_test.go` pattern to re-export unexported symbols for tests (e.g., `llm/export_test.go`)

**Naming:**
- Test files: `*_test.go` (e.g., `store_test.go`, `agent_test.go`)
- Test package: Usually same as implementation package for white-box tests, or `<package>_test` for black-box
- Export files: `export_test.go` in the same package to expose unexported symbols only for testing

**Structure:**
```
llm/
├── llm.go           — main implementation
├── export_test.go   — test helpers that expose unexported functions
└── llm_test.go      — tests in package llm_test (black-box)

memory/
├── store.go         — implementation
├── store_test.go    — tests, accessing unexported symbols directly
└── rrf_test.go      — tests in same package, accessing unexported cosine() and rrfMerge()
```

Total test files: 12 files across 10 packages. 97 total test functions.

## Test Structure

**Helper functions** use `t.Helper()` to exclude themselves from line reporting:

```go
func newTestStore(t *testing.T, embSrv *httptest.Server) *Store {
	t.Helper()
	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test",
			EmbeddingModel:        "test-embed",
			RequestTimeoutSeconds: 5,
		},
		Memory: config.MemoryConfig{DBPath: ":memory:"},
	}
	if embSrv != nil {
		cfg.LLM.BaseURL = embSrv.URL
	}
	cfgStore := config.NewStoreFromConfig(cfg)
	llmClient := llm.New(cfgStore)

	store, err := New(&cfg.Memory, llmClient)
	if err != nil {
		t.Fatalf("failed to create test store: %v", err)
	}
	t.Cleanup(func() { store.db.Close() })
	return store
}
```

**Test naming:** `Test<FunctionName><Scenario>` or `Test<Behavior>`:
- `TestSaveAndRecall` — tests the happy path of Save → Recall roundtrip
- `TestForgetHidesFromRecall` — tests that Forget hides memories from Recall
- `TestSaveWithEmbeddingFailure` — tests graceful degradation when embedding fails
- `TestResolveResponseMode` — tests the priority resolution logic

**Sub-tests with `t.Run()`** for grouped related assertions:

```go
func TestRRFMerge(t *testing.T) {
	t.Run("deduplicates and ranks by combined score", func(t *testing.T) {
		semantic := []string{"a", "b", "c"}
		keyword := []string{"b", "c", "d"}
		result := rrfMerge(semantic, keyword)
		if len(result) != 4 {
			t.Fatalf("expected 4 unique IDs, got %d: %v", len(result), result)
		}
		// ...
	})

	t.Run("empty inputs", func(t *testing.T) {
		result := rrfMerge(nil, nil)
		if len(result) != 0 {
			t.Errorf("expected empty result, got %v", result)
		}
	})
}
```

Table-driven tests using struct slices:

```go
func TestResolveResponseMode(t *testing.T) {
	cfg := &config.Config{
		Response: config.ResponseConfig{DefaultMode: "smart"},
		Agents: []config.AgentConfig{
			{
				ServerID:     "server1",
				ResponseMode: "all",
				Channels: []config.ChannelConfig{
					{ID: "chan1", ResponseMode: "none"},
				},
			},
		},
	}

	tests := []struct {
		name      string
		serverID  string
		channelID string
		want      string
	}{
		{"channel override wins", "server1", "chan1", "none"},
		{"agent-level default", "server1", "chan2", "all"},
		{"global default for unknown server", "server2", "chan3", "smart"},
		{"global default when no agent config", "", "chan4", "smart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ResolveResponseMode(tt.serverID, tt.channelID)
			if got != tt.want {
				t.Errorf("ResolveResponseMode(%q, %q) = %q, want %q",
					tt.serverID, tt.channelID, got, tt.want)
			}
		})
	}
}
```

**Cleanup with `t.Cleanup()`:**
```go
store := newTestStore(t, embSrv)
t.Cleanup(func() { store.db.Close() })
```

## Setup and Teardown

**Helper creation functions** (`newTestStore`, `newTestRouter`, `fakeEmbeddingServer`):
- Named `new<Type>` for internal setup
- Include `t.Helper()` call
- Register cleanup functions with `t.Cleanup()`
- Return fully initialized resources

```go
func fakeEmbeddingServer(t *testing.T, dim int) *httptest.Server {
	t.Helper()
	vec := make([]float64, dim)
	for i := range vec {
		vec[i] = float64(i) / float64(dim+1)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"embedding": vec},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}
```

**No explicit teardown functions** — use `t.Cleanup()` to register all cleanup operations at creation time.

## Mocking and Test Servers

**HTTP mocking with `httptest.NewServer()`:**
- Create a test server that responds with predictable data
- Capture requests to verify behavior

```go
func captureModelServer(t *testing.T) (*httptest.Server, *string) {
	t.Helper()
	var capturedModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		capturedModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &capturedModel
}
```

**Atomic counters for request counting:**
```go
var callCount atomic.Int32
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	n := callCount.Add(1)
	if n < 3 {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// success response
}))
t.Cleanup(srv.Close)

client := clientWithBaseURL(t, srv.URL)
_, err := client.Chat(context.Background(), []llm.Message{...}, nil, nil)
if callCount.Load() != 3 {
	t.Errorf("expected 3 calls, got %d", callCount.Load())
}
```

**Environment variable overrides with `t.Setenv()`:**
```go
t.Setenv("VESPRA_DB_PATH", wantDBPath)
cfg, err := config.Load(cfgFile)
if cfg.Memory.DBPath != wantDBPath {
	t.Errorf("Memory.DBPath = %q, want %q (override not applied)", cfg.Memory.DBPath, wantDBPath)
}
```

**Temporary directories with `t.TempDir()`:**
```go
dir := t.TempDir()
cfgFile := filepath.Join(dir, "config.toml")
if err := os.WriteFile(cfgFile, []byte(minimalTOML), 0o600); err != nil {
	t.Fatalf("write temp config: %v", err)
}
```

## What to Mock

**Mock these:**
- External HTTP services (OpenRouter, GLM, web APIs)
- Time-dependent behavior (use atomic counters or controlled test delays)
- Failure scenarios (simulate 5xx, 4xx, timeouts)

**Do NOT mock these:**
- SQLite (use `:memory:` database)
- Internal logic (test actual implementations)
- Simple utilities (no need to mock string builders, JSON encoding)

## Fixtures and Factories

**Test data via helper functions:**
```go
func msg(content string, attachments ...*discordgo.MessageAttachment) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:     content,
			Author:      &discordgo.User{Username: "alice"},
			Attachments: attachments,
		},
	}
}

func attachment(contentType, url string) *discordgo.MessageAttachment {
	return &discordgo.MessageAttachment{ContentType: contentType, URL: url}
}

func msgAt(username, content string, ts time.Time) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			Content:   content,
			Author:    &discordgo.User{Username: username},
			Timestamp: ts,
		},
	}
}
```

**No separate fixture files** — keep test data creation close to tests where used.

## Error Testing

Test for expected errors using `errors.Is()`:

```go
func TestForgetUnknownIDReturnsErrMemoryNotFound(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	err := store.Forget(ctx, "srv1", "nonexistent-id")
	if !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("Forget() with unknown ID should return ErrMemoryNotFound, got: %v", err)
	}
}
```

Test error messages for content:
```go
if err == nil {
	t.Fatal("expected error on 4xx, got nil")
}
if !strings.Contains(err.Error(), "nonexistent_tool") {
	t.Errorf("error should mention the tool name, got: %v", err)
}
```

## Async Testing

Context-based cancellation:
```go
ctx, cancel := context.WithCancel(context.Background())
t.Cleanup(cancel)

r, err := NewRouter(ctx, cfgStore, llmClient, nil, make(map[string]*AgentResources))
if err != nil {
	t.Fatalf("NewRouter: %v", err)
}
```

## Test-Specific Helpers and Exports

**Use `export_test.go` pattern** to expose unexported symbols for tests only:

`llm/export_test.go`:
```go
package llm

import "time"

// SetRetryDelays overrides retryDelays for the duration of a test and returns
// a restore function to be called via t.Cleanup.
func SetRetryDelays(d []time.Duration) func() {
	orig := retryDelays
	retryDelays = d
	return func() { retryDelays = orig }
}

// SetOpenRouterBaseURL overrides the OpenRouter endpoint on a Client for testing.
// Returns a restore function to be called via t.Cleanup.
func SetOpenRouterBaseURL(c *Client, url string) func() {
	orig := c.openRouterBaseURL
	c.openRouterBaseURL = url
	return func() { c.openRouterBaseURL = orig }
}
```

Then used in tests:
```go
func TestChatRetriesOn5xx(t *testing.T) {
	t.Cleanup(llm.SetRetryDelays([]time.Duration{0, 0}))
	// ... test code
}
```

## Assertion Failures vs Setup Failures

**Setup failures:** Use `t.Fatal()` / `t.Fatalf()` to stop test immediately:
```go
store, err := New(&cfg.Memory, llmClient)
if err != nil {
	t.Fatalf("failed to create test store: %v", err)
}
```

**Assertion failures:** Use `t.Error()` / `t.Errorf()` to record failure but continue test:
```go
if got != want {
	t.Errorf("got %v, want %v", got, want)
}
```

## Coverage

**Requirements:** No enforced minimum coverage target. Guidance from CLAUDE.md:
> Write tests for critical user paths and high-risk logic. Focus on behavior and outputs, not implementation details. Prioritize integration tests at external boundaries (SQLite, HTTP). Skip trivial getters, TOML parsing or config file loading, or experimental code. Do test business logic that lives inside config packages (e.g. priority resolution like `ResolveResponseMode`).

**View coverage:**
```bash
go test -cover ./...
go test -coverprofile=coverage.out ./... && go tool cover -html=coverage.out
```

## Test Types

**Unit Tests:**
- Scope: Single function/method behavior
- Approach: Direct function call with minimal setup
- Examples: `TestCosine()`, `TestFormatMessageContent()`, `TestSplitMessageRespects2000CharLimit()`

**Integration Tests:**
- Scope: Multiple components working together, including external boundaries
- Approach: Set up test servers, real SQLite databases, full config loading
- Examples: `TestSaveAndRecall()`, `TestChatRetriesOn5xx()`, `TestResolveResponseMode()`

**E2E Tests:**
- Not used in this codebase. Functional testing done through Discord DM with live bot.

## Common Patterns

**Retry/backoff testing:**
```go
func TestChatRetriesOn5xx(t *testing.T) {
	// Speed up retries in this test
	t.Cleanup(llm.SetRetryDelays([]time.Duration{0, 0}))

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "hello"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := clientWithBaseURL(t, srv.URL)
	choice, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if choice.Message.Content != "hello" {
		t.Errorf("unexpected content: %q", choice.Message.Content)
	}
	if callCount.Load() != 3 {
		t.Errorf("expected 3 calls (2 failures + 1 success), got %d", callCount.Load())
	}
}
```

**Security boundary testing (cross-server isolation):**
```go
func TestRecallDoesNotLeakCrossServer(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	// Save a memory under srv1
	_, err := store.Save(ctx, "srv1 private data", "srv1", "user1", "chan1", 0.5)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Recall for srv2 should return nothing
	results, err := store.Recall(ctx, "srv1 private data", "srv2", 10)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("cross-server leak: Recall for srv2 returned %d results from srv1", len(results))
	}
}
```

**Graceful degradation (embedding failure):**
```go
func TestSaveWithEmbeddingFailure(t *testing.T) {
	// Embedding server always returns 500; Save must still succeed and create
	// a keyword-only memory (no embedding row) so the bot remains functional
	// when the embedding service is degraded.
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)

	store := newTestStore(t, failSrv)
	ctx := context.Background()

	id, err := store.Save(ctx, "keyword-only memory", "srv1", "user1", "chan1", 0.5)
	if err != nil {
		t.Fatalf("Save() should succeed even when embedding fails, got error: %v", err)
	}
	if id == "" {
		t.Fatal("Save() returned empty ID")
	}
}
```

**SQL special character escaping:**
```go
func TestListLIKEEscaping(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	id, err := store.Save(ctx, "100% done with task_1", "srv1", "user1", "chan1", 0.5)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	rows, total, err := store.List(ctx, ListOptions{
		ServerID: "srv1",
		Query:    "100% done",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if total == 0 || len(rows) == 0 {
		t.Fatalf("expected results with LIKE-special query, got 0")
	}
	if rows[0].ID != id {
		t.Errorf("expected memory %q, got %q", id, rows[0].ID)
	}
}
```

---

*Testing analysis: 2026-02-26*
