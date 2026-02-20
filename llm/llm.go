package llm

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
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

	delays := []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := delays[attempt-1]
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return Choice{}, ctx.Err()
			}
		}

		data, err := json.Marshal(body)
		if err != nil {
			return Choice{}, fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", bytes.NewReader(data))
		if err != nil {
			return Choice{}, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.OpenRouterKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HTTP-Referer", "https://github.com/tomasmach/mnemon-bot")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if isTransient(0, err) {
				lastErr = err
				continue
			}
			return Choice{}, err
		}

		if isTransient(resp.StatusCode, nil) {
			resp.Body.Close()
			lastErr = fmt.Errorf("transient HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return Choice{}, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		var result ChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return Choice{}, fmt.Errorf("decode response: %w", err)
		}
		resp.Body.Close()

		if len(result.Choices) == 0 {
			return Choice{}, fmt.Errorf("no choices in response")
		}
		return result.Choices[0], nil
	}
	return Choice{}, lastErr
}

func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body := map[string]any{
		"model": c.cfg.EmbeddingModel,
		"input": text,
	}

	delays := []time.Duration{500 * time.Millisecond, 1000 * time.Millisecond}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := delays[attempt-1]
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://openrouter.ai/api/v1/embeddings", bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.cfg.OpenRouterKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HTTP-Referer", "https://github.com/tomasmach/mnemon-bot")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if isTransient(0, err) {
				lastErr = err
				continue
			}
			return nil, err
		}

		if isTransient(resp.StatusCode, nil) {
			resp.Body.Close()
			lastErr = fmt.Errorf("transient HTTP %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
		}

		var result struct {
			Data []struct {
				Embedding []float32 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode response: %w", err)
		}
		resp.Body.Close()

		if len(result.Data) == 0 {
			return nil, fmt.Errorf("no embedding data in response")
		}
		return result.Data[0].Embedding, nil
	}
	return nil, lastErr
}

func isTransient(statusCode int, err error) bool {
	if err != nil {
		return true
	}
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if statusCode >= 500 {
		return true
	}
	return false
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
