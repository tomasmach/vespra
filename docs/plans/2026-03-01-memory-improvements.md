# Memory Improvements Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Fix memory recall quality and prevent duplicate memory storage by adding dedup-on-save, similarity thresholds, FTS5 search, and two-pass user-aware recall.

**Architecture:** Storage-level dedup via cosine similarity in `Save()`, FTS5 virtual table for better keyword search, similarity threshold (0.35) to filter irrelevant recall results, and two-pass recall (user-specific + content-relevant) in the agent layer.

**Tech Stack:** Go, SQLite FTS5, existing cosine similarity in `memory/rrf.go`

---

### Task 1: Add config fields for memory tuning

**Files:**
- Modify: `config/config.go:52-61` (TurnConfig struct)
- Modify: `config/config.go:157-177` (defaults in Load)

**Step 1: Add three new fields to TurnConfig**

In `config/config.go`, add to the `TurnConfig` struct after `CoalesceMaxWaitMs`:

```go
type TurnConfig struct {
	HistoryLimit             int     `toml:"history_limit"`
	IdleTimeoutMinutes       int     `toml:"idle_timeout_minutes"`
	MaxToolIterations        int     `toml:"max_tool_iterations"`
	HistoryBackfillLimit     int     `toml:"history_backfill_limit"`
	MemoryExtractionInterval int     `toml:"memory_extraction_interval"` // -1 to disable
	CoalesceDisabled         bool    `toml:"coalesce_disabled"`
	CoalesceDebounceMs       int     `toml:"coalesce_debounce_ms"`
	CoalesceMaxWaitMs        int     `toml:"coalesce_max_wait_ms"`
	MemoryRecallLimit        int     `toml:"memory_recall_limit"`
	MemoryDedupThreshold     float64 `toml:"memory_dedup_threshold"`
	MemoryRecallThreshold    float64 `toml:"memory_recall_threshold"`
}
```

**Step 2: Add defaults in Load()**

After the `CoalesceMaxWaitMs` default block (around line 177), add:

```go
if cfg.Agent.MemoryRecallLimit <= 0 {
	cfg.Agent.MemoryRecallLimit = 15
}
if cfg.Agent.MemoryDedupThreshold <= 0 {
	cfg.Agent.MemoryDedupThreshold = 0.85
}
if cfg.Agent.MemoryRecallThreshold <= 0 {
	cfg.Agent.MemoryRecallThreshold = 0.35
}
```

**Step 3: Verify build**

Run: `go build ./...`
Expected: clean build, no errors

**Step 4: Commit**

```bash
git add config/config.go
git commit -m "feat: add memory recall, dedup, and threshold config fields"
```

---

### Task 2: Add FTS5 virtual table and backfill

**Files:**
- Modify: `memory/store.go:28-58` (migrationSQL constant)
- Modify: `memory/store.go:65-80` (New function)
- Modify: `memory/store.go:90-129` (Save — add FTS insert)
- Modify: `memory/store.go:131-147` (Forget — add FTS delete)
- Modify: `memory/store.go:202-241` (UpdateContent — add FTS update)

**Step 1: Add FTS5 table to migrationSQL**

