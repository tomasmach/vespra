package llm_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
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

func TestChatFailsFastOn4xx(t *testing.T) {
	var callCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	client := clientWithBaseURL(t, srv.URL)
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
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
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
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

	_, err := client.Chat(context.Background(), messages, nil, nil)
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

	_, err := client.Chat(context.Background(), messages, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *capturedModel != "default-model" {
		t.Errorf("expected default-model for plain-text follow-up, got %q", *capturedModel)
	}
}

func captureBodyServer(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &capturedBody
}

func capturedMessages(t *testing.T, body *map[string]any) []any {
	t.Helper()
	msgs, ok := (*body)["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("expected messages array, got %v", (*body)["messages"])
	}
	return msgs
}

// TestNoVisionModelStripsImages verifies that when no vision_model is configured,
// image content parts are stripped and the request reaches the server as plain text.
func TestNoVisionModelStripsImages(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithBaseURL(t, srv.URL)

	messages := []llm.Message{
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "what's in this image?"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://cdn.discordapp.com/img.png"}},
			},
		},
	}

	_, err := client.Chat(context.Background(), messages, nil, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	msgs := capturedMessages(t, capturedBody)
	lastMsg := msgs[len(msgs)-1].(map[string]any)

	content, ok := lastMsg["content"].(string)
	if !ok {
		t.Fatalf("expected content to be a string (images stripped), got %T: %v", lastMsg["content"], lastMsg["content"])
	}
	if !strings.Contains(content, "image(s) attached") {
		t.Errorf("expected note about image in content, got %q", content)
	}
	if !strings.Contains(content, "what's in this image?") {
		t.Errorf("expected original text preserved in content, got %q", content)
	}
}

// TestNoVisionModelStripsHistoricalImages verifies that when no vision_model is configured,
// image content parts in historical (non-last) messages are also stripped.
func TestNoVisionModelStripsHistoricalImages(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithBaseURL(t, srv.URL)

	messages := []llm.Message{
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "look at this"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://cdn.discordapp.com/old.png"}},
			},
		},
		{Role: "assistant", Content: "nice image"},
		{Role: "user", Content: "what do you think?"},
	}

	_, err := client.Chat(context.Background(), messages, nil, nil)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	msgs := capturedMessages(t, capturedBody)
	firstMsg := msgs[0].(map[string]any)
	content, ok := firstMsg["content"].(string)
	if !ok {
		t.Fatalf("expected historical message content to be a string (images stripped), got %T: %v", firstMsg["content"], firstMsg["content"])
	}
	if !strings.Contains(content, "image(s) attached") {
		t.Errorf("expected note about image in historical message content, got %q", content)
	}
}

// captureRequestServer captures the request URL path, authorization header, and
// request body for each call, then returns a success response.
func captureRequestServer(t *testing.T) (*httptest.Server, *string, *string, *map[string]any) {
	t.Helper()
	var capturedURL string
	var capturedAuth string
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedURL = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	return srv, &capturedURL, &capturedAuth, &capturedBody
}

func newTestClientWithConfig(t *testing.T, cfg *config.Config) *llm.Client {
	t.Helper()
	cfgStore := config.NewStoreFromConfig(cfg)
	return llm.New(cfgStore)
}

// TestChatOptsProviderOpenRouterRoutesToOpenRouterEndpoint verifies that when
// opts.Provider is "openrouter" the request is sent to the OpenRouter base URL
// rather than the default BaseURL, using openrouter_key.
func TestChatOptsProviderOpenRouterRoutesToOpenRouterEndpoint(t *testing.T) {
	srv, capturedURL, capturedAuth, _ := captureRequestServer(t)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "openrouter-key",
			Model:                 "global-model",
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               "http://should-not-be-used.invalid",
		},
	}
	client := newTestClientWithConfig(t, cfg)
	t.Cleanup(llm.SetOpenRouterBaseURL(client, srv.URL))

	opts := &llm.ChatOptions{Provider: "openrouter"}
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *capturedURL != "/chat/completions" {
		t.Errorf("expected request path /chat/completions, got %q", *capturedURL)
	}
	if *capturedAuth != "Bearer openrouter-key" {
		t.Errorf("expected Authorization header %q, got %q", "Bearer openrouter-key", *capturedAuth)
	}
}

