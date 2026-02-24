# GLM Web Search & Vision Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace Brave Search with async GLM-native web search, fix vision routing for GLM, update local config to GLM-5.

**Architecture:** The `web_search` tool becomes non-blocking: `Call()` spawns a background goroutine that makes a one-shot GLM Chat request with the native `web_search` tool enabled, then injects results into a new `internalCh` on the `ChannelAgent`. The agent processes search results through the normal turn loop but without re-registering web search (preventing infinite loops). Vision routing is fixed to use GLM key when `vision_base_url` matches `glm_base_url`.

**Tech Stack:** Go, discordgo, GLM/Z.ai API (OpenAI-compatible chat completions with GLM-native web_search tool extension)

---

## Key Design Details

### GLM Web Search Tool Format

GLM's API accepts a non-standard tool type alongside function tools:
```json
{
    "type": "web_search",
    "web_search": {
        "enable": true,
        "search_result": true
    }
}
```
When included in the tools array, GLM performs a server-side web search and returns results with citations in its response content.

### Internal Message Channel

Cannot inject into `msgCh` (typed `chan *discordgo.MessageCreate`) without creating fake Discord structs. Instead, add a separate `internalCh chan string` to `ChannelAgent` for system-generated messages (search results, etc.). The `run()` select loop handles both channels.

### Avoiding Circular Imports

`tools` package cannot import `agent`. The web search tool receives a `func(string)` callback (`deliverResult`) instead of a channel reference. The agent creates this callback to write to its `internalCh`.

### Preventing Infinite Search Loops

`handleInternalMessage` creates a registry WITHOUT web search (passes `nil` for `WebSearchDeps`), so the LLM cannot trigger another search when processing results.

---

### Task 0: Create branch and update local config

**Files:**
- Modify: `~/.config/vespra/config.toml`

**Step 1: Create feature branch**

Run: `cd /Users/tomasmach/Code/vespra && git checkout -b feat/glm-web-search`

**Step 2: Update local config**

In `~/.config/vespra/config.toml`, change:
```toml
[llm]
  model = "glm-5"
  vision_model = "glm-5"
  vision_base_url = "https://api.z.ai/api/coding/paas/v4"
```

And add `model = "glm-5"` to the agent entry:
```toml
[[agents]]
  model = "glm-5"
```

Remove `[tools]` section with `web_search_key` if present (Brave no longer needed).

---

### Task 1: Add ExtraTools support to ChatOptions

**Files:**
- Modify: `llm/llm.go:124-129` (ChatOptions struct)
- Modify: `llm/llm.go:208-214` (tools body construction in Chat)
- Test: `llm/llm_test.go`

**Step 1: Write the failing test**

