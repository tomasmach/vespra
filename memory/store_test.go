package memory

import (
	"context"
	"encoding/json"
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

	id, err := store.Save(ctx, "the cat sat on the mat", "srv1", "user1", "chan1", 0.5)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	if id == "" {
		t.Fatal("Save() returned empty ID")
	}

	results, err := store.Recall(ctx, "cat mat", "srv1", 5)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Recall() returned no results")
	}

	found := false
	for _, r := range results {
		if r.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("saved memory %q not found in recall results: %v", id, results)
	}
}

func TestForgetHidesFromRecall(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)
	store := newTestStore(t, failSrv)
	ctx := context.Background()

	id, err := store.Save(ctx, "secret memory", "srv1", "user1", "chan1", 0.5)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if err := store.Forget(ctx, "srv1", id); err != nil {
		t.Fatalf("Forget() error: %v", err)
	}

	results, err := store.Recall(ctx, "secret memory", "srv1", 5)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	for _, r := range results {
		if r.ID == id {
			t.Errorf("forgotten memory %q still appears in recall results", id)
		}
	}
}

func TestSaveWithEmbeddingFailure(t *testing.T) {
	// Embedding server always returns 500
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)

	store := newTestStore(t, failSrv)
	ctx := context.Background()

	// Save should succeed even if embedding fails (keyword search still works)
	id, err := store.Save(ctx, "keyword-only memory", "srv1", "user1", "chan1", 0.5)
	if err != nil {
		t.Fatalf("Save() should succeed without embedding, got: %v", err)
	}
	if id == "" {
		t.Fatal("Save() returned empty ID")
	}

	// Keyword recall should still find it
	results, err := store.Recall(ctx, "keyword-only memory", "srv1", 5)
	if err != nil {
		t.Fatalf("Recall() error: %v", err)
	}
	found := false
	for _, r := range results {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Errorf("memory not found via keyword fallback: %v", results)
	}
}

func TestListLIKEEscaping(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)
	store := newTestStore(t, failSrv)
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

func TestListUnderscoreEscaping(t *testing.T) {
	failSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(failSrv.Close)
	store := newTestStore(t, failSrv)
	ctx := context.Background()

	// Save two memories: one matching, one that would match with unescaped _
	id1, err := store.Save(ctx, "task_done", "srv1", "u", "c", 0.5)
	if err != nil {
		t.Fatalf("Save() error: %v", err)
	}
	id2, err := store.Save(ctx, "taskXdone", "srv1", "u", "c", 0.5)
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
		if r.ID == id2 {
			t.Error("underscore was not escaped: 'taskXdone' matched 'task_done' query")
		}
		if r.ID == id1 {
			foundID1 = true
		}
	}
	if !foundID1 {
		t.Errorf("expected memory %q ('task_done') to appear in results, but it did not", id1)
	}
}
