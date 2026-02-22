// Package llm provides an OpenRouter HTTP client for chat completions and embeddings.
package llm

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
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
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ToolCallID = raw.ToolCallID
	m.ToolCalls = raw.ToolCalls
	m.Name = raw.Name
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
}

// ImageURL holds the URL for an image content part.
type ImageURL struct {
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
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatOptions allows per-request provider and model overrides.
// A nil pointer or zero value means "use global defaults".
type ChatOptions struct {
	Provider string // "openrouter" | "glm" | "" (use global)
	Model    string // override model name; "" = use global
}

type Client struct {
	cfgStore          *config.Store
	openRouterBaseURL string // for testing: overrides the hardcoded OpenRouter endpoint
}

func New(cfgStore *config.Store) *Client {
	return &Client{
		cfgStore: cfgStore,
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

func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDefinition, opts *ChatOptions) (Choice, error) {
	cfg := c.cfgStore.Get().LLM
	model := cfg.Model
	apiBase := c.apiBase()
	apiKey := c.chatKey()

	// Apply per-request provider override before vision logic.
	if opts != nil {
		switch opts.Provider {
		case "openrouter":
			if c.openRouterBaseURL != "" {
				apiBase = c.openRouterBaseURL
			} else {
				apiBase = "https://openrouter.ai/api/v1"
			}
		case "glm":
			apiBase = cfg.GLMBaseURL
			apiKey = cfg.GLMKey
		}
		if opts.Model != "" {
			model = opts.Model
		}
	}

	hasPerAgentProvider := opts != nil && opts.Provider != ""
	last := len(messages) - 1
	switch {
	case last >= 0 && len(messages[last].ContentParts) > 0 && cfg.VisionModel != "" && !hasPerAgentProvider:
		model = cfg.VisionModel
		if cfg.VisionBaseURL != "" {
			apiBase = cfg.VisionBaseURL
		}
	case cfg.VisionModel == "" && messagesHaveImages(messages):
		messages = stripImages(messages)
	}
	body := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}

	respBody, err := c.post(ctx, apiBase+"/chat/completions", apiKey, body)
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
	return result.Choices[0], nil
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

// stripImages returns a copy of messages with image content parts removed.
// Each stripped message gets a short text note so the model knows an image was
// shared even though it cannot see it.
func stripImages(messages []Message) []Message {
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		if len(out[i].ContentParts) == 0 {
			continue
		}
		var text string
		var imageCount int
		for _, p := range out[i].ContentParts {
			switch p.Type {
			case "text":
				text = p.Text
			case "image_url":
				imageCount++
			}
		}
		if imageCount == 0 {
			continue
		}
		note := fmt.Sprintf("[%d image(s) attached — vision not supported by current model]", imageCount)
		if text != "" {
			text += "\n" + note
		} else {
			text = note
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