Append to the `migrationSQL` constant in `memory/store.go`, after the `idx_conv_channel` index:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(memory_id UNINDEXED, content);
```

The full constant ends like:
```go
const migrationSQL = `
...existing SQL...
CREATE INDEX IF NOT EXISTS idx_conv_channel ON conversations(channel_id);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(memory_id UNINDEXED, content);
`
```

**Step 2: Add FTS backfill in New()**

After the migration runs successfully in `New()` (line 78), add a backfill step before the return:

```go
// Backfill FTS index for existing memories that predate FTS5 migration.
var ftsCount int
if err := db.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM memories_fts").Scan(&ftsCount); err != nil {
	db.Close()
	return nil, fmt.Errorf("count fts entries: %w", err)
}
if ftsCount == 0 {
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO memories_fts(memory_id, content) SELECT id, content FROM memories WHERE forgotten = 0`,
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("backfill fts index: %w", err)
	}
}
```

**Step 3: Add FTS insert in Save()**

In `Save()`, after the embeddings insert block (after line 123, inside the transaction), add:

```go
if _, err = tx.ExecContext(ctx,
	`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`,
	id, content,
); err != nil {
	return "", fmt.Errorf("insert fts: %w", err)
}
```

Note: `Save()` return type changes in Task 3. For now, add the FTS insert and keep the existing return type. Task 3 will refactor the signature.

**Step 4: Add FTS delete in Forget()**

In `Forget()`, after the soft-delete UPDATE (line 133), add FTS cleanup. Refactor to use a transaction:

```go
func (s *Store) Forget(ctx context.Context, serverID, memoryID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	result, err := tx.ExecContext(ctx,
		`UPDATE memories SET forgotten = 1, updated_at = ? WHERE id = ? AND server_id = ?`,
		time.Now().UTC(), memoryID, serverID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected: %w", err)
	}
	if n == 0 {
		return ErrMemoryNotFound
	}

	if _, err = tx.ExecContext(ctx,
		`DELETE FROM memories_fts WHERE memory_id = ?`, memoryID,
	); err != nil {
		return fmt.Errorf("delete fts: %w", err)
	}

	return tx.Commit()
}
```

**Step 5: Add FTS update in UpdateContent()**

In `UpdateContent()`, after the embedding upsert (line 235), add FTS sync inside the same transaction:

```go
if _, err = tx.ExecContext(ctx,
	`DELETE FROM memories_fts WHERE memory_id = ?`, id,
); err != nil {
	return fmt.Errorf("delete old fts: %w", err)
}
if _, err = tx.ExecContext(ctx,
	`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`, id, content,
); err != nil {
	return fmt.Errorf("insert new fts: %w", err)
}
```

**Step 6: Verify build and existing tests**

Run: `go build ./... && go test ./memory/...`
Expected: build succeeds, all existing tests pass (tests use `:memory:` DB which starts fresh)

**Step 7: Commit**

```bash
git add memory/store.go
git commit -m "feat: add FTS5 virtual table with backfill and sync"
```

---

### Task 3: Implement dedup-on-save

**Files:**
- Modify: `memory/search.go:13-21` (add SaveResult type)
- Modify: `memory/store.go:90-129` (refactor Save with dedup)
- Create: `memory/dedup.go` (findSimilar function)
- Modify: `memory/store_test.go` (add dedup tests)
- Modify: `tools/tools.go:98-116` (update memorySaveTool.Call)
- Modify: `tools/tools.go:75-78` (add dedupThreshold to memorySaveTool)
- Modify: `tools/tools.go:414-428` (update NewDefaultRegistry)
- Modify: `tools/tools.go:437-444` (update NewMemoryOnlyRegistry)

**Step 1: Write failing tests for dedup**

Add to `memory/store_test.go`:

```go
func TestSaveDedupSkipsIdentical(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	r1, err := store.Save(ctx, "Tomas likes coffee", "srv1", "user1", "", 0.5, 0.85)
	if err != nil {
		t.Fatalf("first Save() error: %v", err)
	}
	if r1.Status != "saved" {
		t.Fatalf("expected status 'saved', got %q", r1.Status)
	}

	// Same content — should be skipped.
	r2, err := store.Save(ctx, "Tomas likes coffee", "srv1", "user1", "", 0.5, 0.85)
	if err != nil {
		t.Fatalf("second Save() error: %v", err)
	}
	if r2.Status != "exists" {
		t.Errorf("expected status 'exists', got %q", r2.Status)
	}
	if r2.ID != r1.ID {
		t.Errorf("expected existing ID %q, got %q", r1.ID, r2.ID)
	}
}

func TestSaveDedupUpdatesLongerContent(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	r1, err := store.Save(ctx, "Tomas likes coffee", "srv1", "user1", "", 0.5, 0.85)
	if err != nil {
		t.Fatalf("first Save() error: %v", err)
	}

	// Longer content with more detail — should update existing memory.
	r2, err := store.Save(ctx, "Tomas likes dark roast coffee, especially Ethiopian", "srv1", "user1", "", 0.7, 0.85)
	if err != nil {
		t.Fatalf("second Save() error: %v", err)
	}
	if r2.Status != "updated" {
		t.Errorf("expected status 'updated', got %q", r2.Status)
	}
	if r2.ID != r1.ID {
		t.Errorf("expected same ID %q, got %q", r1.ID, r2.ID)
	}

	// Verify content was actually updated.
	var content string
	store.db.QueryRowContext(ctx, `SELECT content FROM memories WHERE id = ?`, r1.ID).Scan(&content)
	if content != "Tomas likes dark roast coffee, especially Ethiopian" {
		t.Errorf("content not updated: got %q", content)
	}
}

func TestSaveDedupDisabledWithZeroThreshold(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	r1, err := store.Save(ctx, "some fact", "srv1", "user1", "", 0.5, 0)
	if err != nil {
		t.Fatalf("first Save() error: %v", err)
	}

	// With threshold 0, dedup is disabled — should create a new memory.
	r2, err := store.Save(ctx, "some fact", "srv1", "user1", "", 0.5, 0)
	if err != nil {
		t.Fatalf("second Save() error: %v", err)
	}
	if r2.ID == r1.ID {
		t.Errorf("with threshold 0, should create new memory, got same ID")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./memory/ -run TestSaveDedup -v`
Expected: compilation error (SaveResult doesn't exist, Save signature wrong)

**Step 3: Add SaveResult type**

In `memory/search.go`, after the `MemoryRow` struct (line 21), add:

```go
// SaveResult holds the outcome of a Save operation.
type SaveResult struct {
	ID     string // memory ID (new or existing)
	Status string // "saved", "updated", or "exists"
}
```

**Step 4: Create memory/dedup.go with findSimilar**

Create `memory/dedup.go`:

```go
package memory

import (
	"context"
	"fmt"
	"sort"
)

// similarMatch holds a memory ID and its cosine similarity score.
type similarMatch struct {
	id    string
	score float32
}

// findSimilar returns the single best matching memory above the threshold
// for the given server. Returns nil if no match exceeds the threshold.
func (s *Store) findSimilar(ctx context.Context, serverID string, vec []float32, threshold float64) (*similarMatch, error) {
	embeddings, err := s.allEmbeddings(ctx, serverID)
	if err != nil {
		return nil, fmt.Errorf("load embeddings: %w", err)
	}

	type scored struct {
		id    string
		score float32
	}
	var candidates []scored
	for id, emb := range embeddings {
		if len(emb) != len(vec) {
			continue
		}
		sim := cosine(vec, emb)
		if float64(sim) >= threshold {
			candidates = append(candidates, scored{id, sim})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score > candidates[j].score
	})
	return &similarMatch{id: candidates[0].id, score: candidates[0].score}, nil
}
```

**Step 5: Refactor Save() with dedup logic**

Change the `Save` signature and implementation in `memory/store.go`:

```go
func (s *Store) Save(ctx context.Context, content, serverID, userID, channelID string, importance float64, dedupThreshold float64) (SaveResult, error) {
	vec, embedErr := s.llm.Embed(ctx, content)
	if embedErr != nil {
		slog.Warn("embed failed, skipping embedding", "error", embedErr)
	}

	// Dedup check: find existing similar memory if embedding succeeded and threshold > 0.
	if embedErr == nil && dedupThreshold > 0 {
		match, err := s.findSimilar(ctx, serverID, vec, dedupThreshold)
		if err != nil {
			slog.Warn("dedup check failed, saving as new", "error", err)
		} else if match != nil {
			// Fetch existing content to compare length.
			var existingContent string
			if err := s.db.QueryRowContext(ctx,
				`SELECT content FROM memories WHERE id = ? AND server_id = ? AND forgotten = 0`,
				match.id, serverID,
			).Scan(&existingContent); err == nil {
				if len(content) > len(existingContent) {
					// New content has more detail — update existing memory.
					if err := s.updateForDedup(ctx, match.id, serverID, content, importance, vec); err != nil {
						return SaveResult{}, fmt.Errorf("dedup update: %w", err)
					}
					return SaveResult{ID: match.id, Status: "updated"}, nil
				}
				return SaveResult{ID: match.id, Status: "exists"}, nil
			}
		}
	}

	id, err := newID()
	if err != nil {
		return SaveResult{}, fmt.Errorf("generate id: %w", err)
	}
	now := time.Now().UTC()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SaveResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, importance, server_id, user_id, channel_id, created_at, updated_at, forgotten)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, content, importance, serverID, userID, channelID, now, now,
	); err != nil {
		return SaveResult{}, fmt.Errorf("insert memory: %w", err)
	}

	if embedErr == nil {
		if _, err = tx.ExecContext(ctx,
			`INSERT INTO embeddings (memory_id, vector) VALUES (?, ?)`,
			id, llm.VectorToBlob(vec),
		); err != nil {
			return SaveResult{}, fmt.Errorf("insert embedding: %w", err)
		}
	}

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`,
		id, content,
	); err != nil {
		return SaveResult{}, fmt.Errorf("insert fts: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return SaveResult{}, fmt.Errorf("commit transaction: %w", err)
	}
	return SaveResult{ID: id, Status: "saved"}, nil
}
```

**Step 6: Add updateForDedup helper in store.go**

Add after the `Save` function:

```go
// updateForDedup updates an existing memory's content, importance, embedding, and FTS entry.
func (s *Store) updateForDedup(ctx context.Context, id, serverID, content string, importance float64, vec []float32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.ExecContext(ctx,
		`UPDATE memories SET content = ?, importance = ?, updated_at = ? WHERE id = ? AND server_id = ? AND forgotten = 0`,
		content, importance, time.Now().UTC(), id, serverID,
	); err != nil {
		return fmt.Errorf("update memory: %w", err)
	}

	if _, err = tx.ExecContext(ctx,
		`INSERT INTO embeddings (memory_id, vector) VALUES (?, ?)
		 ON CONFLICT(memory_id) DO UPDATE SET vector = excluded.vector`,
		id, llm.VectorToBlob(vec),
	); err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}

	if _, err = tx.ExecContext(ctx,
		`DELETE FROM memories_fts WHERE memory_id = ?`, id,
	); err != nil {
		return fmt.Errorf("delete old fts: %w", err)
	}
	if _, err = tx.ExecContext(ctx,
		`INSERT INTO memories_fts(memory_id, content) VALUES (?, ?)`, id, content,
	); err != nil {
		return fmt.Errorf("insert new fts: %w", err)
	}

	return tx.Commit()
}
```

**Step 7: Update all callers of Save()**

The `Save` signature changed. Update callers:

In `tools/tools.go`, update `memorySaveTool`:

```go
type memorySaveTool struct {
	store          *memory.Store
	serverID       string
	dedupThreshold float64
}
```

Update `memorySaveTool.Call()` (line 98-116):

```go
func (t *memorySaveTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Content    string   `json:"content"`
		UserID     string   `json:"user_id"`
		Importance *float64 `json:"importance"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	importance := 0.5
	if p.Importance != nil {
		importance = *p.Importance
	}
	result, err := t.store.Save(ctx, p.Content, t.serverID, p.UserID, "", importance, t.dedupThreshold)
	if err != nil {
		return "", err
	}
	switch result.Status {
	case "updated":
		return fmt.Sprintf("Memory updated with new details (id: %s)", result.ID), nil
	case "exists":
		return fmt.Sprintf("Memory already exists (id: %s)", result.ID), nil
	default:
		return fmt.Sprintf("Memory saved (id: %s)", result.ID), nil
	}
}
```

Update `NewDefaultRegistry` (line 416-428) to accept and pass `dedupThreshold`:

```go
func NewDefaultRegistry(store *memory.Store, serverID string, dedupThreshold float64, send SendFunc, react ReactFunc, searchDeps *WebSearchDeps) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID, dedupThreshold: dedupThreshold})
	r.Register(&memoryRecallTool{store: store, serverID: serverID})
	r.Register(&memoryForgetTool{store: store, serverID: serverID})
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText})
	r.Register(&reactTool{react: react})
	if searchDeps != nil {
		r.Register(&webSearchTool{deps: searchDeps})
		r.Register(&webFetchTool{timeoutSeconds: searchDeps.TimeoutSeconds})
	}
	return r
}
```

Update `NewMemoryOnlyRegistry` (line 439-444) similarly:

```go
func NewMemoryOnlyRegistry(store *memory.Store, serverID string, dedupThreshold float64) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID, dedupThreshold: dedupThreshold})
	r.Register(&memoryRecallTool{store: store, serverID: serverID})
	return r
}
```

**Step 8: Update all callers of NewDefaultRegistry and NewMemoryOnlyRegistry**

In `agent/agent.go`, update the calls:

Line 534 (handleMessage):
```go
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, a.cfgStore.Get().Agent.MemoryDedupThreshold, sendFn, reactFn, a.webSearchDeps())
```

Line 633 (handleMessages):
```go
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, a.cfgStore.Get().Agent.MemoryDedupThreshold, sendFn, reactFn, a.webSearchDeps())
```

Line 684 (handleInternalMessage):
```go
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, a.cfgStore.Get().Agent.MemoryDedupThreshold, sendFn, reactFn, nil)
```

Line 990 (runMemoryExtraction):
```go
reg := tools.NewMemoryOnlyRegistry(a.resources.Memory, a.serverID, a.cfgStore.Get().Agent.MemoryDedupThreshold)
```

**Step 9: Fix existing tests that call Save()**

In `memory/store_test.go`, update all `store.Save(...)` calls to include the new `dedupThreshold` param (use `0` to disable dedup in legacy tests, preserving their behavior):

Replace all instances of:
```go
store.Save(ctx, "...", "srv1", "user1", "chan1", 0.5)
```
With:
```go
store.Save(ctx, "...", "srv1", "user1", "chan1", 0.5, 0)
```

And update the return value handling from `id, err` to `result, err` using `result.ID`:

For example, `TestSaveAndRecall`:
```go
result, err := store.Save(ctx, "the cat sat on the mat", "srv1", "user1", "chan1", 0.5, 0)
if err != nil {
	t.Fatalf("Save() error: %v", err)
}
if result.ID == "" {
	t.Fatal("Save() returned empty ID")
}
// ... use result.ID instead of id throughout
```

Do the same for all tests: `TestForgetHidesFromRecall`, `TestSaveWithEmbeddingFailure`, `TestListLIKEEscaping`, `TestForgetWrongServerReturnsErrMemoryNotFound`, `TestUpdateContentRefreshesEmbedding`, `TestRecallDoesNotLeakCrossServer`, `TestListUnderscoreEscaping`.

Also fix any tests in `tools/tools_test.go` and `web/server_test.go` that call these constructors.

**Step 10: Run all tests**

Run: `go test ./...`
Expected: all tests pass including new dedup tests

**Step 11: Commit**

```bash
git add memory/dedup.go memory/store.go memory/search.go memory/store_test.go tools/tools.go agent/agent.go
git commit -m "feat: add dedup-on-save with cosine similarity threshold"
```

---

### Task 4: Search improvements — similarity threshold and FTS5 keyword search

**Files:**
- Modify: `memory/search.go:23-96` (Recall function)
- Modify: `memory/store_test.go` (add threshold test)

**Step 1: Write failing test for similarity threshold**

Add to `memory/store_test.go`:

```go
func TestRecallRespectsSimThreshold(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	// Save a memory (fake embedding server returns same vector for all,
	// so all memories will have cosine sim ~1.0 with any query).
	store.Save(ctx, "Tomas likes coffee", "srv1", "user1", "", 0.5, 0)

	// With threshold 0, should return results.
	results, err := store.Recall(ctx, "anything", "srv1", 10, 0)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results with threshold 0")
	}

	// Threshold doesn't filter here because fake embeddings are all identical (sim=1.0),
	// but we verify the parameter is accepted.
	results2, err := store.Recall(ctx, "anything", "srv1", 10, 0.35)
	if err != nil {
		t.Fatalf("Recall() with threshold error: %v", err)
	}
	if len(results2) == 0 {
		t.Error("expected results with threshold 0.35 (fake embeds have sim=1.0)")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestRecallRespectsSimThreshold -v`
Expected: compilation error (Recall signature doesn't accept threshold)

**Step 3: Update Recall with threshold and FTS5**

Refactor `memory/search.go` `Recall` function:

```go
func (s *Store) Recall(ctx context.Context, query, serverID string, topN int, simThreshold float64) ([]MemoryRow, error) {
	var semanticIDs []string

	vec, err := s.llm.Embed(ctx, query)
	if err != nil {
		slog.Warn("embed failed, falling back to keyword-only search", "error", err)
	} else {
		embeddings, err := s.allEmbeddings(ctx, serverID)
		if err != nil {
			return nil, fmt.Errorf("load embeddings: %w", err)
		}
		type scored struct {
			id    string
			score float32
		}
		results := make([]scored, 0, len(embeddings))
		for id, emb := range embeddings {
			if len(emb) != len(vec) {
				continue
			}
			sim := cosine(vec, emb)
			if simThreshold > 0 && float64(sim) < simThreshold {
				continue
			}
			results = append(results, scored{id, sim})
		}
		sort.Slice(results, func(i, j int) bool {
			return results[i].score > results[j].score
		})
		semanticIDs = make([]string, len(results))
		for i, r := range results {
			semanticIDs[i] = r.id
		}
	}

	// FTS5 keyword search with fallback to LIKE.
	keywordIDs, err := s.ftsSearch(ctx, query, serverID)
	if err != nil {
		slog.Warn("fts search failed, falling back to LIKE", "error", err)
		keywordIDs, err = s.likeSearch(ctx, query, serverID)
		if err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
	}

	merged := rrfMerge(semanticIDs, keywordIDs)
	if len(merged) > topN {
		merged = merged[:topN]
	}

	out := make([]MemoryRow, 0, len(merged))
	for _, id := range merged {
		var row MemoryRow
		err := s.db.QueryRowContext(ctx,
			`SELECT id, content, importance, COALESCE(user_id, ''), COALESCE(channel_id, ''), created_at
			 FROM memories WHERE id = ? AND server_id = ? AND forgotten = 0`,
			id, serverID,
		).Scan(&row.ID, &row.Content, &row.Importance, &row.UserID, &row.ChannelID, &row.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetch memory %s: %w", id, err)
		}
		out = append(out, row)
	}
	return out, nil
}

// ftsSearch returns memory IDs matching the query via FTS5 full-text search.
func (s *Store) ftsSearch(ctx context.Context, query, serverID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT f.memory_id FROM memories_fts f
		 JOIN memories m ON m.id = f.memory_id
		 WHERE m.server_id = ? AND m.forgotten = 0
		 AND memories_fts MATCH ?
		 ORDER BY rank`,
		serverID, query,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan fts result: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// likeSearch returns memory IDs matching the query via SQL LIKE substring search.
// Used as fallback when FTS5 search fails (e.g., syntax errors in query).
func (s *Store) likeSearch(ctx context.Context, query, serverID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM memories WHERE server_id = ? AND forgotten = 0 AND content LIKE ? ESCAPE '\'`,
		serverID, "%"+escapeLIKE(query)+"%",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan keyword result: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
```

**Step 4: Update all Recall callers**

The `Recall` signature now takes `simThreshold` as 5th parameter. Update all callers:

In `agent/agent.go` line 519 (handleMessage):
```go
memories, err := a.resources.Memory.Recall(ctx, msg.Content, a.serverID, cfg.Agent.MemoryRecallLimit, cfg.Agent.MemoryRecallThreshold)
```

In `agent/agent.go` line 618 (handleMessages):
```go
memories, err := a.resources.Memory.Recall(ctx, recallQuery, a.serverID, cfg.Agent.MemoryRecallLimit, cfg.Agent.MemoryRecallThreshold)
```

In `tools/tools.go` `memoryRecallTool.Call()` line 150:
```go
rows, err := t.store.Recall(ctx, p.Query, t.serverID, p.TopN, 0)
```
Note: tool-invoked recall uses threshold 0 (the LLM explicitly asked to search, don't filter).

**Step 5: Fix existing tests that call Recall()**

Update all `store.Recall(ctx, ..., topN)` calls in tests to add the threshold param:
```go
store.Recall(ctx, "query", "srv1", 5, 0) // 0 threshold for legacy tests
```

**Step 6: Run all tests**

Run: `go test ./...`
Expected: all tests pass

**Step 7: Commit**

```bash
git add memory/search.go memory/store_test.go agent/agent.go tools/tools.go
git commit -m "feat: add cosine similarity threshold and FTS5 keyword search"
```

---

### Task 5: Add RecallByUser and implement two-pass recall in agent

**Files:**
- Modify: `memory/search.go` (add RecallByUser method)
- Modify: `agent/agent.go:488-549` (handleMessage — two-pass recall)
- Modify: `agent/agent.go:569-654` (handleMessages — two-pass recall)
- Modify: `memory/store_test.go` (add RecallByUser test)

**Step 1: Write failing test for RecallByUser**

Add to `memory/store_test.go`:

```go
func TestRecallByUser(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	store.Save(ctx, "Tomas likes coffee", "srv1", "user1", "", 0.8, 0)
	store.Save(ctx, "Tomas works at Acme", "srv1", "user1", "", 0.6, 0)
	store.Save(ctx, "Alice likes tea", "srv1", "user2", "", 0.5, 0)

	results, err := store.RecallByUser(ctx, "srv1", "user1", 10)
	if err != nil {
		t.Fatalf("RecallByUser() error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results for user1, got %d", len(results))
	}
	// Should be ordered by importance DESC.
	if results[0].Importance < results[1].Importance {
		t.Errorf("expected results ordered by importance DESC")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./memory/ -run TestRecallByUser -v`
Expected: compilation error (RecallByUser doesn't exist)

**Step 3: Implement RecallByUser**

Add to `memory/search.go`:

```go
// RecallByUser returns memories associated with a specific user, ordered by
// importance (descending) then recency. Used for user-specific recall pass.
func (s *Store) RecallByUser(ctx context.Context, serverID, userID string, limit int) ([]MemoryRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, content, importance, COALESCE(user_id, ''), COALESCE(channel_id, ''), created_at
		 FROM memories
		 WHERE server_id = ? AND user_id = ? AND forgotten = 0
		 ORDER BY importance DESC, updated_at DESC
		 LIMIT ?`,
		serverID, userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("recall by user: %w", err)
	}
	defer rows.Close()

	var out []MemoryRow
	for rows.Next() {
		var row MemoryRow
		if err := rows.Scan(&row.ID, &row.Content, &row.Importance, &row.UserID, &row.ChannelID, &row.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
```

**Step 4: Run RecallByUser test**

Run: `go test ./memory/ -run TestRecallByUser -v`
Expected: PASS

**Step 5: Add mergeMemories helper in agent.go**

Add to `agent/agent.go` (before or after `buildSystemPrompt`):

```go
// mergeMemories combines user-specific and content-relevant memories,
// deduplicating by ID. User-specific memories appear first. Result is
// capped at limit.
func mergeMemories(userMems, contentMems []memory.MemoryRow, limit int) []memory.MemoryRow {
	seen := make(map[string]bool, len(userMems)+len(contentMems))
	out := make([]memory.MemoryRow, 0, limit)

	for _, m := range userMems {
		if len(out) >= limit {
			break
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	for _, m := range contentMems {
		if len(out) >= limit {
			break
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}
```

**Step 6: Implement two-pass recall in handleMessage**

Replace the single recall call in `handleMessage` (around line 519-522):

```go
// Two-pass recall: user-specific memories + content-relevant memories.
recallLimit := cfg.Agent.MemoryRecallLimit
userMemories, err := a.resources.Memory.RecallByUser(ctx, a.serverID, msg.Author.ID, recallLimit/2)
if err != nil {
	a.logger.Warn("user memory recall error", "error", err)
}
contentMemories, err := a.resources.Memory.Recall(ctx, msg.Content, a.serverID, recallLimit, cfg.Agent.MemoryRecallThreshold)
if err != nil {
	a.logger.Warn("content memory recall error", "error", err)
}
memories := mergeMemories(userMemories, contentMemories, recallLimit)
```

**Step 7: Implement two-pass recall in handleMessages**

Replace the single recall call in `handleMessages` (around line 612-621):

```go
recallParts := make([]string, 0, len(msgs))
for _, m := range msgs {
	recallParts = append(recallParts, m.Content)
}
recallQuery := strings.Join(recallParts, " ")

// Two-pass recall: user-specific memories + content-relevant memories.
// Use the last message author as the user for user-specific recall.
recallLimit := cfg.Agent.MemoryRecallLimit
lastAuthorID := ""
if lastMsg.Author != nil {
	lastAuthorID = lastMsg.Author.ID
}
userMemories, err := a.resources.Memory.RecallByUser(ctx, a.serverID, lastAuthorID, recallLimit/2)
if err != nil {
	a.logger.Warn("user memory recall error", "error", err)
}
contentMemories, err := a.resources.Memory.Recall(ctx, recallQuery, a.serverID, recallLimit, cfg.Agent.MemoryRecallThreshold)
if err != nil {
	a.logger.Warn("content memory recall error", "error", err)
}
memories := mergeMemories(userMemories, contentMemories, recallLimit)
```

**Step 8: Run all tests**

Run: `go test ./...`
Expected: all pass

**Step 9: Commit**

```bash
git add memory/search.go memory/store_test.go agent/agent.go
git commit -m "feat: add two-pass recall with user-specific memory prioritization"
```

---

### Task 6: Better system prompt formatting and tool recall default

**Files:**
- Modify: `agent/agent.go:717-739` (buildSystemPrompt — better memory formatting)
- Modify: `tools/tools.go:118-121` (memoryRecallTool — add defaultTopN)
- Modify: `tools/tools.go:139-162` (memoryRecallTool.Call — use defaultTopN)
- Modify: `tools/tools.go:414-428` (NewDefaultRegistry — pass defaultTopN)
- Modify: `tools/tools.go:437-444` (NewMemoryOnlyRegistry — pass defaultTopN)

**Step 1: Update buildSystemPrompt with richer memory formatting**

In `agent/agent.go`, update the memory section of `buildSystemPrompt`:

```go
if len(memories) > 0 {
	sb.WriteString("\n\n## Relevant Memories\n")
	now := time.Now()
	for _, m := range memories {
		age := now.Sub(m.CreatedAt)
		var ageStr string
		switch {
		case age < 24*time.Hour:
			ageStr = "today"
		case age < 48*time.Hour:
			ageStr = "yesterday"
		case age < 7*24*time.Hour:
			ageStr = fmt.Sprintf("%d days ago", int(age.Hours()/24))
		case age < 30*24*time.Hour:
			weeks := int(age.Hours() / 24 / 7)
			if weeks == 1 {
				ageStr = "1 week ago"
			} else {
				ageStr = fmt.Sprintf("%d weeks ago", weeks)
			}
		default:
			months := int(age.Hours() / 24 / 30)
			if months == 1 {
				ageStr = "1 month ago"
			} else {
				ageStr = fmt.Sprintf("%d months ago", months)
			}
		}
		fmt.Fprintf(&sb, "- [%s] (importance: %.1f, %s) %s\n", m.ID, m.Importance, ageStr, m.Content)
	}
}
```

**Step 2: Update memoryRecallTool with configurable default top_n**

Add `defaultTopN` field to `memoryRecallTool`:

```go
type memoryRecallTool struct {
	store       *memory.Store
	serverID    string
	defaultTopN int
}
```

Update `Call()`:

```go
func (t *memoryRecallTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		TopN  int    `json:"top_n"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.TopN == 0 {
		p.TopN = t.defaultTopN
	}
	// Tool-invoked recall uses threshold 0: the LLM explicitly asked, don't filter.
	rows, err := t.store.Recall(ctx, p.Query, t.serverID, p.TopN, 0)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No memories found.", nil
	}
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "[%s] (importance: %.1f) %s\n", r.ID, r.Importance, r.Content)
	}
	return sb.String(), nil
}
```

**Step 3: Update registry constructors to pass defaultTopN**

Update `NewDefaultRegistry`:

```go
func NewDefaultRegistry(store *memory.Store, serverID string, dedupThreshold float64, defaultRecallLimit int, send SendFunc, react ReactFunc, searchDeps *WebSearchDeps) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID, dedupThreshold: dedupThreshold})
	r.Register(&memoryRecallTool{store: store, serverID: serverID, defaultTopN: defaultRecallLimit})
	r.Register(&memoryForgetTool{store: store, serverID: serverID})
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText})
	r.Register(&reactTool{react: react})
	if searchDeps != nil {
		r.Register(&webSearchTool{deps: searchDeps})
		r.Register(&webFetchTool{timeoutSeconds: searchDeps.TimeoutSeconds})
	}
	return r
}
```

Update `NewMemoryOnlyRegistry`:

```go
func NewMemoryOnlyRegistry(store *memory.Store, serverID string, dedupThreshold float64, defaultRecallLimit int) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID, dedupThreshold: dedupThreshold})
	r.Register(&memoryRecallTool{store: store, serverID: serverID, defaultTopN: defaultRecallLimit})
	return r
}
```

**Step 4: Update all callers in agent.go**

Update the 4 call sites in `agent/agent.go` to pass `cfg.Agent.MemoryRecallLimit`:

```go
// handleMessage (line ~534):
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, cfg.Agent.MemoryDedupThreshold, cfg.Agent.MemoryRecallLimit, sendFn, reactFn, a.webSearchDeps())