// TestChatOptsProviderGLMRoutesToGLMEndpoint verifies that when opts.Provider is
// "glm" the request is sent to the configured GLMBaseURL using the GLMKey.
func TestChatOptsProviderGLMRoutesToGLMEndpoint(t *testing.T) {
	srv, capturedURL, capturedAuth, _ := captureRequestServer(t)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "or-key",
			GLMKey:                "glm-secret",
			GLMBaseURL:            srv.URL,
			Model:                 "global-model",
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               "http://should-not-be-used.invalid",
		},
	}
	client := newTestClientWithConfig(t, cfg)

	opts := &llm.ChatOptions{Provider: "glm"}
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *capturedURL != "/chat/completions" {
		t.Errorf("expected request path /chat/completions, got %q", *capturedURL)
	}
	if *capturedAuth != "Bearer glm-secret" {
		t.Errorf("expected Authorization header %q, got %q", "Bearer glm-secret", *capturedAuth)
	}
}

// TestChatOptsModelOverrideIsUsed verifies that when opts.Model is set, the
// request body contains that model name instead of the global default.
func TestChatOptsModelOverrideIsUsed(t *testing.T) {
	srv, capturedModel := captureModelServer(t)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test-key",
			Model:                 "global-model",
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               srv.URL,
		},
	}
	client := newTestClientWithConfig(t, cfg)

	opts := &llm.ChatOptions{Model: "per-agent-model"}
	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *capturedModel != "per-agent-model" {
		t.Errorf("expected model %q, got %q", "per-agent-model", *capturedModel)
	}
}

// TestVisionModelWinsOverPerAgentProviderForImageContent verifies that when a
// per-agent GLM provider is set but a vision model is also configured globally,
// the vision endpoint is used (not GLM) when the last message contains images.
func TestVisionModelWinsOverPerAgentProviderForImageContent(t *testing.T) {
	// Set up a vision server to confirm it IS called.
	visionCalled := false
	var capturedAuth string
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalled = true
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "vision-ok"}},
			},
		})
	}))
	t.Cleanup(visionSrv.Close)

	// Set up a GLM server to confirm it is NOT called.
	glmCalled := false
	glmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		glmCalled = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(glmSrv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "or-key",
			GLMKey:                "glm-secret",
			GLMBaseURL:            glmSrv.URL,
			Model:                 "global-model",
			VisionModel:           "vision-model",
			VisionBaseURL:         visionSrv.URL,
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
		},
	}
	client := newTestClientWithConfig(t, cfg)

	imageMessages := []llm.Message{
		{Role: "user", Content: "hi"},
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "what is this?"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
			},
		},
	}

	opts := &llm.ChatOptions{Provider: "glm"}
	_, err := client.Chat(context.Background(), imageMessages, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !visionCalled {
		t.Error("vision server was not called; vision model should override per-agent provider for image content")
	}
	if glmCalled {
		t.Error("GLM server was called but vision model should have taken precedence")
	}
	if capturedAuth != "Bearer or-key" {
		t.Errorf("expected OpenRouter key for vision request, got %q", capturedAuth)
	}
}

// TestChatNilOptsFallsBackToGlobalConfig verifies that when opts is nil the
// global cfg.Model and default BaseURL are used unchanged.
func TestChatNilOptsFallsBackToGlobalConfig(t *testing.T) {
	srv, capturedModel := captureModelServer(t)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test-key",
			Model:                 "global-model",
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               srv.URL,
		},
	}
	client := newTestClientWithConfig(t, cfg)

	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *capturedModel != "global-model" {
		t.Errorf("expected global model %q, got %q", "global-model", *capturedModel)
	}
}

func TestExtraToolsIncludedInRequestBody(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithBaseURL(t, srv.URL)

	extraTool := json.RawMessage(`{"type":"web_search","web_search":{"enable":true}}`)
	opts := &llm.ChatOptions{
		ExtraTools: []json.RawMessage{extraTool},
	}

	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools, ok := (*capturedBody)["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected tools array with 1 entry, got %v", (*capturedBody)["tools"])
	}
	toolMap := tools[0].(map[string]any)
	if toolMap["type"] != "web_search" {
		t.Errorf("expected tool type web_search, got %v", toolMap["type"])
	}
}

func TestExtraToolsMergedWithFunctionTools(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithBaseURL(t, srv.URL)

	funcTools := []llm.ToolDefinition{{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "test_func",
			Description: "A test function",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
	}}
	extraTool := json.RawMessage(`{"type":"web_search","web_search":{"enable":true}}`)
	opts := &llm.ChatOptions{
		ExtraTools: []json.RawMessage{extraTool},
	}

	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, funcTools, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools, ok := (*capturedBody)["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected tools array with 2 entries, got %v", (*capturedBody)["tools"])
	}
}

