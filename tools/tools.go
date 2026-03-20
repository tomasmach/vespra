// Package tools defines the AI tool registry and tool implementations.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/memory"
)

// Tool name constants used across packages to avoid stringly-typed checks.
const (
	ToolNameWebSearch = "web_search"
	ToolNameWebFetch  = "web_fetch"
	ToolNameImageGen  = "generate_image"
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
	tools           map[string]Tool
	Replied         bool   // set to true when the reply tool is called
	ReplyText       string // the content argument passed to the reply tool
	ReplyCount      int    // number of reply tool calls in this turn
	WebSearchCalled bool   // set to true when web_search is invoked
	ImageGenCalled  bool   // set to true when generate_image is invoked
	Reacted         bool   // set to true when the react tool is called
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
		slog.Warn("dispatch: unknown tool", "tool", name)
		return fmt.Sprintf("Tool %q is not available. Respond to the user without it.", name), nil
	}
	return t.Call(ctx, args)
}

// SendFunc sends a text message to the channel.
type SendFunc func(content string) error

// ReactFunc adds an emoji reaction to the triggering message.
type ReactFunc func(emoji string) error

type memorySaveTool struct {
	store          *memory.Store
	serverID       string
	dedupThreshold float64
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
	result, err := t.store.Save(ctx, p.Content, t.serverID, p.UserID, "", importance, t.dedupThreshold)
	if err != nil {
		return "", err
	}
	switch result.Status {
	case memory.SaveStatusUpdated:
		return fmt.Sprintf("Memory updated with new details (id: %s)", result.ID), nil
	case memory.SaveStatusExists:
		return fmt.Sprintf("Memory already exists (id: %s)", result.ID), nil
	default:
		return fmt.Sprintf("Memory saved (id: %s)", result.ID), nil
	}
}

type memoryRecallTool struct {
	store       *memory.Store
	serverID    string
	defaultTopN int
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
            "top_n": {"type": "integer", "description": "Max results to return."}
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
		p.TopN = t.defaultTopN
	}
	// Tool-invoked recall uses threshold 0: the LLM explicitly asked, don't filter.
	rows, err := t.store.Recall(ctx, p.Query, t.serverID, p.TopN, 0)
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
	send       SendFunc
	replied    *bool
	replyText  *string
	replyCount *int
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
	// Suppress stage directions passed through the reply tool (e.g. <IDLE/>, (silence)).
	if isStageDirection(p.Content) {
		return "Replied.", nil
	}
	// Guardrail: cap replies per turn to prevent runaway loops while still
	// allowing a status message followed by the real answer (max 3).
	if *t.replyCount >= 2 {
		return "Reply limit reached for this turn.", nil
	}
	parts := SplitAndCapMessage(p.Content, 2000)
	for _, part := range parts {
		if err := t.send(part); err != nil {
			return "", err
		}
	}
	*t.replied = true
	*t.replyCount++
	*t.replyText = p.Content
	return "Replied.", nil
}

