// Package llm provides an OpenRouter HTTP client for chat completions and embeddings.
package llm

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/tomasmach/vespra/config"
)

type Message struct {
	Role         string        `json:"role"`
	Content      string        `json:"-"` // use MarshalJSON/UnmarshalJSON
	ContentParts []ContentPart `json:"-"` // use MarshalJSON
	ToolCallID   string        `json:"tool_call_id,omitempty"`
	ToolCalls    []ToolCall    `json:"tool_calls,omitempty"`
	Name         string        `json:"name,omitempty"`
	Refusal      string        `json:"-"` // set only on API responses; never sent back
}

// MarshalJSON serializes content as a string when no image parts are present,
// or as a content-part array when images are included (OpenAI vision format).
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.ContentParts) > 0 {
		return json.Marshal(struct {
			Role       string        `json:"role"`
			Content    []ContentPart `json:"content"`
			ToolCallID string        `json:"tool_call_id,omitempty"`
			ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
			Name       string        `json:"name,omitempty"`
		}{m.Role, m.ContentParts, m.ToolCallID, m.ToolCalls, m.Name})
	}
	return json.Marshal(struct {
		Role       string     `json:"role"`
		Content    string     `json:"content,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		Name       string     `json:"name,omitempty"`
	}{m.Role, m.Content, m.ToolCallID, m.ToolCalls, m.Name})
}

// UnmarshalJSON decodes a message from JSON, handling string content from API responses.
func (m *Message) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
		Name       string          `json:"name,omitempty"`
		Refusal    string          `json:"refusal,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ToolCallID = raw.ToolCallID
	m.ToolCalls = raw.ToolCalls
	m.Name = raw.Name
	m.Refusal = raw.Refusal
	if len(raw.Content) > 0 {
		var s string
		if err := json.Unmarshal(raw.Content, &s); err == nil {
			m.Content = s
		}
	}
	return nil
}

// ContentPart is a single element in a multimodal message content array.
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	VideoURL *VideoURL `json:"video_url,omitempty"`
}

// ImageURL holds the URL for an image content part.
type ImageURL struct {
	URL string `json:"url"`
}

