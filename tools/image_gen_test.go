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