// isStageDirection reports whether s is a stage direction that the model emits
// instead of a real reply, e.g. "(staying silent)", "[MLČÍM]", "<IDLE/>".
func isStageDirection(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.Contains(s, "\n") {
		return false
	}
	return (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) ||
		(strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"))
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

// maxReplyParts is the maximum number of Discord message chunks sent per reply.
// Caps output length to ~4000 UTF-16 units (2 × 2000) to prevent flooding.
const maxReplyParts = 2

// SplitAndCapMessage splits s into chunks and caps at maxReplyParts.
func SplitAndCapMessage(s string, limit int) []string {
	parts := SplitMessage(s, limit)
	if len(parts) > maxReplyParts {
		slog.Warn("truncated SplitMessage output", "original_parts", len(parts))
		parts = parts[:maxReplyParts]
	}
	return parts
}

// customEmojiRe matches Discord custom emoji markup like <:name:id> or <a:name:id>
// and captures the "name:id" portion.
var customEmojiRe = regexp.MustCompile(`^<a?:(\w+:\d+)>$`)

type reactTool struct {
	react   ReactFunc
	reacted *bool
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
	emoji := p.Emoji
	if m := customEmojiRe.FindStringSubmatch(emoji); m != nil {
		emoji = m[1]
	}
	if err := t.react(emoji); err != nil {
		return "", err
	}
	*t.reacted = true
	return "Reacted.", nil
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
	SearchProvider string // "brave" | "glm"
	SearchAPIKey   string // Brave API key
}

type webSearchTool struct {
	deps         *WebSearchDeps
	searchCalled *bool
}

func (t *webSearchTool) Name() string { return ToolNameWebSearch }
func (t *webSearchTool) Description() string {
	return "Search the web for current information. You MUST call this tool to trigger a search — " +
		"results will not appear unless you explicitly invoke it. " +
		"After calling this tool, use the reply tool to tell the user you are searching (in their language)."
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
	// Set only on the successful CAS path so processTurn's loop-break guard
	// fires exclusively when a new search was actually launched, not on a skip.
	*t.searchCalled = true

	t.deps.SearchWg.Add(1)
	go t.runSearch(p.Query)
	return fmt.Sprintf("Web search started for: %q — results will arrive shortly.", p.Query), nil
}

func (t *webSearchTool) runSearch(query string) {
	defer t.deps.SearchWg.Done()
	defer t.deps.SearchRunning.Store(false)

	ctx, cancel := context.WithTimeout(t.deps.Ctx, time.Duration(t.deps.TimeoutSeconds)*time.Second)
	defer cancel()

	// Use Brave Search if configured
	if t.deps.SearchProvider == "brave" && t.deps.SearchAPIKey != "" {
		client := newBraveClient(t.deps.SearchAPIKey)
		result, err := client.searchToMarkdown(ctx, query, 10)
		if err != nil {
			slog.Error("brave search failed", "error", err, "query", query)
			t.deps.DeliverResult(fmt.Sprintf("[SYSTEM:web_search_results]\nWeb search for %q failed: %s", query, err))
			return
		}
		t.deps.DeliverResult(fmt.Sprintf("[SYSTEM:web_search_results]\nSearch results for %q:\n\n%s", query, result))
		return
	}

	// Fall back to GLM built-in web search
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

// NewReplyOnlyRegistry creates a minimal registry with only the reply and react tools.
// Used for internal turns (e.g. web search result summarization) where the LLM
// should just summarize and reply without calling memory or search tools.
func NewReplyOnlyRegistry(send SendFunc, react ReactFunc) *Registry {
	r := NewRegistry()
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText, replyCount: &r.ReplyCount})
	r.Register(&reactTool{react: react, reacted: &r.Reacted})
	return r
}

// NewDefaultRegistry creates a registry with standard tools.
// If searchDeps is non-nil, the async web_search and web_fetch tools are also registered.
func NewDefaultRegistry(store *memory.Store, serverID string, dedupThreshold float64, defaultRecallLimit int, send SendFunc, react ReactFunc, searchDeps *WebSearchDeps, imageGenDeps *ImageGenDeps) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID, dedupThreshold: dedupThreshold})
	r.Register(&memoryRecallTool{store: store, serverID: serverID, defaultTopN: defaultRecallLimit})
	r.Register(&memoryForgetTool{store: store, serverID: serverID})
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText, replyCount: &r.ReplyCount})
	r.Register(&reactTool{react: react, reacted: &r.Reacted})
	if searchDeps != nil {
		r.Register(&webSearchTool{deps: searchDeps, searchCalled: &r.WebSearchCalled})
		r.Register(&webFetchTool{timeoutSeconds: searchDeps.TimeoutSeconds})
	}
	if imageGenDeps != nil {
		r.Register(&imageGenTool{deps: imageGenDeps, imageCalled: &r.ImageGenCalled})
	}
	return r
}

// RegisterWebFetch adds web_fetch to the registry without web_search.
// Used in internal search-result turns where the LLM can follow up on URLs
// but must not trigger new searches (which would cause infinite loops).
func (r *Registry) RegisterWebFetch(timeoutSeconds int) {
	r.Register(&webFetchTool{timeoutSeconds: timeoutSeconds})
}

// NewMemoryOnlyRegistry creates a registry with only memory_save and memory_recall.
// Used by the background memory extraction pass.
func NewMemoryOnlyRegistry(store *memory.Store, serverID string, dedupThreshold float64, defaultRecallLimit int) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID, dedupThreshold: dedupThreshold})
	r.Register(&memoryRecallTool{store: store, serverID: serverID, defaultTopN: defaultRecallLimit})
	return r
}
