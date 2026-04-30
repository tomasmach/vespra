package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// SendImageFunc sends an image file attachment to the Discord channel.
type SendImageFunc func(filename string, data io.Reader, caption string) error

// ImageGenDeps groups dependencies for the async image generation tool.
// Pass nil to NewDefaultRegistry to omit image generation from the registry.
type ImageGenDeps struct {
	SendImage       SendImageFunc
	SendText        SendFunc
	ImageWg         *sync.WaitGroup
	ImageRunning    *atomic.Bool
	Ctx             context.Context
	APIKey          string
	Model           string
	EditModel       string
	SourceImageURLs []string
	SafetyChecker   bool
	TimeoutSeconds  int
	Resolution      string
	BaseURL         string // for testing; overrides https://fal.run
}

type imageGenTool struct {
	deps        *ImageGenDeps
	imageCalled *bool
}

const maxImageEditSourceURLs = 14

func (t *imageGenTool) Name() string { return ToolNameImageGen }
func (t *imageGenTool) Description() string {
	return "Generate an image from a text prompt, or edit attached/replied-to source images. Call this tool whenever the user asks you to draw, create, make, generate, visualize, show, edit, transform, or change an image or picture — including requests phrased as 'make an image of X', 'show me what X looks like', 'draw X', 'edit this', 'change this image', or similar. " +
		"Do NOT describe the image generation in your text — always call this tool first. " +
		"Include a brief status message as inline text content alongside this tool call (e.g. 'Generating your image…') — do NOT call the reply tool separately after this one."
}
func (t *imageGenTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {"type": "string", "description": "A detailed English prompt describing the image to generate."},
			"mode": {"type": "string", "description": "Use edit when modifying attached or replied-to source images; otherwise use generate. Options: generate, edit."},
			"aspect_ratio": {"type": "string", "description": "Image aspect ratio. Options: auto, 21:9, 16:9, 3:2, 4:3, 5:4, 1:1, 4:5, 3:4, 2:3, 9:16. Default: auto."},
			"image_size": {"type": "string", "description": "Deprecated legacy aspect ratio; accepted for backwards compatibility."}
		},
		"required": ["prompt"]
	}`)
}

func (t *imageGenTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Prompt      string `json:"prompt"`
		Mode        string `json:"mode"`
		AspectRatio string `json:"aspect_ratio"`
		ImageSize   string `json:"image_size"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Prompt == "" {
		return "Error: prompt is required", nil
	}
	aspectRatio := p.AspectRatio
	if aspectRatio == "" {
		aspectRatio = legacyImageSizeToAspectRatio(p.ImageSize)
	}
	if aspectRatio == "" {
		aspectRatio = "auto"
	}
	mode := p.Mode
	if mode == "" {
		mode = "generate"
	}
	if mode != "generate" && mode != "edit" {
		return "Error: mode must be either generate or edit", nil
	}
	sourceImageURLs := t.deps.SourceImageURLs
	if len(sourceImageURLs) > maxImageEditSourceURLs {
		sourceImageURLs = sourceImageURLs[:maxImageEditSourceURLs]
	}
	if mode == "edit" && len(sourceImageURLs) == 0 {
		return "Image editing requires an attached or replied-to image.", nil
	}

	if !t.deps.ImageRunning.CompareAndSwap(false, true) {
		return "An image generation is already running, please wait.", nil
	}
	*t.imageCalled = true

	t.deps.ImageWg.Add(1)
	go t.runGenerate(p.Prompt, aspectRatio, mode, sourceImageURLs)
	return fmt.Sprintf("Image generation started for prompt: %q — the image will be sent shortly.", p.Prompt), nil
}

func legacyImageSizeToAspectRatio(imageSize string) string {
	switch imageSize {
	case "square_hd", "square":
		return "1:1"
	case "portrait_4_3":
		return "3:4"
	case "portrait_16_9":
		return "9:16"
	case "landscape_4_3":
		return "4:3"
	case "landscape_16_9":
		return "16:9"
	default:
		return imageSize
	}
}

type falRequest struct {
	Prompt             string   `json:"prompt"`
	NumImages          int      `json:"num_images"`
	AspectRatio        string   `json:"aspect_ratio"`
	OutputFormat       string   `json:"output_format"`
	SafetyTolerance    string   `json:"safety_tolerance,omitempty"`
	ImageURLs          []string `json:"image_urls,omitempty"`
	Resolution         string   `json:"resolution"`
	LimitGenerations   bool     `json:"limit_generations"`
	EnableWebSearch    bool     `json:"enable_web_search,omitempty"`
	EnableGoogleSearch bool     `json:"enable_google_search,omitempty"`
}

type falResponse struct {
	Images []struct {
		URL string `json:"url"`
	} `json:"images"`
	HasNSFWConcepts []bool `json:"has_nsfw_concepts"`
}

