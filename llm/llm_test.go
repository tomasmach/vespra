package llm_test

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
	"github.com/tomasmach/mnemon-bot/llm"
)

func TestVectorRoundtrip(t *testing.T) {
	original := []float32{1.5, -0.5, 0.0, math.MaxFloat32}
	blob := llm.VectorToBlob(original)
	decoded := llm.BlobToVector(blob)

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
	decoded := llm.BlobToVector(blob)
	if len(decoded) != 1 {
		t.Errorf("expected 1 float (5 bytes / 4 = 1), got %d", len(decoded))
	}
}

func TestBlobToVectorEmpty(t *testing.T) {
	decoded := llm.BlobToVector(nil)
	if len(decoded) != 0 {
		t.Errorf("expected empty result, got %v", decoded)
	}
}

func clientWithBaseURL(t *testing.T, baseURL string) *llm.Client {
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
	return llm.New(cfgStore)
}

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
	choice, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil)
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
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil {
		t.Fatal("expected error on 4xx, got nil")
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 call (no retry on 4xx), got %d", callCount.Load())
	}
}

func TestChatRetries429(t *testing.T) {
	// 429 Too Many Requests should also be retried
	t.Cleanup(llm.SetRetryDelays([]time.Duration{0, 0}))

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
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("expected success after 429 retry, got: %v", err)
	}
	if callCount.Load() != 2 {
		t.Errorf("expected 2 calls, got %d", callCount.Load())
	}
}

func TestMessageMarshalStringContent(t *testing.T) {
	msg := llm.Message{Role: "user", Content: "hello"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(data, &out)
	if s, ok := out["content"].(string); !ok || s != "hello" {
		t.Errorf("expected content to be string %q, got %v", "hello", out["content"])
	}
}

func clientWithVisionModel(t *testing.T, baseURL string) *llm.Client {
	t.Helper()
	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test-key",
			Model:                 "default-model",
			VisionModel:           "vision-model",
			EmbeddingModel:        "test-embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               baseURL,
		},
	}
	cfgStore := config.NewStoreFromConfig(cfg)
	return llm.New(cfgStore)
}

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

// TestVisionModelUsedForCurrentImageMessage verifies that the vision model is used
// when the last (current) message contains image parts.
func TestVisionModelUsedForCurrentImageMessage(t *testing.T) {
	srv, capturedModel := captureModelServer(t)
	client := clientWithVisionModel(t, srv.URL)

	messages := []llm.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "what's in this image?"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
			},
		},
	}

	_, err := client.Chat(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *capturedModel != "vision-model" {
		t.Errorf("expected vision-model for image message, got %q", *capturedModel)
	}
}

// TestVisionModelNotUsedForHistoricalImageMessage verifies that the vision model is NOT used
// when only a past history message had images, and the current message is plain text.
func TestVisionModelNotUsedForHistoricalImageMessage(t *testing.T) {
	srv, capturedModel := captureModelServer(t)
	client := clientWithVisionModel(t, srv.URL)

	messages := []llm.Message{
		{Role: "user", Content: "hello"},
		// past message that had an image
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "look at this"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/old.png"}},
			},
		},
		{Role: "assistant", Content: "nice image"},
		// current plain-text message
		{Role: "user", Content: "what do you think about it?"},
	}

	_, err := client.Chat(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *capturedModel != "default-model" {
		t.Errorf("expected default-model for plain-text follow-up, got %q", *capturedModel)
	}
}

func TestMessageMarshalContentParts(t *testing.T) {
	msg := llm.Message{
		Role: "user",
		ContentParts: []llm.ContentPart{
			{Type: "text", Text: "describe this"},
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://cdn.discordapp.com/img.png"}},
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(data, &out)
	parts, ok := out["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected content to be array of 2, got %v", out["content"])
	}
	first := parts[0].(map[string]any)
	if first["type"] != "text" {
		t.Errorf("expected first part type=text, got %v", first["type"])
	}
	second := parts[1].(map[string]any)
	if second["type"] != "image_url" {
		t.Errorf("expected second part type=image_url, got %v", second["type"])
	}
}