Add to `llm/llm_test.go`:
```go
func TestExtraToolsIncludedInRequestBody(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithBaseURL(t, srv.URL)

	extraTool := json.RawMessage(`{"type":"web_search","web_search":{"enable":true}}`)
	opts := &llm.ChatOptions{
		ExtraTools: []json.RawMessage{extraTool},
	}

	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools, ok := (*capturedBody)["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected tools array with 1 entry, got %v", (*capturedBody)["tools"])
	}
	toolMap := tools[0].(map[string]any)
	if toolMap["type"] != "web_search" {
		t.Errorf("expected tool type web_search, got %v", toolMap["type"])
	}
}

func TestExtraToolsMergedWithFunctionTools(t *testing.T) {
	srv, capturedBody := captureBodyServer(t)
	client := clientWithBaseURL(t, srv.URL)

	funcTools := []llm.ToolDefinition{{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        "test_func",
			Description: "A test function",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		},
	}}
	extraTool := json.RawMessage(`{"type":"web_search","web_search":{"enable":true}}`)
	opts := &llm.ChatOptions{
		ExtraTools: []json.RawMessage{extraTool},
	}

	_, err := client.Chat(context.Background(), []llm.Message{{Role: "user", Content: "hi"}}, funcTools, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tools, ok := (*capturedBody)["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("expected tools array with 2 entries, got %v", (*capturedBody)["tools"])
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/tomasmach/Code/vespra && go test ./llm/ -run TestExtraTools -v`
Expected: FAIL (ExtraTools field doesn't exist yet)

**Step 3: Implement ExtraTools**

In `llm/llm.go`, add field to `ChatOptions`:
```go
type ChatOptions struct {
	Provider   string            // "openrouter" | "glm" | "" (use global)
	Model      string            // override model name; "" = use global
	ExtraTools []json.RawMessage // additional raw tool defs (e.g., GLM web_search)
}
```

In `llm/llm.go`, replace the tools body construction (around line 208-214):
```go
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
```

**Step 4: Run test to verify it passes**

Run: `cd /Users/tomasmach/Code/vespra && go test ./llm/ -run TestExtraTools -v`
Expected: PASS

**Step 5: Commit**

```bash
git add llm/llm.go llm/llm_test.go
git commit -m "feat: add ExtraTools support to ChatOptions for GLM native tools"
```

---

### Task 2: Fix vision routing for GLM provider

**Files:**
- Modify: `llm/llm.go:187-204` (vision switch case in Chat)
- Test: `llm/llm_test.go`

**Step 1: Write the failing test**

Add to `llm/llm_test.go`:
```go
func TestVisionRoutesToGLMWhenVisionBaseURLMatchesGLMBaseURL(t *testing.T) {
	glmSrv, _, capturedAuth, capturedBody := captureRequestServer(t)

	cfg := &config.Config{
		LLM: config.LLMConfig{
			OpenRouterKey:         "or-key",
			GLMKey:                "glm-secret",
			GLMBaseURL:            glmSrv.URL,
			Model:                 "global-model",
			VisionModel:           "glm-5",
			VisionBaseURL:         glmSrv.URL, // same as GLMBaseURL
			EmbeddingModel:        "embed-model",
			RequestTimeoutSeconds: 5,
			BaseURL:               "http://should-not-be-used.invalid",
		},
	}
	client := newTestClientWithConfig(t, cfg)

	imageMessages := []llm.Message{
		{Role: "user", Content: "hi"},
		{
			Role: "user",
			ContentParts: []llm.ContentPart{
				{Type: "text", Text: "what is this?"},
				{Type: "image_url", ImageURL: &llm.ImageURL{URL: "https://example.com/img.png"}},
			},
		},
	}

	opts := &llm.ChatOptions{Provider: "glm"}
	_, err := client.Chat(context.Background(), imageMessages, nil, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if *capturedAuth != "Bearer glm-secret" {
		t.Errorf("expected GLM key for vision request when VisionBaseURL matches GLMBaseURL, got %q", *capturedAuth)
	}
	model, _ := (*capturedBody)["model"].(string)
	if model != "glm-5" {
		t.Errorf("expected vision model glm-5, got %q", model)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `cd /Users/tomasmach/Code/vespra && go test ./llm/ -run TestVisionRoutesToGLM -v`
Expected: FAIL (currently vision always uses OpenRouter key)

**Step 3: Fix vision routing**

In `llm/llm.go`, update the vision case (around line 189):
```go
	case last >= 0 && len(messages[last].ContentParts) > 0 && cfg.VisionModel != "":
		model = cfg.VisionModel
		if cfg.VisionBaseURL != "" {
			apiBase = cfg.VisionBaseURL
			// If vision routes through the same endpoint as GLM, use the GLM key.
			if cfg.VisionBaseURL == cfg.GLMBaseURL {
				apiKey = cfg.GLMKey
			}
		} else {
			apiBase = c.apiBase()
		}
```

**Step 4: Run tests to verify pass**

Run: `cd /Users/tomasmach/Code/vespra && go test ./llm/ -v`
Expected: ALL PASS (including existing vision tests)

**Step 5: Commit**

```bash
git add llm/llm.go llm/llm_test.go
git commit -m "fix: route vision requests through GLM when vision_base_url matches glm_base_url"
```

---

### Task 3: Add internalCh and handleInternalMessage to ChannelAgent

**Files:**
- Modify: `agent/agent.go:62-81` (ChannelAgent struct)
- Modify: `agent/agent.go:290-302` (newChannelAgent)
- Modify: `agent/agent.go:304-402` (run method — add select case)
- Add new method: `handleInternalMessage`

**Step 1: Add internalCh field to ChannelAgent**

In `agent/agent.go`, add to `ChannelAgent` struct:
```go
	searchRunning atomic.Bool   // prevents concurrent web searches
	internalCh    chan string    // buffered; receives system messages (e.g., web search results)
```

In `newChannelAgent`, initialize it:
```go
	internalCh: make(chan string, 10),
```

**Step 2: Add select case in run()**

In the `run()` method's `for` loop, add a new case after the `case msg := <-a.msgCh:` case:
```go
		case intMsg := <-a.internalCh:
			flush(ctx)
			resetIdleTimer()
			a.handleInternalMessage(ctx, intMsg)
```

**Step 3: Implement handleInternalMessage**

Add new method to `agent/agent.go`:
```go
// handleInternalMessage processes a system-generated message (e.g., web search results)
// through the normal agent turn loop. Web search is NOT registered to prevent loops.
func (a *ChannelAgent) handleInternalMessage(ctx context.Context, content string) {
	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()
	stopTyping := a.startTyping(ctx)
	defer stopTyping()

	memories, err := a.resources.Memory.Recall(ctx, content, a.serverID, 5)
	if err != nil {
		a.logger.Warn("memory recall error (internal msg)", "error", err)
	}

	botName := a.resources.Session.State.User.Username
	systemPrompt := a.buildSystemPrompt(cfg, "all", a.channelID, memories, botName)

	sendFn := func(text string) error {
		_, err := a.resources.Session.ChannelMessageSend(a.channelID, text)
		return err
	}
	reactFn := func(emoji string) error { return nil }
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, nil)

	userMsg := llm.Message{Role: "user", Content: content}
	llmMsgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(llmMsgs, a.history)
	llmMsgs = append(llmMsgs, userMsg)

	a.processTurn(ctx, cfg, turnParams{
		mode:         "all",
		systemPrompt: systemPrompt,
		sendFn:       sendFn,
		reg:          reg,
		llmMsgs:      llmMsgs,
		userMsgText:  content,
	})
}
```

Note: `NewDefaultRegistry` with `nil` as last param — this is the new signature (Task 4). This task and Task 4 must be done together for compilation.

**Step 4: Verify compilation**

Run: `cd /Users/tomasmach/Code/vespra && go build ./...`
Expected: May fail until Task 4 updates the registry signature. Implement together.

---

### Task 4: Replace webSearchTool with async GLM implementation

**Files:**
- Modify: `tools/tools.go:299-397` (replace webSearchTool + update NewDefaultRegistry)
- Modify: `agent/agent.go:507` and `agent/agent.go:606` (update NewDefaultRegistry call sites)

**Step 1: Define WebSearchDeps and new webSearchTool**

In `tools/tools.go`, add imports for `encoding/json`, `log/slog`, `sync/atomic`, `context`, and the new types. Replace the old `webSearchTool` struct and methods (lines 299-373) with:

```go
// WebSearchDeps groups dependencies for the async web search tool.
// Pass nil to NewDefaultRegistry to omit web search from the registry.
type WebSearchDeps struct {
	DeliverResult func(result string) // injects results back into agent
	LLM           *llm.Client
	CfgStore      *config.Store
	SearchRunning *atomic.Bool
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

	go t.runSearch(p.Query)
	return fmt.Sprintf("Web search started for: %q — results will arrive shortly.", p.Query), nil
}

