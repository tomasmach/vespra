// Package tools defines the AI tool registry and tool implementations.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/memory"
)

// Tool is the interface every tool must implement.
type Tool interface {
	Name() string
	Description() string
	Parameters() json.RawMessage
	Call(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds registered tools and provides dispatch.
type Registry struct {
	tools     map[string]Tool
	Replied   bool   // set to true when the reply tool is called
	ReplyText string // the content argument passed to the reply tool
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Definitions returns all registered tools as LLM tool definitions.
func (r *Registry) Definitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, llm.ToolDefinition{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return defs
}

// Dispatch calls the named tool with the given args.
func (r *Registry) Dispatch(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Call(ctx, args)
}

// SendFunc sends a text message to the channel.
type SendFunc func(content string) error

// ReactFunc adds an emoji reaction to the triggering message.
type ReactFunc func(emoji string) error

type memorySaveTool struct {
	store    *memory.Store
	serverID string
}

func (t *memorySaveTool) Name() string { return "memory_save" }
func (t *memorySaveTool) Description() string {
	return "Save a fact or observation to long-term memory."
}
func (t *memorySaveTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "content": {"type": "string", "description": "The memory content to save."},
            "user_id": {"type": "string", "description": "Optional Discord user ID this memory is about."},
            "importance": {"type": "number", "description": "Importance score 0.0-1.0, default 0.5."}
        },
        "required": ["content"]
    }`)
}
func (t *memorySaveTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Content    string  `json:"content"`
		UserID     string  `json:"user_id"`
		Importance float64 `json:"importance"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Importance == 0 {
		p.Importance = 0.5
	}
	id, err := t.store.Save(ctx, p.Content, t.serverID, p.UserID, "", p.Importance)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Memory saved (id: %s)", id), nil
}

type memoryRecallTool struct {
	store    *memory.Store
	serverID string
}

func (t *memoryRecallTool) Name() string { return "memory_recall" }
func (t *memoryRecallTool) Description() string {
	return "Search long-term memory for relevant facts."
}
func (t *memoryRecallTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "query": {"type": "string", "description": "Search query."},
            "top_n": {"type": "integer", "description": "Max results to return, default 10."}
        },
        "required": ["query"]
    }`)
}
func (t *memoryRecallTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		TopN  int    `json:"top_n"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.TopN == 0 {
		p.TopN = 10
	}
	rows, err := t.store.Recall(ctx, p.Query, t.serverID, p.TopN)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "No memories found.", nil
	}
	var sb strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&sb, "[%s] (importance: %.1f) %s\n", r.ID, r.Importance, r.Content)
	}
	return sb.String(), nil
}

type memoryForgetTool struct {
	store    *memory.Store
	serverID string
}

func (t *memoryForgetTool) Name() string { return "memory_forget" }
func (t *memoryForgetTool) Description() string {
	return "Soft-delete a memory so it no longer appears in searches."
}
func (t *memoryForgetTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "memory_id": {"type": "string", "description": "ID of the memory to forget."}
        },
        "required": ["memory_id"]
    }`)
}
func (t *memoryForgetTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.store.Forget(ctx, t.serverID, p.MemoryID); err != nil {
		return "", err
	}
	return "Memory forgotten.", nil
}

type replyTool struct {
	send      SendFunc
	replied   *bool
	replyText *string
}

func (t *replyTool) Name() string { return "reply" }
func (t *replyTool) Description() string {
	return "Send a text reply to the Discord channel."
}
func (t *replyTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "content": {"type": "string", "description": "The message content to send."}
        },
        "required": ["content"]
    }`)
}
func (t *replyTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	parts := SplitMessage(p.Content, 2000)
	for _, part := range parts {
		if err := t.send(part); err != nil {
			return "", err
		}
	}
	*t.replied = true
	*t.replyText = p.Content
	return "Replied.", nil
}

// SplitMessage splits s into chunks of at most limit Unicode code points.
func SplitMessage(s string, limit int) []string {
	if utf8.RuneCountInString(s) <= limit {
		return []string{s}
	}
	var parts []string
	runes := []rune(s)
	for len(runes) > 0 {
		end := limit
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}

type reactTool struct {
	react ReactFunc
}

func (t *reactTool) Name() string { return "react" }
func (t *reactTool) Description() string {
	return "Add an emoji reaction to the user's message."
}
func (t *reactTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "emoji": {"type": "string", "description": "The emoji to react with."}
        },
        "required": ["emoji"]
    }`)
}
func (t *reactTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Emoji string `json:"emoji"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	return "Reacted.", t.react(p.Emoji)
}

type webSearchTool struct {
	apiKey string
}

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the web for current information using Brave Search."
}
func (t *webSearchTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "query": {"type": "string", "description": "The search query."}
        },
        "required": ["query"]
    }`)
}
func (t *webSearchTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	endpoint := "https://api.search.brave.com/res/v1/web/search"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	q := url.Values{}
	q.Set("q", p.Query)
	q.Set("count", "5")
	req.URL.RawQuery = q.Encode()
	req.Header.Set("X-Subscription-Token", t.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("search HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	var result struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if len(result.Web.Results) == 0 {
		return "No results found.", nil
	}

	var sb strings.Builder
	for _, r := range result.Web.Results {
		fmt.Fprintf(&sb, "%s\n%s\n%s\n\n", r.Title, r.URL, r.Description)
	}
	return strings.TrimSpace(sb.String()), nil
}

// NewDefaultRegistry creates a registry with standard tools.
// If webSearchKey is non-empty, the web_search tool is also registered.
func NewDefaultRegistry(store *memory.Store, serverID string, send SendFunc, react ReactFunc, webSearchKey string) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID})
	r.Register(&memoryRecallTool{store: store, serverID: serverID})
	r.Register(&memoryForgetTool{store: store, serverID: serverID})
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText})
	r.Register(&reactTool{react: react})
	if webSearchKey != "" {
		r.Register(&webSearchTool{apiKey: webSearchKey})
	}
	return r
}