// VideoURL holds the URL for a video content part.
type VideoURL struct {
	URL string `json:"url"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDefinition struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ChatResponse struct {
	ID       string   `json:"id"`
	Model    string   `json:"model"`
	Provider string   `json:"provider"`
	Choices  []Choice `json:"choices"`
	Usage    Usage    `json:"usage"`
}

type Usage struct {
	PromptTokens            int `json:"prompt_tokens"`
	CompletionTokens        int `json:"completion_tokens"`
	CompletionTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

type Choice struct {
	Message            Message         `json:"message"`
	FinishReason       string          `json:"finish_reason"`
	NativeFinishReason string          `json:"native_finish_reason"`
	Error              json.RawMessage `json:"error,omitempty"` // provider error reported inside a 200 response

	// Response-level metadata copied from ChatResponse for diagnostics.
	GenerationID string `json:"-"`
	Model        string `json:"-"`
	Provider     string `json:"-"`
	Usage        Usage  `json:"-"`
}

// LogAttrs returns response metadata for diagnosing empty or failed completions.
func (c Choice) LogAttrs() []any {
	return []any{
		"generation_id", c.GenerationID,
		"model", c.Model,
		"provider", c.Provider,
		"finish_reason", c.FinishReason,
		"native_finish_reason", c.NativeFinishReason,
		"content_len", len(c.Message.Content),
		"tool_calls", len(c.Message.ToolCalls),
		"refusal", c.Message.Refusal,
		"error", string(c.Error),
		"prompt_tokens", c.Usage.PromptTokens,
		"completion_tokens", c.Usage.CompletionTokens,
		"reasoning_tokens", c.Usage.CompletionTokensDetails.ReasoningTokens,
	}
}

// ChatOptions allows per-request provider and model overrides.
// A nil pointer or zero value means "use global defaults".
type ChatOptions struct {
	Provider   string            // "openrouter" | "glm" | "fireworks" | "" (use global)
	Model      string            // override model name; "" = use global
	ExtraTools []json.RawMessage // raw tool objects appended to the tools array (e.g. GLM native tools)
	MaxTokens  int               // max_tokens cap for this request; 0 means no cap
	// ReasoningEffort is sent as OpenRouter's reasoning.effort ("low", "medium", ...);
	// "" leaves the model default. Ignored for vision, GLM and Fireworks requests.
	ReasoningEffort string
}

type Client struct {
	cfgStore          *config.Store
	openRouterBaseURL string // for testing: overrides the hardcoded OpenRouter endpoint

	mu         sync.Mutex
	imageInput map[string]bool // whether a model takes image input, keyed by API base and model
}

func New(cfgStore *config.Store) *Client {
	return &Client{
		cfgStore:   cfgStore,
		imageInput: make(map[string]bool),
	}
}

func (c *Client) apiBase() string {
	if u := c.cfgStore.Get().LLM.BaseURL; u != "" {
		return u
	}
	return "https://openrouter.ai/api/v1"
}

func (c *Client) chatKey() string {
	return c.cfgStore.Get().LLM.OpenRouterKey
}

func (c *Client) embeddingBase() string {
	if u := c.cfgStore.Get().LLM.EmbeddingBaseURL; u != "" {
		return u
	}
	return c.apiBase()
}

// route is the endpoint, key and model a chat request goes to.
type route struct {
	apiBase string
	apiKey  string
	model   string
}

// mainRoute resolves where a main chat request goes, applying the per-request
// provider and model overrides in opts.
func (c *Client) mainRoute(opts *ChatOptions) route {
	cfg := c.cfgStore.Get().LLM
	r := route{apiBase: c.apiBase(), apiKey: c.chatKey(), model: cfg.Model}
	if opts == nil {
		return r
	}
	switch opts.Provider {
	case "openrouter":
		if c.openRouterBaseURL != "" {
			r.apiBase = c.openRouterBaseURL
		} else {
			r.apiBase = "https://openrouter.ai/api/v1"
		}
	case "glm":
		r.apiBase = cfg.GLMBaseURL
		r.apiKey = cfg.GLMKey
		// Only reset the model when the global model is not a GLM model,
		// to avoid sending e.g. an OpenRouter model name to the GLM API.
		if !strings.HasPrefix(r.model, "glm-") {
			r.model = "glm-4.7"
		}
	case "fireworks":
		r.apiBase = cfg.FireworksBaseURL
		r.apiKey = cfg.FireworksKey
	}
	if opts.Model != "" {
		r.model = opts.Model
	}
	return r
}

// visionRoute resolves where requests for the configured vision model go: the
// OpenRouter endpoint and key by default, or VisionBaseURL, which uses the GLM
// key when it matches the GLM base.
func (c *Client) visionRoute() route {
	cfg := c.cfgStore.Get().LLM
	r := route{apiBase: c.apiBase(), apiKey: c.chatKey(), model: cfg.VisionModel}
	if cfg.VisionBaseURL != "" {
		r.apiBase = cfg.VisionBaseURL
		if cfg.VisionBaseURL == cfg.GLMBaseURL {
			r.apiKey = cfg.GLMKey
		}
	}
	return r
}

// SeesImages reports whether the main chat model for opts takes image input.
// Chat then sends it images directly in every step of a turn instead of
// routing them to the vision model.
func (c *Client) SeesImages(ctx context.Context, opts *ChatOptions) bool {
	return c.seesImages(ctx, c.mainRoute(opts))
}

// seesImages reports whether the model behind r takes image input, according
// to its OpenRouter model metadata. Answers are cached per API base and model.
// GLM and Fireworks publish no such metadata, and a failed lookup counts as no,
// so media then goes through the vision model.
func (c *Client) seesImages(ctx context.Context, r route) bool {
	cfg := c.cfgStore.Get().LLM
	if r.apiBase == "" || r.apiBase == cfg.GLMBaseURL || r.apiBase == cfg.FireworksBaseURL {
		return false
	}
	key := r.apiBase + " " + r.model
	c.mu.Lock()
	sees, ok := c.imageInput[key]
	c.mu.Unlock()
	if ok {
		return sees
	}
	sees, err := c.fetchImageInput(ctx, r)
	if err != nil {
		slog.Warn("model metadata lookup failed; media goes through the vision model", "model", r.model, "error", err)
		return false
	}
	slog.Info("model image input", "model", r.model, "base_url", r.apiBase, "sees_images", sees)
	c.mu.Lock()
	c.imageInput[key] = sees
	c.mu.Unlock()
	return sees
}

// fetchImageInput reads the model's input modalities from OpenRouter's
// /models/{model}/endpoints. A 404 means the endpoint has no metadata for the
// model (e.g. a custom base_url) and counts as no image input.
func (c *Client) fetchImageInput(ctx context.Context, r route) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.apiBase+"/models/"+r.model+"/endpoints", nil)
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var meta struct {
		Data struct {
			Architecture struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}
	return slices.Contains(meta.Data.Architecture.InputModalities, "image"), nil
}

func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, opts *ChatOptions) (Choice, error) {
	cfg := c.cfgStore.Get().LLM
	r := c.mainRoute(opts)

	last := len(messages) - 1
	vision := false
	switch {
	case !messagesHaveImages(messages):
	case c.seesImages(ctx, r):
		// The main model sees the images itself, in every step of the turn.
		// Only video, which it cannot take, is replaced by a note.
		messages = stripMedia(messages, true)
	case len(messages[last].ContentParts) > 0 && cfg.VisionModel != "":
		vision = true
		// Vision model takes priority over per-agent provider.
		r = c.visionRoute()
		// Strip stale media from older history messages — only the current
		// message needs its content parts; re-sending old base64 blobs wastes
		// tokens and may confuse the vision model.
		if last > 0 && messagesHaveImages(messages[:last]) {
			stripped := stripMedia(messages[:last], false)
			messages = append(stripped, messages[last])
		}
	default:
		messages = stripMedia(messages, false)
	}

	// GLM doesn't support the OpenAI multimodal content format for
	// non-vision models. Strip images that would otherwise be sent to
	// a GLM endpoint with a model that isn't the configured vision model.
	if cfg.GLMBaseURL != "" && r.apiBase == cfg.GLMBaseURL &&
		r.model != cfg.VisionModel && messagesHaveImages(messages) {
		messages = stripMedia(messages, false)
	}

	return c.complete(ctx, r, messages, tools, opts, vision)
}

// complete sends one chat completion request to r and returns the first choice.
// vision marks a request to the vision model, which gets no reasoning effort.
func (c *Client) complete(ctx context.Context, r route, messages []Message, tools []ToolDefinition, opts *ChatOptions, vision bool) (Choice, error) {
	cfg := c.cfgStore.Get().LLM
	body := map[string]any{
		"model":    r.model,
		"messages": messages,
	}
	if opts != nil && opts.MaxTokens > 0 {
		body["max_tokens"] = opts.MaxTokens
	}
	if opts != nil && opts.ReasoningEffort != "" && !vision && r.apiBase != cfg.GLMBaseURL && r.apiBase != cfg.FireworksBaseURL {
		body["reasoning"] = map[string]string{"effort": opts.ReasoningEffort}
	}

	// GLM vision models don't support function-calling tools alongside
	// multimodal content. Omit tools when the request goes to GLM with images.
	glmVision := cfg.GLMBaseURL != "" && r.apiBase == cfg.GLMBaseURL && messagesHaveImages(messages)

	if !glmVision {
		if opts != nil && len(opts.ExtraTools) > 0 {
			combined := make([]json.RawMessage, 0, len(tools)+len(opts.ExtraTools))
			for _, t := range tools {
				b, err := json.Marshal(t)
				if err != nil {
					return Choice{}, fmt.Errorf("marshal tool definition: %w", err)
				}
				combined = append(combined, b)
			}
			combined = append(combined, opts.ExtraTools...)
			body["tools"] = combined
		} else if len(tools) > 0 {
			body["tools"] = tools
		}
	}

	slog.Debug("llm chat dispatch", "model", r.model, "base_url", r.apiBase)
	respBody, err := c.post(ctx, r.apiBase+"/chat/completions", r.apiKey, body)
	if err != nil {
		return Choice{}, err
	}
	defer respBody.Close()

	var result ChatResponse
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return Choice{}, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Choices) == 0 {
		return Choice{}, fmt.Errorf("no choices in response")
	}
	choice := result.Choices[0]
	choice.GenerationID = result.ID
	choice.Model = result.Model
	choice.Provider = result.Provider
	choice.Usage = result.Usage
	slog.Debug("llm response", choice.LogAttrs()...)
	return choice, nil
}

const mediaDescriptionPrompt = `You describe media for an assistant that cannot see it. The assistant answers the user's message using only your description, so include everything it needs.

- Transcribe all readable text verbatim: comments, captions, usernames, labels, meme text. Keep who wrote what.
- Describe people, objects, setting, and anything the user's message asks about.
- Stay factual. Never guess unreadable text or unclear details; say they are unreadable.
- Keep it short when the media is simple. When there are several images or videos, describe each one separately.`

// DescribeMedia makes a lightweight vision call to produce a text description
// of the given media content parts. Returns "" if no vision model is configured.
func (c *Client) DescribeMedia(ctx context.Context, parts []ContentPart) (string, error) {
	// cfg is re-read here independently of the caller's snapshot — intentional
	// double-read for simplicity; worst case is a no-op if the model just changed.
	cfg := c.cfgStore.Get().LLM
	if cfg.VisionModel == "" {
		return "", nil
	}
	messages := []Message{
		{Role: "system", Content: mediaDescriptionPrompt},
		{Role: "user", ContentParts: parts},
	}
	choice, err := c.complete(ctx, c.visionRoute(), messages, nil, nil, true)
	if err != nil {
		return "", err
	}
	return choice.Message.Content, nil
}

func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	cfg := c.cfgStore.Get().LLM
	body := map[string]any{
		"model": cfg.EmbeddingModel,
		"input": text,
	}

	respBody, err := c.post(ctx, c.embeddingBase()+"/embeddings", c.chatKey(), body)
	if err != nil {
		return nil, err
	}
	defer respBody.Close()

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("no embedding data in response")
	}
	return result.Data[0].Embedding, nil
}

// cancelOnClose wraps an io.ReadCloser to call a cancel function on Close.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

var retryDelays = []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond}

// post sends a JSON POST request to the given URL with retry on transient errors.
// Returns the response body on success; the caller must close it.
func (c *Client) post(ctx context.Context, url, key string, body any) (io.ReadCloser, error) {
	cfg := c.cfgStore.Get()
	timeout := time.Duration(cfg.LLM.RequestTimeoutSeconds) * time.Second

	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	slog.Debug("llm request", "url", url, "body", string(data))

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryDelays[attempt-1]):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		attemptCtx, attemptCancel := context.WithTimeout(ctx, timeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			attemptCancel()
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HTTP-Referer", "https://github.com/tomasmach/vespra")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			attemptCancel()
			lastErr = err
			continue // all network errors are transient
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			attemptCancel()
			lastErr = fmt.Errorf("transient HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			attemptCancel()
			slog.Error("llm request failed",
				"url", url,
				"status", resp.StatusCode,
				"request_body", string(data),
				"response_body", strings.TrimSpace(string(respBody)),
			)
			return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
		}

		// Cancel the per-attempt context when the caller closes the body,
		// not before — the context must remain live while the body is being read.
		return &cancelOnClose{ReadCloser: resp.Body, cancel: attemptCancel}, nil
	}
	return nil, lastErr
}

// messagesHaveImages reports whether any message contains image content parts.
func messagesHaveImages(messages []Message) bool {
	for i := range messages {
		if len(messages[i].ContentParts) > 0 {
			return true
		}
	}
	return false
}

// stripMedia returns a copy of messages without the media parts the model
// cannot take: videos always, images unless keepImages is set. Each message
// that loses media gets a short text note so the model knows it was shared
// even though it cannot see it.
func stripMedia(messages []Message, keepImages bool) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		if len(out[i].ContentParts) == 0 {
			continue
		}
		var text string
		var images []ContentPart
		var imageCount, videoCount int
		for _, p := range out[i].ContentParts {
			switch p.Type {
			case "text":
				text = p.Text
			case "image_url":
				if keepImages {
					images = append(images, p)
				} else {
					imageCount++
				}
			case "video_url":
				videoCount++
			}
		}
		if imageCount == 0 && videoCount == 0 {
			continue
		}
		var note string
		switch {
		case imageCount > 0 && videoCount > 0:
			note = fmt.Sprintf("[%d image(s) and %d video(s) attached — vision not supported by current model]", imageCount, videoCount)
		case imageCount > 0:
			note = fmt.Sprintf("[%d image(s) attached — vision not supported by current model]", imageCount)
		case videoCount > 0:
			note = fmt.Sprintf("[%d video(s) attached — vision not supported by current model]", videoCount)
		}
		if text != "" {
			text += "\n" + note
		} else {
			text = note
		}
		if len(images) > 0 {
			out[i].ContentParts = append([]ContentPart{{Type: "text", Text: text}}, images...)
			continue
		}
		out[i].ContentParts = nil
		out[i].Content = text
	}
	return out
}

// VectorToBlob converts float32 slice to little-endian bytes.
func VectorToBlob(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// BlobToVector converts little-endian bytes to float32 slice.
func BlobToVector(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