func (t *webSearchTool) runSearch(query string) {
	defer t.deps.SearchRunning.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []llm.Message{
		{Role: "user", Content: fmt.Sprintf("Search the web for: %s\n\nReturn the results with titles, URLs, and brief descriptions.", query)},
	}

	webSearchTool := json.RawMessage(`{"type":"web_search","web_search":{"enable":true,"search_result":true}}`)
	opts := &llm.ChatOptions{
		Provider:   "glm",
		ExtraTools: []json.RawMessage{webSearchTool},
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
```

**Step 2: Update NewDefaultRegistry**

Replace the old signature (line 377) with:
```go
// NewDefaultRegistry creates a registry with standard tools.
// If searchDeps is non-nil, the async web_search tool is also registered.
func NewDefaultRegistry(store *memory.Store, serverID string, send SendFunc, react ReactFunc, searchDeps *WebSearchDeps) *Registry {
	r := NewRegistry()
	r.Register(&memorySaveTool{store: store, serverID: serverID})
	r.Register(&memoryRecallTool{store: store, serverID: serverID})
	r.Register(&memoryForgetTool{store: store, serverID: serverID})
	r.Register(&replyTool{send: send, replied: &r.Replied, replyText: &r.ReplyText})
	r.Register(&reactTool{react: react})
	if searchDeps != nil {
		r.Register(&webSearchTool{deps: searchDeps})
	}
	return r
}
```

**Step 3: Update call sites in agent.go**

In `handleMessage` (around line 507), replace:
```go
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, cfg.Tools.WebSearchKey)
```
with:
```go
	searchDeps := &tools.WebSearchDeps{
		DeliverResult: func(result string) {
			select {
			case a.internalCh <- result:
			default:
				a.logger.Warn("internal channel full, dropping web search result")
			}
		},
		LLM:           a.llm,
		CfgStore:      a.cfgStore,
		SearchRunning: &a.searchRunning,
	}
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, searchDeps)
```

In `handleMessages` (around line 606), same replacement:
```go
reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, cfg.Tools.WebSearchKey)
```
→
```go
	searchDeps := &tools.WebSearchDeps{
		DeliverResult: func(result string) {
			select {
			case a.internalCh <- result:
			default:
				a.logger.Warn("internal channel full, dropping web search result")
			}
		},
		LLM:           a.llm,
		CfgStore:      a.cfgStore,
		SearchRunning: &a.searchRunning,
	}
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, searchDeps)
```

**Step 4: Remove old imports**

In `tools/tools.go`, remove unused imports: `"io"`, `"net/http"`, `"net/url"`.
Add needed imports: `"log/slog"`, `"sync/atomic"`, `"time"`.

**Step 5: Verify compilation and tests**

Run: `cd /Users/tomasmach/Code/vespra && go build ./... && go test ./...`
Expected: PASS

**Step 6: Commit**

```bash
git add tools/tools.go agent/agent.go
git commit -m "feat: replace Brave web search with async GLM-native web search"
```

---

### Task 5: Add system prompt instructions for web search results

**Files:**
- Modify: `agent/agent.go:631-650` (buildSystemPrompt)

**Step 1: Add web search instruction to system prompt**

In `buildSystemPrompt`, add before the return:
```go
	sb.WriteString("\n\nWhen you receive a message starting with [SYSTEM:web_search_results], these are results from a web search you previously requested. Summarize the findings and reply to the user naturally. Include relevant sources and links when available. Do not call web_search again for these results.")
