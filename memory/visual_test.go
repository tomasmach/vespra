package memory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
)

func newTestVisualStore(t *testing.T) *Store {
	t.Helper()
	embSrv := fakeEmbeddingServer(t, 4)
	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test",
			EmbeddingModel:        "test-embed",
			RequestTimeoutSeconds: 5,
			BaseURL:               embSrv.URL,
		},
		Memory: config.MemoryConfig{DBPath: filepath.Join(t.TempDir(), "memory.db")},
	}
	cfgStore := config.NewStoreFromConfig(cfg)
	llmClient := llm.New(cfgStore)
	store, err := New(&cfg.Memory, llmClient)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { store.db.Close() })
	return store
}

func TestSaveVisualMemoryPersistsFileAndMetadata(t *testing.T) {
	store := newTestVisualStore(t)
	ctx := context.Background()

	result, err := store.SaveVisual(ctx, VisualSaveOptions{
		Label:       "Alice",
		Description: "Alice smiling in a red jacket",
		ServerID:    "srv1",
		UserID:      "user1",
		ChannelID:   "chan1",
		MessageID:   "msg1",
		ContentType: "image/png",
		Data:        []byte("png-bytes"),
	})
	if err != nil {
		t.Fatalf("SaveVisual() error: %v", err)
	}
	if result.ID == "" || result.Status != SaveStatusSaved {
		t.Fatalf("unexpected save result: %+v", result)
	}

	rows, total, err := store.ListVisual(ctx, VisualListOptions{ServerID: "srv1", Query: "alice", Limit: 10})
	if err != nil {
		t.Fatalf("ListVisual() error: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("expected one visual memory, got total=%d len=%d", total, len(rows))
	}
	if rows[0].Label != "Alice" || rows[0].NormalizedLabel != "alice" || rows[0].Description != "Alice smiling in a red jacket" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
	if _, err := os.Stat(rows[0].FilePath); err != nil {
		t.Fatalf("saved file missing: %v", err)
	}
}

func TestSaveVisualMemoryDedupesByHashAndCapsPerLabel(t *testing.T) {
	store := newTestVisualStore(t)
	ctx := context.Background()

	first, err := store.SaveVisual(ctx, VisualSaveOptions{
		Label:       "Alice",
		ServerID:    "srv1",
		ContentType: "image/jpeg",
		Data:        []byte("same-image"),
	})
	if err != nil {
		t.Fatalf("SaveVisual() first error: %v", err)
	}
	dup, err := store.SaveVisual(ctx, VisualSaveOptions{
		Label:       "alice",
		ServerID:    "srv1",
		ContentType: "image/jpeg",
		Data:        []byte("same-image"),
	})
	if err != nil {
		t.Fatalf("SaveVisual() duplicate error: %v", err)
	}
	if dup.ID != first.ID || dup.Status != SaveStatusExists {
		t.Fatalf("expected duplicate to return existing id, got first=%+v dup=%+v", first, dup)
	}

	for i := 0; i < maxVisualMemoriesPerLabel+2; i++ {
		_, err := store.SaveVisual(ctx, VisualSaveOptions{
			Label:       "Alice",
			ServerID:    "srv1",
			ContentType: "image/png",
			Data:        []byte{byte(i), byte(i + 1), byte(i + 2)},
		})
		if err != nil {
			t.Fatalf("SaveVisual() cap insert %d error: %v", i, err)
		}
	}

	rows, total, err := store.ListVisual(ctx, VisualListOptions{ServerID: "srv1", Query: "alice", Limit: 20})
	if err != nil {
		t.Fatalf("ListVisual() error: %v", err)
	}
	if total != maxVisualMemoriesPerLabel || len(rows) != maxVisualMemoriesPerLabel {
		t.Fatalf("expected cap of %d active rows, got total=%d len=%d", maxVisualMemoriesPerLabel, total, len(rows))
	}
}

func TestForgetVisualHidesAndRemovesFile(t *testing.T) {
	store := newTestVisualStore(t)
	ctx := context.Background()

	result, err := store.SaveVisual(ctx, VisualSaveOptions{
		Label:       "Bob",
		ServerID:    "srv1",
		ContentType: "image/png",
		Data:        []byte("bob-image"),
	})
	if err != nil {
		t.Fatalf("SaveVisual() error: %v", err)
	}
	row, err := store.GetVisual(ctx, "srv1", result.ID)
	if err != nil {
		t.Fatalf("GetVisual() error: %v", err)
	}

	if err := store.ForgetVisual(ctx, "srv1", result.ID); err != nil {
		t.Fatalf("ForgetVisual() error: %v", err)
	}
	if _, err := store.GetVisual(ctx, "srv1", result.ID); err != ErrMemoryNotFound {
		t.Fatalf("expected ErrMemoryNotFound after forget, got %v", err)
	}
	if _, err := os.Stat(row.FilePath); !os.IsNotExist(err) {
		t.Fatalf("expected file removal, got stat err %v", err)
	}
}
