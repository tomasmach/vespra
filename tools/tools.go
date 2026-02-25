// Package tools defines the AI tool registry and tool implementations.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/memory"
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
	return "Save important information to long-term memory. " +
		"Use this for: user preferences or opinions, personal facts (location, job, hobbies, relationships), " +
		"decisions made, goals or plans, tasks to follow up on, or anything the user explicitly asks to remember. " +
		"Check memory_recall first to avoid saving duplicates."
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
		Content    string   `json:"content"`
		UserID     string   `json:"user_id"`
		Importance *float64 `json:"importance"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	importance := 0.5
	if p.Importance != nil {
		importance = *p.Importance
	}
	id, err := t.store.Save(ctx, p.Content, t.serverID, p.UserID, "", importance)
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
	return "Search long-term memory for relevant facts. " +
		"Call this proactively when the topic might connect to something already saved, " +
		"and before calling memory_save to check for duplicates."
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
		if errors.Is(err, memory.ErrMemoryNotFound) {
			return "Memory not found.", nil
		}
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

// utf16Len returns the number of UTF-16 code units for a rune.
func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2 // surrogate pair
	}
	return 1
}

// SplitMessage splits s into chunks of at most limit UTF-16 code units,
// matching Discord's 2000-character limit which is measured in UTF-16 units.
func SplitMessage(s string, limit int) []string {
	// fast path: count total UTF-16 units
	total := 0
	for _, r := range s {
		total += utf16Len(r)
	}
	if total <= limit {
		return []string{s}
	}
	var parts []string
	var buf strings.Builder
	units := 0
	for _, r := range s {
		rLen := utf16Len(r)
		if units+rLen > limit {
			parts = append(parts, buf.String())
			buf.Reset()
			units = 0
		}
		buf.WriteRune(r)
		units += rLen
	}
	if buf.Len() > 0 {
		parts = append(parts, buf.String())
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

// WebSearchDeps groups dependencies for the async web search tool.
// Pass nil to NewDefaultRegistry to omit web search from the registry.
type WebSearchDeps struct {
	DeliverResult  func(result string) // injects results back into agent
	LLM            *llm.Client
	Model          string
	Ctx            context.Context
	SearchWg       *sync.WaitGroup
	SearchRunning  *atomic.Bool
	TimeoutSeconds int
}

type webSearchTool struct {
	deps *WebSearchDeps
}

func (t *webSearchTool) Name() string { return "web_search" }
func (t *webSearchTool) Description() string {
	return "Search the web for current information. This is an async operation — " +
		"results will be delivered in a follow-up message. After calling this tool, " +
		"acknowledge to the user that you are searching (in their language)."
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
	if p.Query == "" {
		return "Error: query is required", nil
	}
	if !t.deps.SearchRunning.CompareAndSwap(false, true) {
		return "A web search is already running, please wait for results.", nil
	}

	t.deps.SearchWg.Add(1)
	go t.runSearch(p.Query)
	return fmt.Sprintf("Web search started for: %q — results will arrive shortly.", p.Query), nil
}

func (t *webSearchTool) runSearch(query string) {
	defer t.deps.SearchWg.Done()
	defer t.deps.SearchRunning.Store(false)

	ctx, cancel := context.WithTimeout(t.deps.Ctx, time.Duration(t.deps.TimeoutSeconds)*time.Second)
	defer cancel()

	messages := []llm.Message{
		{Role: "user", Content: fmt.Sprintf("Search the web for: %s\n\nReturn comprehensive results with titles, URLs, and detailed content summaries.", query)},
	}

	glmSearchSpec := json.RawMessage(`{"type":"web_search","web_search":{"enable":true,"search_result":true,"search_engine":"search_pro","content_size":"high"}}`)
	opts := &llm.ChatOptions{
		Provider:   "glm",
		Model:      t.deps.Model,
		ExtraTools: []json.RawMessage{glmSearchSpec},
	}

	choice, err := t.deps.LLM.Chat(ctx, messages, nil, opts)
	if err != nil {
		slog.Error("web search background call failed", "error", err, "query", query)
		t.deps.DeliverResult(fmt.Sprintf("[SYSTEM:web_search_results]\nWeb search for %q failed: %s", query, err))
		return
	}

	result := choice.Message.Content
	if result == "" {
		result = "No results found."
	}
	t.deps.DeliverResult(fmt.Sprintf("[SYSTEM:web_search_results]\nSearch results for %q:\n\n%s", query, result))
}

// NewDefaultRegistry creates a registry with standard tools.
// If searchDeps is non-nil, the async web_search and web_fetch tools are also registered.
func NewDefaultRegistry(store *memory.Store, serverID string, send SendFunc, react ReactFunc, searchDeps *WebSearchDeps) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID})
	r.Register(&memoryRecallTool{store: store, serverID: serverID})
	r.Register(&memoryForgetTool{store: store, serverID: serverID})
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText})
	r.Register(&reactTool{react: react})
	if searchDeps != nil {
		r.Register(&webSearchTool{deps: searchDeps})
		r.Register(&webFetchTool{timeoutSeconds: searchDeps.TimeoutSeconds})
	}
	return r
}

// NewMemoryOnlyRegistry creates a registry with only memory_save and memory_recall.
// Used by the background memory extraction pass.
func NewMemoryOnlyRegistry(store *memory.Store, serverID string) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID})
	r.Register(&memoryRecallTool{store: store, serverID: serverID})
	return r
}
