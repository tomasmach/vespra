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
	"time"

	"github.com/tomasmach/mnemon-bot/config"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	Name       string     `json:"name,omitempty"`
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

type Client struct {
	cfg        *config.LLMConfig
	httpClient *http.Client
}

func New(cfg *config.LLMConfig) *Client {
	return &Client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: time.Duration(cfg.RequestTimeoutSeconds) * time.Second},
	}
}

func (c *Client) Chat(ctx context.Context, messages []Message, tools []ToolDefinition) (Choice, error) {
	body := map[string]any{
		"model":    c.cfg.Model,
		"messages": messages,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}

	respBody, err := c.post(ctx, "https://openrouter.ai/api/v1/chat/completions", body)
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
	body := map[string]any{
		"model": c.cfg.EmbeddingModel,
		"input": text,
	}

	respBody, err := c.post(ctx, "https://openrouter.ai/api/v1/embeddings", body)
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

var retryDelays = []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond}

// post sends a JSON POST request to the given URL with retry on transient errors.
// Returns the response body on success; the caller must close it.
func (c *Client) post(ctx context.Context, url string, body any) (io.ReadCloser, error) {
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

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.OpenRouterKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HTTP-Referer", "https://github.com/tomasmach/mnemon-bot")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue // all network errors are transient
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			resp.Body.Close()
			lastErr = fmt.Errorf("transient HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		return resp.Body, nil
	}
	return nil, lastErr
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