func (t *imageGenTool) runGenerate(prompt, aspectRatio, mode string, sourceImageURLs []string) {
	defer t.deps.ImageWg.Done()
	defer t.deps.ImageRunning.Store(false)

	model := t.deps.Model
	if mode == "edit" && t.deps.EditModel != "" {
		model = t.deps.EditModel
	}
	slog.Debug("image gen goroutine started", "prompt", prompt, "model", model, "mode", mode, "timeout", t.deps.TimeoutSeconds)

	ctx, cancel := context.WithTimeout(t.deps.Ctx, time.Duration(t.deps.TimeoutSeconds)*time.Second)
	defer cancel()

	resolution := t.deps.Resolution
	if resolution == "" {
		resolution = "1K"
	}
	reqBody := falRequest{
		Prompt:           prompt,
		NumImages:        1,
		AspectRatio:      aspectRatio,
		OutputFormat:     "png",
		Resolution:       resolution,
		LimitGenerations: true,
	}
	if mode == "edit" {
		reqBody.ImageURLs = sourceImageURLs
	}
	if t.deps.SafetyChecker {
		reqBody.SafetyTolerance = "4"
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		slog.Error("image gen marshal error", "error", err)
		if err := t.deps.SendText(fmt.Sprintf("Failed to generate image: %s", err)); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}

	baseURL := t.deps.BaseURL
	if baseURL == "" {
		baseURL = "https://fal.run"
	}
	url := fmt.Sprintf("%s/%s", baseURL, model)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		slog.Error("image gen request creation error", "error", err)
		if err := t.deps.SendText(fmt.Sprintf("Failed to generate image: %s", err)); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}
	req.Header.Set("Authorization", "Key "+t.deps.APIKey)
	req.Header.Set("Content-Type", "application/json")

	slog.Debug("image gen calling fal API", "url", url)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("image gen API call failed", "error", err, "prompt", prompt)
		if err := t.deps.SendText(fmt.Sprintf("Failed to generate image: %s", err)); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		slog.Error("image gen API error", "status", resp.StatusCode, "body", string(body), "prompt", prompt)
		if err := t.deps.SendText(fmt.Sprintf("Failed to generate image (HTTP %d).", resp.StatusCode)); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}

	var falResp falResponse
	if err := json.NewDecoder(resp.Body).Decode(&falResp); err != nil {
		slog.Error("image gen response decode error", "error", err)
		if err := t.deps.SendText("Failed to generate image: could not parse response."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}

	slog.Debug("image gen fal response", "images", len(falResp.Images), "nsfw", falResp.HasNSFWConcepts, "safety_checker", t.deps.SafetyChecker)

	if t.deps.SafetyChecker && (mode != "edit" || len(falResp.HasNSFWConcepts) > 0) {
		if len(falResp.HasNSFWConcepts) == 0 {
			slog.Warn("image gen safety check: has_nsfw_concepts absent, blocking image", "prompt", prompt)
			if err := t.deps.SendText("The generated image could not be safety-checked and was not sent."); err != nil {
				slog.Warn("image gen notify failed", "error", err)
			}
			return
		}
		if falResp.HasNSFWConcepts[0] {
			slog.Warn("image gen NSFW content blocked", "prompt", prompt)
			if err := t.deps.SendText("The generated image was flagged as inappropriate and was not sent."); err != nil {
				slog.Warn("image gen notify failed", "error", err)
			}
			return
		}
	}

	if len(falResp.Images) == 0 || falResp.Images[0].URL == "" {
		slog.Error("image gen returned no images", "prompt", prompt)
		if err := t.deps.SendText("Failed to generate image: no image was returned."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}

	// Download the image immediately (fal.ai URLs expire).
	imgReq, err := http.NewRequestWithContext(ctx, http.MethodGet, falResp.Images[0].URL, nil)
	if err != nil {
		slog.Error("image download request error", "error", err)
		if err := t.deps.SendText("Failed to download generated image."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}
	imgResp, err := http.DefaultClient.Do(imgReq)
	if err != nil {
		slog.Error("image download failed", "error", err)
		if err := t.deps.SendText("Failed to download generated image."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}
	defer imgResp.Body.Close()

	if imgResp.StatusCode != http.StatusOK {
		slog.Error("image download HTTP error", "status", imgResp.StatusCode)
		if err := t.deps.SendText("Failed to download generated image."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}

	imgData, err := io.ReadAll(io.LimitReader(imgResp.Body, 20*1024*1024)) // 20 MB max
	if err != nil {
		slog.Error("image read error", "error", err)
		if err := t.deps.SendText("Failed to read generated image."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
		return
	}

	// When safety checker is disabled, send NSFW images as spoilers.
	filename := "generated.png"
	if !t.deps.SafetyChecker && len(falResp.HasNSFWConcepts) > 0 && falResp.HasNSFWConcepts[0] {
		filename = "SPOILER_generated.png"
	}
	slog.Debug("image gen sending to Discord", "filename", filename, "size", len(imgData))
	if err := t.deps.SendImage(filename, bytes.NewReader(imgData), ""); err != nil {
		slog.Error("image send to Discord failed", "error", err)
		if err := t.deps.SendText("Failed to send the generated image."); err != nil {
			slog.Warn("image gen notify failed", "error", err)
		}
	} else {
		slog.Info("image gen completed successfully", "prompt", prompt, "size", len(imgData))
	}
}
