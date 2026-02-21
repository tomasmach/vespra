package llm

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomasmach/mnemon-bot/config"
)

func TestVectorRoundtrip(t *testing.T) {
	original := []float32{1.5, -0.5, 0.0, math.MaxFloat32}
	blob := VectorToBlob(original)
	decoded := BlobToVector(blob)

	if len(decoded) != len(original) {
		t.Fatalf("length mismatch: got %d, want %d", len(decoded), len(original))
	}
	for i := range original {
		if math.Abs(float64(decoded[i]-original[i])) > 1e-6 {
			t.Errorf("index %d: got %v, want %v", i, decoded[i], original[i])
		}
	}
}

func TestBlobToVectorTruncates(t *testing.T) {
	// 5 bytes: not a multiple of 4 — last byte should be silently dropped
	blob := []byte{0, 0, 128, 63, 0xFF}
	decoded := BlobToVector(blob)
	if len(decoded) != 1 {
		t.Errorf("expected 1 float (5 bytes / 4 = 1), got %d", len(decoded))
	}
}

func TestBlobToVectorEmpty(t *testing.T) {
	decoded := BlobToVector(nil)
	if len(decoded) != 0 {
		t.Errorf("expected empty result, got %v", decoded)
	}
}

func clientWithBaseURL(t *testing.T, baseURL string) *Client {
	t.Helper()
	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test-key",
			Model:                 "test-model",
			EmbeddingModel:        "test-embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               baseURL,
		},
	}
	cfgStore := config.NewStoreFromConfig(cfg)
	return New(cfgStore)
}

func TestChatRetriesOn5xx(t *testing.T) {
	// Speed up retries in this test
	orig := retryDelays
	retryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { retryDelays = orig })

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
	choice, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
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

func TestChatFailsFastOn4xx(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client := clientWithBaseURL(t, srv.URL)
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error on 4xx, got nil")
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", callCount.Load())
	}
}

func TestChatRetries429(t *testing.T) {
	// 429 Too Many Requests should also be retried
	orig := retryDelays
	retryDelays = []time.Duration{0, 0}
	t.Cleanup(func() { retryDelays = orig })

	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client := clientWithBaseURL(t, srv.URL)
	_, err := client.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after 429 retry, got: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", callCount.Load())
	}
}
