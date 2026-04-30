package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/memory"
	"github.com/tomasmach/vespra/tools"
)

func newToolTestStore(t *testing.T) *memory.Store {
	t.Helper()
	embSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`)
	}))
	t.Cleanup(embSrv.Close)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "test",
			EmbeddingModel:        "test-embed",
			RequestTimeoutSeconds: 5,
			BaseURL:               embSrv.URL,
		},
		Memory: config.MemoryConfig{DBPath: filepath.Join(t.TempDir(), "memory.db")},
	}
	store, err := memory.New(&cfg.Memory, llm.New(config.NewStoreFromConfig(cfg)))
	if err != nil {
		t.Fatalf("memory.New() error: %v", err)
	}
	return store
}

func TestImageGenCalledFlagSetOnSuccessfulCAS(t *testing.T) {
	var imageRunning atomic.Bool
	var wg sync.WaitGroup

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            cancelledCtx,
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 1,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	if r.ImageGenCalled {
		t.Fatal("ImageGenCalled should be false before any tool call")
	}

	result, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test prompt"}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "Image generation started") {
		t.Errorf("unexpected result: %q", result)
	}
	if !r.ImageGenCalled {
		t.Error("ImageGenCalled should be true after successful call")
	}

	wg.Wait()
}

func TestImageGenDefinitionAdvertisesReferenceImageIDs(t *testing.T) {
	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	var generateImageParams json.RawMessage
	for _, def := range r.Definitions() {
		if def.Function.Name == "generate_image" {
			generateImageParams = def.Function.Parameters
			break
		}
	}
	if generateImageParams == nil {
		t.Fatal("generate_image definition not found")
	}

	var schema struct {
		Properties map[string]struct {
			Type        string `json:"type"`
			Description string `json:"description"`
			Items       *struct {
				Type string `json:"type"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(generateImageParams, &schema); err != nil {
		t.Fatalf("unmarshal generate_image parameters: %v", err)
	}

	property, ok := schema.Properties["reference_image_ids"]
	if !ok {
		t.Fatal("generate_image schema should expose reference_image_ids")
	}
	if property.Type != "array" {
		t.Fatalf("reference_image_ids type = %q, want array", property.Type)
	}
	if property.Items == nil || property.Items.Type != "string" {
		t.Fatalf("reference_image_ids items = %#v, want string items", property.Items)
	}
}

func TestImageGenCASRejectedWhenAlreadyRunning(t *testing.T) {
	var imageRunning atomic.Bool
	imageRunning.Store(true)

	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	result, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "already running") {
		t.Errorf("expected 'already running' message, got %q", result)
	}
	if r.ImageGenCalled {
		t.Error("ImageGenCalled should be false when CAS fails")
	}
}

func TestImageGenEmptyPromptReturnsError(t *testing.T) {
	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	result, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":""}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "prompt is required") {
		t.Errorf("expected 'prompt is required' message, got %q", result)
	}
	if r.ImageGenCalled {
		t.Error("ImageGenCalled should be false when prompt is empty")
	}
	if imageRunning.Load() {
		t.Error("ImageRunning should not be set when prompt is empty")
	}
}

func TestImageGenNSFWBlocked(t *testing.T) {
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"images":[{"url":"http://example.com/img.jpg"}],"has_nsfw_concepts":[true]}`)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var sentText string
	var sendImageCalled atomic.Bool

	deps := &tools.ImageGenDeps{
		SendImage: func(string, io.Reader, string) error {
			sendImageCalled.Store(true)
			return nil
		},
		SendText: func(content string) error {
			sentText = content
			return nil
		},
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if !strings.Contains(sentText, "inappropriate") {
		t.Errorf("expected NSFW block message, got %q", sentText)
	}
	if sendImageCalled.Load() {
		t.Error("SendImage should NOT be called when NSFW is flagged")
	}
}

func TestImageGenHTTPError(t *testing.T) {
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"error":"internal error"}`)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var sentText string

	deps := &tools.ImageGenDeps{
		SendImage: func(string, io.Reader, string) error { return nil },
		SendText: func(content string) error {
			sentText = content
			return nil
		},
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if !strings.Contains(sentText, "Failed") {
		t.Errorf("expected error message containing 'Failed', got %q", sentText)
	}
}

