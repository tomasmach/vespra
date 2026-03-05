package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tomasmach/vespra/tools"
)

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
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

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
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

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
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

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
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

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
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if !strings.Contains(sentText, "Failed") {
		t.Errorf("expected error message containing 'Failed', got %q", sentText)
	}
}

func TestImageGenImg2ImgWithReference(t *testing.T) {
	var capturedPath string
	var capturedBody []byte

	// Mock fal.ai API — record which endpoint was hit and the request body
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"images":[{"url":"data:image/jpeg;base64,/9j/fake"}],"has_nsfw_concepts":[false]}`)
	}))
	defer falServer.Close()

	// Mock image download — use a data URL so no real HTTP call is needed
	imgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte("fake-img"))
	}))
	defer imgServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup

	deps := &tools.ImageGenDeps{
		SendImage:         func(string, io.Reader, string) error { return nil },
		SendText:          func(string) error { return nil },
		ImageWg:           &wg,
		ImageRunning:      &imageRunning,
		Ctx:               context.Background(),
		APIKey:            "test-key",
		Model:             "fal-ai/flux/schnell",
		Img2ImgModel:      "fal-ai/flux/dev/image-to-image",
		ReferenceImageURL: "data:image/jpeg;base64,/9j/ref",
		SafetyChecker:     false,
		TimeoutSeconds:    5,
		BaseURL:           falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

	// Override the fal response to serve a real image URL
	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test","use_reference_image":true}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	wg.Wait()

	if capturedPath != "/fal-ai/flux/dev/image-to-image" {
		t.Errorf("expected img2img endpoint, got path %q", capturedPath)
	}

	var reqBody map[string]any
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if reqBody["image_url"] != "data:image/jpeg;base64,/9j/ref" {
		t.Errorf("expected image_url in request, got %v", reqBody["image_url"])
	}
	if strength, ok := reqBody["strength"].(float64); !ok || strength != 0.85 {
		t.Errorf("expected strength 0.85, got %v", reqBody["strength"])
	}
	if steps, ok := reqBody["num_inference_steps"].(float64); !ok || steps != 28 {
		t.Errorf("expected num_inference_steps 28, got %v", reqBody["num_inference_steps"])
	}
}

func TestImageGenImg2ImgWithoutReference(t *testing.T) {
	var capturedPath string
	var capturedBody []byte

	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"images":[{"url":"data:image/jpeg;base64,/9j/fake"}],"has_nsfw_concepts":[false]}`)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup

	deps := &tools.ImageGenDeps{
		SendImage:         func(string, io.Reader, string) error { return nil },
		SendText:          func(string) error { return nil },
		ImageWg:           &wg,
		ImageRunning:      &imageRunning,
		Ctx:               context.Background(),
		APIKey:            "test-key",
		Model:             "fal-ai/flux/schnell",
		Img2ImgModel:      "fal-ai/flux/dev/image-to-image",
		ReferenceImageURL: "", // no reference image available
		SafetyChecker:     false,
		TimeoutSeconds:    5,
		BaseURL:           falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test","use_reference_image":true}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	wg.Wait()

	// Should fall back to text-to-image (schnell), not img2img
	if capturedPath != "/fal-ai/flux/schnell" {
		t.Errorf("expected text-to-image fallback endpoint, got path %q", capturedPath)
	}
	var reqBody map[string]any
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if reqBody["image_url"] != nil {
		t.Errorf("expected no image_url in fallback request, got %v", reqBody["image_url"])
	}
}

func TestImageGenNoReferenceFlag(t *testing.T) {
	var capturedPath string
	var capturedBody []byte

	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"images":[{"url":"data:image/jpeg;base64,/9j/fake"}],"has_nsfw_concepts":[false]}`)
	}))
	defer falServer.Close()

	var imageRunning atomic.Bool
	var wg sync.WaitGroup

	deps := &tools.ImageGenDeps{
		SendImage:         func(string, io.Reader, string) error { return nil },
		SendText:          func(string) error { return nil },
		ImageWg:           &wg,
		ImageRunning:      &imageRunning,
		Ctx:               context.Background(),
		APIKey:            "test-key",
		Model:             "fal-ai/flux/schnell",
		Img2ImgModel:      "fal-ai/flux/dev/image-to-image",
		ReferenceImageURL: "data:image/jpeg;base64,/9j/ref", // reference available but flag not set
		SafetyChecker:     false,
		TimeoutSeconds:    5,
		BaseURL:           falServer.URL,
	}

	send := func(string) error { return nil }
	react := func(string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

	// use_reference_image is false (default) — should use text-to-image
	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	wg.Wait()

	if capturedPath != "/fal-ai/flux/schnell" {
		t.Errorf("expected text-to-image endpoint, got path %q", capturedPath)
	}
	var reqBody map[string]any
	if err := json.Unmarshal(capturedBody, &reqBody); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	if reqBody["image_url"] != nil {
		t.Errorf("expected no image_url when use_reference_image is false, got %v", reqBody["image_url"])
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

	// Mock fal.ai API
	falServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, deps)

	_, err := r.Dispatch(context.Background(), "generate_image", json.RawMessage(`{"prompt":"a sunset"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	wg.Wait()

	if receivedFilename != "generated.jpg" {
		t.Errorf("expected filename 'generated.jpg', got %q", receivedFilename)
	}
	if string(receivedData) != string(imageData) {
		t.Errorf("expected image data %q, got %q", imageData, receivedData)
	}
}