// TestVisionRoutesToGLMWhenVisionBaseURLMatchesGLMBaseURL verifies that when
// VisionBaseURL is the same as GLMBaseURL, vision requests use the GLM key.
func TestVisionRoutesToGLMWhenVisionBaseURLMatchesGLMBaseURL(t *testing.T) {
	glmSrv, _, capturedAuth, capturedBody := captureRequestServer(t)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "or-key",
			GLMKey:                "glm-secret",
			GLMBaseURL:            glmSrv.URL,
			Model:                 "global-model",
			VisionModel:           "glm-5",
			VisionBaseURL:         glmSrv.URL, // same as GLMBaseURL
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               "http://should-not-be-used.invalid",
		},
	}
	client := newTestClientWithConfig(t, cfg)

	imageMessages := []llm.Message{
		{Role: "user", Content: "hi"},
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "what is this?"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
			},
		},
	}

	opts := &llm.ChatOptions{Provider: "glm"}
	_, err := client.Chat(context.Background(), imageMessages, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *capturedAuth != "Bearer glm-secret" {
		t.Errorf("expected GLM key for vision request when VisionBaseURL matches GLMBaseURL, got %q", *capturedAuth)
	}
	model, _ := (*capturedBody)["model"].(string)
	if model != "glm-5" {
		t.Errorf("expected vision model glm-5, got %q", model)
	}
}

func TestDescribeMedia(t *testing.T) {
	t.Run("no vision model returns empty string without HTTP call", func(t *testing.T) {
		var serverCalled atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverCalled.Store(true)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		// clientWithBaseURL does not set VisionModel, so DescribeMedia should short-circuit.
		client := clientWithBaseURL(t, srv.URL)
		parts := []llm.ContentPart{
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
		}
		desc, err := client.DescribeMedia(context.Background(), parts)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if desc != "" {
			t.Errorf("expected empty description, got %q", desc)
		}
		if serverCalled.Load() {
			t.Error("expected no HTTP call when vision model is not set")
		}
	})

	t.Run("vision model set returns description text", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "content": "A cat sitting on a mat."}},
				},
			})
		}))
		t.Cleanup(srv.Close)

		client := clientWithVisionModel(t, srv.URL)
		parts := []llm.ContentPart{
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/cat.png"}},
		}
		desc, err := client.DescribeMedia(context.Background(), parts)
		if err != nil {
			t.Fatalf("expected no error, got: %v", err)
		}
		if desc != "A cat sitting on a mat." {
			t.Errorf("expected description %q, got %q", "A cat sitting on a mat.", desc)
		}
	})

	t.Run("HTTP 5xx returns error", func(t *testing.T) {
		t.Cleanup(llm.SetRetryDelays([]time.Duration{0, 0}))

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)

		client := clientWithVisionModel(t, srv.URL)
		parts := []llm.ContentPart{
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
		}
		_, err := client.DescribeMedia(context.Background(), parts)
		if err == nil {
			t.Fatal("expected error on 5xx, got nil")
		}
	})
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

