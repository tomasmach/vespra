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
