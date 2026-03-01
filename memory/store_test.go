package memory

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
)

// fakeEmbeddingServer returns a test server that responds to embedding requests
// with a fixed-dimension vector.
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

func TestSaveAndRecall(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	result, err := store.Save(ctx, "the cat sat on the mat", "srv1", "user1", "chan1", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if result.ID == "" {
		t.Fatal("Save() returned empty ID")
	}

	results, err := store.Recall(ctx, "cat mat", "srv1", 5, 0)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Recall() returned no results")
	}

	found := false
	for _, r := range results {
		if r.ID == result.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("saved memory %q not found in recall results: %v", result.ID, results)
	}
}

func TestForgetHidesFromRecall(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	result, err := store.Save(ctx, "secret memory", "srv1", "user1", "chan1", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if err := store.Forget(ctx, "srv1", result.ID); err != nil {
		t.Fatalf("Forget() error: %v", err)
	}

	results, err := store.Recall(ctx, "secret memory", "srv1", 5, 0)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	for _, r := range results {
		if r.ID == result.ID {
			t.Errorf("forgotten memory %q still appears in recall results", result.ID)
		}
	}
}

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

	result, err := store.Save(ctx, "keyword-only memory", "srv1", "user1", "chan1", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() should succeed even when embedding fails, got error: %v", err)
	}
	if result.ID == "" {
		t.Fatal("Save() returned empty ID")
	}
}

func TestListLIKEEscaping(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	result, err := store.Save(ctx, "100% done with task_1", "srv1", "user1", "chan1", 0.5, 0)
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
	if rows[0].ID != result.ID {
		t.Errorf("expected memory %q, got %q", result.ID, rows[0].ID)
	}
}

func TestForgetUnknownIDReturnsErrMemoryNotFound(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	err := store.Forget(ctx, "srv1", "nonexistent-id")
	if !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("Forget() with unknown ID should return ErrMemoryNotFound, got: %v", err)
	}
}

func TestForgetWrongServerReturnsErrMemoryNotFound(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	result, err := store.Save(ctx, "cross-server memory", "srv1", "user1", "chan1", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Attempt to forget using a different server_id.
	err = store.Forget(ctx, "srv2", result.ID)
	if !errors.Is(err, ErrMemoryNotFound) {
		t.Errorf("Forget() with wrong server_id should return ErrMemoryNotFound, got: %v", err)
	}
}

func TestUpdateContentRefreshesEmbedding(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	result, err := store.Save(ctx, "original content", "srv1", "user1", "chan1", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Verify an embedding row exists after Save.
	var beforeBlob []byte
	if err := store.db.QueryRowContext(ctx,
		`SELECT vector FROM embeddings WHERE memory_id = ?`, result.ID,
	).Scan(&beforeBlob); err != nil {
		t.Fatalf("embedding not found after Save: %v", err)
	}

	if err := store.UpdateContent(ctx, result.ID, "srv1", "updated content"); err != nil {
		t.Fatalf("UpdateContent() error: %v", err)
	}

	// The embedding row should still exist (upserted) after UpdateContent.
	var afterBlob []byte
	if err := store.db.QueryRowContext(ctx,
		`SELECT vector FROM embeddings WHERE memory_id = ?`, result.ID,
	).Scan(&afterBlob); err != nil {
		t.Fatalf("embedding not found after UpdateContent: %v", err)
	}

	// The content column should reflect the new value.
	var content string
	if err := store.db.QueryRowContext(ctx,
		`SELECT content FROM memories WHERE id = ?`, result.ID,
	).Scan(&content); err != nil {
		t.Fatalf("memory not found after UpdateContent: %v", err)
	}
	if content != "updated content" {
		t.Errorf("content not updated: got %q", content)
	}
}

func TestRecallDoesNotLeakCrossServer(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	// Save a memory under srv1.
	_, err := store.Save(ctx, "srv1 private data", "srv1", "user1", "chan1", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Recall for srv2 should return nothing.
	results, err := store.Recall(ctx, "srv1 private data", "srv2", 10, 0)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("cross-server leak: Recall for srv2 returned %d results from srv1", len(results))
	}
}

func TestListUnderscoreEscaping(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	// Save two memories: one matching, one that would match with unescaped _
	r1, err := store.Save(ctx, "task_done", "srv1", "u", "c", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	r2, err := store.Save(ctx, "taskXdone", "srv1", "u", "c", 0.5, 0)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Search for literal "task_done"; "taskXdone" should NOT appear
	rows, _, err := store.List(ctx, ListOptions{ServerID: "srv1", Query: "task_done", Limit: 10})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	foundID1 := false
	for _, r := range rows {
		if r.ID == r2.ID {
			t.Error("underscore was not escaped: 'taskXdone' matched 'task_done' query")
		}
		if r.ID == r1.ID {
			foundID1 = true
		}
	}
	if !foundID1 {
		t.Errorf("expected memory %q ('task_done') to appear in results, but it did not", r1.ID)
	}
}

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

	var content string
	if err := store.db.QueryRowContext(ctx, `SELECT content FROM memories WHERE id = ?`, r1.ID).Scan(&content); err != nil {
		t.Fatalf("fetch updated content: %v", err)
	}
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

	r2, err := store.Save(ctx, "some fact", "srv1", "user1", "", 0.5, 0)
	if err != nil {
		t.Fatalf("second Save() error: %v", err)
	}
	if r2.ID == r1.ID {
		t.Errorf("with threshold 0, should create new memory, got same ID")
	}
}

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

func TestRecallRespectsSimThreshold(t *testing.T) {
	embSrv := fakeEmbeddingServer(t, 4)
	store := newTestStore(t, embSrv)
	ctx := context.Background()

	store.Save(ctx, "Tomas likes coffee", "srv1", "user1", "", 0.5, 0)

	// With threshold 0, should return results.
	results, err := store.Recall(ctx, "anything", "srv1", 10, 0)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected results with threshold 0")
	}

	// Threshold doesn't filter here because fake embeddings are all identical (sim=1.0).
	results2, err := store.Recall(ctx, "anything", "srv1", 10, 0.35)
	if err != nil {
		t.Fatalf("Recall() with threshold error: %v", err)
	}
	if len(results2) == 0 {
		t.Error("expected results with threshold 0.35 (fake embeds have sim=1.0)")
	}
}