```

**Step 2: Verify compilation**

Run: `cd /Users/tomasmach/Code/vespra && go build ./...`
Expected: PASS

**Step 3: Commit**

```bash
git add agent/agent.go
git commit -m "feat: add system prompt instructions for async web search results"
```

---

### Task 6: Remove ToolsConfig.WebSearchKey from config

**Files:**
- Modify: `config/config.go:67-69` (ToolsConfig struct)

**Step 1: Check if WebSearchKey is used anywhere else**

Run: `grep -r "WebSearchKey\|web_search_key" /Users/tomasmach/Code/vespra/ --include="*.go"`

Expected: Only config.go definition and the old agent.go call sites (already updated in Task 4).

**Step 2: Remove the field**

The `ToolsConfig` struct and `[tools]` section can stay (for future tools), but remove the `WebSearchKey` field:
```go
type ToolsConfig struct {
}
```

Or if this is the only field, the struct can remain empty — TOML will silently ignore the old `web_search_key` in existing configs.

**Step 3: Verify compilation and tests**

Run: `cd /Users/tomasmach/Code/vespra && go build ./... && go test ./...`
Expected: PASS

**Step 4: Commit**

```bash
git add config/config.go
git commit -m "refactor: remove unused Brave web_search_key from config"
```

---

### Task 7: Final integration test

**Step 1: Run all tests**

Run: `cd /Users/tomasmach/Code/vespra && go test ./... -v`
Expected: ALL PASS

**Step 2: Build binary**

Run: `cd /Users/tomasmach/Code/vespra && go build -o vespra .`
Expected: Clean build, no warnings

**Step 3: Verify config loads**

Run: `cd /Users/tomasmach/Code/vespra && ./vespra --config ~/.config/vespra/config.toml 2>&1 | head -5`
Expected: Bot starts (or fails at Discord connection, which is fine — config loaded ok)