// handleMessages (line ~633):
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, cfg.Agent.MemoryDedupThreshold, cfg.Agent.MemoryRecallLimit, sendFn, reactFn, a.webSearchDeps())

// handleInternalMessage (line ~684):
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, cfg.Agent.MemoryDedupThreshold, cfg.Agent.MemoryRecallLimit, sendFn, reactFn, nil)

// runMemoryExtraction (line ~990):
reg := tools.NewMemoryOnlyRegistry(a.resources.Memory, a.serverID, cfg.Agent.MemoryDedupThreshold, cfg.Agent.MemoryRecallLimit)
```

**Step 5: Fix any tests using registry constructors**

Update any tests in `tools/tools_test.go` and `web/server_test.go` that call `NewDefaultRegistry` or `NewMemoryOnlyRegistry` to pass the new parameters.

**Step 6: Run all tests**

Run: `go test ./...`
Expected: all pass

**Step 7: Commit**

```bash
git add agent/agent.go tools/tools.go
git commit -m "feat: add memory age and importance to system prompt, configurable recall limit"
```

---

### Task 7: Final verification and cleanup

**Files:**
- All modified files

**Step 1: Run full test suite**

Run: `go test ./...`
Expected: all pass

**Step 2: Build the binary**

Run: `go build -o vespra .`
Expected: clean build

**Step 3: Verify no unused imports or variables**

Run: `go vet ./...`
Expected: clean

**Step 4: Commit any remaining fixes**

If any issues found in steps 1-3, fix and commit.