func TestReasoningEffortSentForTextRequests(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithVisionModel(t, srv.URL)
	opts := &llm.ChatOptions{MaxTokens: 8192, ReasoningEffort: "medium"}

	if _, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reasoning, _ := (*capturedBody)["reasoning"].(map[string]any)
	if reasoning["effort"] != "medium" {
		t.Errorf("expected reasoning.effort=medium, got %v", (*capturedBody)["reasoning"])
	}

	srv, capturedBody = captureBodyServer(t) // fresh capture: decoding into the old map would keep "reasoning"
	client = clientWithVisionModel(t, srv.URL)
	image := llm.Message{Role: "user", ContentParts: []llm.ContentPart{
		{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
	}}
	if _, err := client.Chat(context.Background(), []llm.Message{image}, nil, opts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := (*capturedBody)["reasoning"]; ok {
		t.Errorf("reasoning must not be sent to the vision model, got %v", (*capturedBody)["reasoning"])
	}
}

func TestChatReturnsResponseMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id": "gen-123",
			"model": "openai/gpt-6-luna",
			"provider": "OpenAI",
			"choices": [{
				"finish_reason": "length",
				"native_finish_reason": "max_output_tokens",
				"message": {"role": "assistant", "content": null, "refusal": "I can't help with that."}
			}],
			"usage": {"prompt_tokens": 900, "completion_tokens": 1024, "completion_tokens_details": {"reasoning_tokens": 1024}}
		}`))
	}))
	t.Cleanup(srv.Close)
	client := clientWithBaseURL(t, srv.URL)

	choice, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if choice.Message.Content != "" || choice.FinishReason != "length" || choice.NativeFinishReason != "max_output_tokens" {
		t.Errorf("unexpected choice: %+v", choice)
	}
	if choice.Message.Refusal != "I can't help with that." {
		t.Errorf("refusal = %q", choice.Message.Refusal)
	}
	if choice.GenerationID != "gen-123" || choice.Provider != "OpenAI" || choice.Usage.CompletionTokensDetails.ReasoningTokens != 1024 {
		t.Errorf("unexpected metadata: id=%q provider=%q usage=%+v", choice.GenerationID, choice.Provider, choice.Usage)
	}
}

// imageModelServer serves OpenRouter metadata saying "sees-images" takes image
// input, counts metadata lookups, and captures chat completion request bodies.
func imageModelServer(t *testing.T) (*httptest.Server, *atomic.Int32, *[]map[string]any) {
	t.Helper()
	var lookups atomic.Int32
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			lookups.Add(1)
			if r.URL.Path != "/models/sees-images/endpoints" {
				http.NotFound(w, r)
				return
			}
			w.Write([]byte(`{"data":{"architecture":{"input_modalities":["text","image"]}}}`))
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &lookups, &bodies
}

func TestChatSendsImagesToMainModelThatSeesThem(t *testing.T) {
	srv, lookups, bodies := imageModelServer(t)
	client := newTestClientWithConfig(t, &config.Config{LLM: config.LLMConfig{
		OpenRouterKey:         "or-key",
		Model:                 "global-model",
		VisionModel:           "vision-model",
		BaseURL:               "http://should-not-be-used.invalid",
		RequestTimeoutSeconds: 5,
	}})
	t.Cleanup(llm.SetOpenRouterBaseURL(client, srv.URL))
	opts := &llm.ChatOptions{Provider: "openrouter", Model: "sees-images", ReasoningEffort: "medium"}

	// The step after a tool call: the image must still reach the main model.
	messages := []llm.Message{
		{Role: "user", ContentParts: []llm.ContentPart{
			{Type: "text", Text: "what is this?"},
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:image/png;base64,AAAA"}},
			{Type: "video_url", VideoURL: &llm.VideoURL{URL: "data:video/mp4;base64,BBBB"}},
		}},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_1", Type: "function", Function: llm.FunctionCall{Name: "reply", Arguments: `{"content":"hi"}`}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "Replied."},
	}
	for range 2 {
		if _, err := client.Chat(context.Background(), messages, nil, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if lookups.Load() != 1 {
		t.Errorf("expected 1 cached metadata lookup, got %d", lookups.Load())
	}
	body := (*bodies)[1]
	if body["model"] != "sees-images" {
		t.Errorf("expected the main model, got %v", body["model"])
	}
	if reasoning, _ := body["reasoning"].(map[string]any); reasoning["effort"] != "medium" {
		t.Errorf("expected reasoning.effort=medium, got %v", body["reasoning"])
	}
	parts, ok := capturedMessages(t, &body)[0].(map[string]any)["content"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("expected text + image parts, got %v", capturedMessages(t, &body)[0])
	}
	if text := parts[0].(map[string]any)["text"].(string); !strings.Contains(text, "what is this?") || !strings.Contains(text, "1 video(s) attached") {
		t.Errorf("expected user text and a note for the dropped video, got %q", text)
	}
	if parts[1].(map[string]any)["type"] != "image_url" {
		t.Errorf("expected the image part to be kept, got %v", parts[1])
	}
}

func TestDescribeMediaUsesVisionModelWhenMainModelSeesImages(t *testing.T) {
	srv, _, bodies := imageModelServer(t)
	client := newTestClientWithConfig(t, &config.Config{LLM: config.LLMConfig{
		Model:                 "sees-images",
		VisionModel:           "vision-model",
		BaseURL:               srv.URL,
		RequestTimeoutSeconds: 5,
	}})

	parts := []llm.ContentPart{{Type: "video_url", VideoURL: &llm.VideoURL{URL: "data:video/mp4;base64,BBBB"}}}
	if _, err := client.DescribeMedia(context.Background(), parts); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(*bodies) != 1 || (*bodies)[0]["model"] != "vision-model" {
		t.Errorf("expected one vision-model request, got %v", *bodies)
	}
}