func TestImageGenNSFWSpoilerWhenSafetyCheckerOff(t *testing.T) {
	imageData := []byte("fake-jpeg-data")

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/image.jpg"}],"has_nsfw_concepts":[true]}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var receivedFilename string

	deps := &tools.ImageGenDeps{
		SendImage: func(filename string, data io.Reader, caption string) error {
			receivedFilename = filename
			return nil
		},
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  false,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if receivedFilename != "SPOILER_generated.png" {
		t.Errorf("expected filename 'SPOILER_generated.png', got %q", receivedFilename)
	}
}

func TestImageGenNonNSFWWithSafetyCheckerOffSendsPlainFilename(t *testing.T) {
	imageData := []byte("fake-jpeg-data")

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/image.jpg"}],"has_nsfw_concepts":[false]}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var receivedFilename string

	deps := &tools.ImageGenDeps{
		SendImage: func(filename string, data io.Reader, caption string) error {
			receivedFilename = filename
			return nil
		},
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  false,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if receivedFilename != "generated.png" {
		t.Errorf("expected filename 'generated.png' for non-NSFW image, got %q", receivedFilename)
	}
}

func TestImageGenAbsentNSFWArrayWithSafetyCheckerOffSendsPlainFilename(t *testing.T) {
	imageData := []byte("fake-jpeg-data")

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	// has_nsfw_concepts is absent entirely
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/image.jpg"}]}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var receivedFilename string

	deps := &tools.ImageGenDeps{
		SendImage: func(filename string, data io.Reader, caption string) error {
			receivedFilename = filename
			return nil
		},
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  false,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if receivedFilename != "generated.png" {
		t.Errorf("expected filename 'generated.png' when has_nsfw_concepts is absent, got %q", receivedFilename)
	}
}

func TestImageGenSuccess(t *testing.T) {
	imageData := []byte("fake-jpeg-data")

	// Mock image download server
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	var requestBody map[string]any
	// Mock fal.ai API
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/image.jpg"}],"has_nsfw_concepts":[false]}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var receivedFilename string
	var receivedData []byte

	deps := &tools.ImageGenDeps{
		SendImage: func(filename string, data io.Reader, caption string) error {
			receivedFilename = filename
			var err error
			receivedData, err = io.ReadAll(data)
			return err
		},
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "test-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"a sunset"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if receivedFilename != "generated.png" {
		t.Errorf("expected filename 'generated.png', got %q", receivedFilename)
	}
	if requestBody["aspect_ratio"] != "auto" {
		t.Errorf("expected aspect_ratio auto, got %#v", requestBody["aspect_ratio"])
	}
	if requestBody["resolution"] != "1K" {
		t.Errorf("expected resolution 1K, got %#v", requestBody["resolution"])
	}
	if requestBody["output_format"] != "png" {
		t.Errorf("expected output_format png, got %#v", requestBody["output_format"])
	}
	if _, ok := requestBody["image_size"]; ok {
		t.Errorf("did not expect legacy image_size in fal request: %#v", requestBody["image_size"])
	}
	if string(receivedData) != string(imageData) {
		t.Errorf("expected image data %q, got %q", imageData, receivedData)
	}
}

func TestImageGenOmittedModeWithSourceImagesDefaultsToGenerate(t *testing.T) {
	imageData := []byte("generated-image-data")

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	var requestPath string
	var requestBody map[string]any
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/generated.png"}],"has_nsfw_concepts":[false]}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var receivedData []byte

	deps := &tools.ImageGenDeps{
		SendImage: func(filename string, data io.Reader, caption string) error {
			var err error
			receivedData, err = io.ReadAll(data)
			return err
		},
		SendText:        func(string) error { return nil },
		ImageWg:         &wg,
		ImageRunning:    &imageRunning,
		Ctx:             context.Background(),
		APIKey:          "test-key",
		Model:           "text-model",
		EditModel:       "fal-ai/nano-banana-2/edit",
		SourceImageURLs: []string{"data:image/png;base64,abc123"},
		SafetyChecker:   true,
		TimeoutSeconds:  5,
		BaseURL:         falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"a fresh landscape"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if requestPath != "/text-model" {
		t.Errorf("expected generate endpoint path, got %q", requestPath)
	}
	if _, ok := requestBody["image_urls"]; ok {
		t.Fatalf("did not expect source image URLs for omitted mode generation: %#v", requestBody["image_urls"])
	}
	if string(receivedData) != string(imageData) {
		t.Errorf("expected generated image data %q, got %q", imageData, receivedData)
	}
}

func TestImageGenEditModeUsesEditModelAndSourceImages(t *testing.T) {
	imageData := []byte("edited-image-data")

	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	var requestPath string
	var requestBody map[string]any
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/edited.png"}],"description":""}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	var receivedData []byte

	deps := &tools.ImageGenDeps{
		SendImage: func(filename string, data io.Reader, caption string) error {
			var err error
			receivedData, err = io.ReadAll(data)
			return err
		},
		SendText:        func(string) error { return nil },
		ImageWg:         &wg,
		ImageRunning:    &imageRunning,
		Ctx:             context.Background(),
		APIKey:          "test-key",
		Model:           "text-model",
		EditModel:       "fal-ai/nano-banana-2/edit",
		SourceImageURLs: []string{"data:image/png;base64,abc123"},
		SafetyChecker:   true,
		TimeoutSeconds:  5,
		BaseURL:         falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"make it cinematic","mode":"edit","aspect_ratio":"1:1"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if requestPath != "/fal-ai/nano-banana-2/edit" {
		t.Errorf("expected edit endpoint path, got %q", requestPath)
	}
	imageURLs, ok := requestBody["image_urls"].([]any)
	if !ok || len(imageURLs) != 1 || imageURLs[0] != "data:image/png;base64,abc123" {
		t.Fatalf("unexpected image_urls payload: %#v", requestBody["image_urls"])
	}
	if requestBody["prompt"] != "make it cinematic" {
		t.Errorf("expected prompt in request body, got %#v", requestBody["prompt"])
	}
	if string(receivedData) != string(imageData) {
		t.Errorf("expected edited image data %q, got %q", imageData, receivedData)
	}
}

func TestVisualMemorySaveToolStoresCurrentSourceImages(t *testing.T) {
	store := newToolTestStore(t)
	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:       func(string, io.Reader, string) error { return nil },
		SendText:        func(string) error { return nil },
		ImageWg:         &wg,
		ImageRunning:    &imageRunning,
		Ctx:             context.Background(),
		APIKey:          "test-key",
		Model:           "text-model",
		SourceImageURLs: []string{"data:image/png;base64,YWxpY2U="},
		SafetyChecker:   true,
		TimeoutSeconds:  5,
		VisualStore:     store,
		ServerID:        "srv1",
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(store, "srv1", 0, 0, send, react, nil, deps, 2)

	result, err := r.Dispatch(context.Background(), "visual_memory_save", json.RawMessage(`{"label":"Alice","description":"Alice in a red jacket","user_id":"user1"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if !strings.Contains(result, "Visual memory saved") {
		t.Fatalf("unexpected result: %q", result)
	}

	rows, total, err := store.ListVisual(context.Background(), memory.VisualListOptions{ServerID: "srv1", Query: "alice", Limit: 10})
	if err != nil {
		t.Fatalf("ListVisual() error: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].Label != "Alice" {
		t.Fatalf("expected saved visual memory for Alice, got total=%d rows=%+v", total, rows)
	}
}

func TestVisualMemoryRecallToolReturnsReferenceIDs(t *testing.T) {
	store := newToolTestStore(t)
	save, err := store.SaveVisual(context.Background(), memory.VisualSaveOptions{
		Label:       "Alice",
		Description: "Alice in a red jacket",
		ServerID:    "srv1",
		ContentType: "image/png",
		Data:        []byte("alice"),
	})
	if err != nil {
		t.Fatalf("SaveVisual() error: %v", err)
	}

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "text-model",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
		VisualStore:    store,
		ServerID:       "srv1",
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(store, "srv1", 0, 0, send, react, nil, deps, 2)

	result, err := r.Dispatch(context.Background(), "visual_memory_recall", json.RawMessage(`{"query":"alice"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if !strings.Contains(result, save.ID) || !strings.Contains(result, "Alice in a red jacket") {
		t.Fatalf("recall result did not include saved reference: %q", result)
	}
}

func TestImageGenUsesStoredReferenceImageIDs(t *testing.T) {
	store := newToolTestStore(t)
	save, err := store.SaveVisual(context.Background(), memory.VisualSaveOptions{
		Label:       "Alice",
		Description: "Alice in a red jacket",
		ServerID:    "srv1",
		ContentType: "image/png",
		Data:        []byte("alice-reference"),
	})
	if err != nil {
		t.Fatalf("SaveVisual() error: %v", err)
	}

	imageData := []byte("generated-image-data")
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(imageData)
	}))
	defer imgServer.Close()

	var requestPath string
	var requestBody map[string]any
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"images":[{"url":"%s/generated.png"}],"has_nsfw_concepts":[false]}`, imgServer.URL)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "text-model",
		EditModel:      "fal-ai/nano-banana-2/edit",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
		BaseURL:        falServer.URL,
		VisualStore:    store,
		ServerID:       "srv1",
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(store, "srv1", 0, 0, send, react, nil, deps, 2)

	_, err = r.Dispatch(context.Background(), "generate_image", json.RawMessage(fmt.Sprintf(`{"prompt":"Alice as a detective","reference_image_ids":["%s"]}`, save.ID)))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if requestPath != "/fal-ai/nano-banana-2/edit" {
		t.Errorf("expected edit endpoint for references, got %q", requestPath)
	}
	imageURLs, ok := requestBody["image_urls"].([]any)
	if !ok || len(imageURLs) != 1 {
		t.Fatalf("expected one reference image URL, got %#v", requestBody["image_urls"])
	}
	if got := imageURLs[0].(string); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Fatalf("expected stored reference as png data URL, got %q", got)
	}
}

func TestImageGenEditModeWithoutSourceImagesReturnsError(t *testing.T) {
	var imageRunning atomic.Bool
	var wg sync.WaitGroup
	deps := &tools.ImageGenDeps{
		SendImage:      func(string, io.Reader, string) error { return nil },
		SendText:       func(string) error { return nil },
		ImageWg:        &wg,
		ImageRunning:   &imageRunning,
		Ctx:            context.Background(),
		APIKey:         "test-key",
		Model:          "text-model",
		EditModel:      "fal-ai/nano-banana-2/edit",
		SafetyChecker:  true,
		TimeoutSeconds: 5,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps, 2)

	result, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"make it cinematic","mode":"edit"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	if !strings.Contains(result, "requires an attached or replied-to image") {
		t.Errorf("expected missing source image message, got %q", result)
	}
	if r.ImageGenCalled {
		t.Error("ImageGenCalled should be false when edit mode has no source images")
	}
	if imageRunning.Load() {
		t.Error("ImageRunning should not be set when edit mode has no source images")
	}
}
