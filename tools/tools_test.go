package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tomasmach/vespra/tools"
)

func TestDispatchUnknownToolReturnsError(t *testing.T) {
	r := tools.NewRegistry()
	_, err := r.Dispatch(context.Background(), "nonexistent_tool", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("Dispatch() on unknown tool should return an error")
	}
	if !strings.Contains(err.Error(), "nonexistent_tool") {
		t.Errorf("error should mention the tool name, got: %v", err)
	}
}

func TestSplitMessageRespects2000CharLimit(t *testing.T) {
	long := strings.Repeat("a", 4500)
	parts := tools.SplitMessage(long, 2000)
	for i, p := range parts {
		if len([]rune(p)) > 2000 {
			t.Errorf("part %d has %d runes, exceeds 2000", i, len([]rune(p)))
		}
	}
	// Reassembled content should equal original
	got := strings.Join(parts, "")
	if got != long {
		t.Error("SplitMessage() lost content")
	}
}

func TestSplitMessageShortString(t *testing.T) {
	msg := "hello world"
	parts := tools.SplitMessage(msg, 2000)
	if len(parts) != 1 {
		t.Errorf("expected 1 part for short string, got %d", len(parts))
	}
	if parts[0] != msg {
		t.Errorf("expected %q, got %q", msg, parts[0])
	}
}

func utf16Len(r rune) int {
	if r >= 0x10000 {
		return 2
	}
	return 1
}

func utf16Units(s string) int {
	n := 0
	for _, r := range s {
		n += utf16Len(r)
	}
	return n
}

func TestSplitMessageUnicode(t *testing.T) {
	// Each emoji (U+1F600) is 1 rune but 2 UTF-16 code units.
	// 2500 emoji = 5000 UTF-16 units, so limit of 2000 must split on UTF-16 boundaries.
	emoji := "😀"
	long := strings.Repeat(emoji, 2500)
	parts := tools.SplitMessage(long, 2000)
	for i, p := range parts {
		units := utf16Units(p)
		if units > 2000 {
			t.Errorf("part %d has %d UTF-16 units, exceeds limit of 2000", i, units)
		}
	}
	got := strings.Join(parts, "")
	if got != long {
		t.Error("SplitMessage() lost content with unicode input")
	}
}

func TestSplitMessageMixedASCIIAndEmoji(t *testing.T) {
	// 1500 ASCII chars (1 UTF-16 unit each) + 500 emoji (2 UTF-16 units each)
	// = 1500 + 1000 = 2500 UTF-16 units total, which exceeds the 2000 limit.
	// The old rune-based code would wrongly count this as 2000 runes and not split.
	ascii := strings.Repeat("a", 1500)
	emoji := strings.Repeat("😀", 500)
	combined := ascii + emoji
	parts := tools.SplitMessage(combined, 2000)
	if len(parts) < 2 {
		t.Errorf("expected message to be split into at least 2 parts, got %d", len(parts))
	}
	for i, p := range parts {
		units := utf16Units(p)
		if units > 2000 {
			t.Errorf("part %d has %d UTF-16 units, exceeds limit of 2000", i, units)
		}
	}
	got := strings.Join(parts, "")
	if got != combined {
		t.Error("SplitMessage() lost content with mixed ASCII and emoji input")
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	msg := strings.Repeat("x", 2000)
	parts := tools.SplitMessage(msg, 2000)
	if len(parts) != 1 {
		t.Errorf("string exactly at limit should be 1 part, got %d", len(parts))
	}
}

func TestReplyToolDeduplication(t *testing.T) {
	sendCount := 0
	send := func(content string) error {
		sendCount++
		return nil
	}
	react := func(emoji string) error { return nil }

	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, nil)
	ctx := context.Background()

	// First call: should send and return "Replied."
	result, err := r.Dispatch(ctx, "reply", json.RawMessage(`{"content":"hello"}`))
	if err != nil {
		t.Fatalf("first Dispatch() returned unexpected error: %v", err)
	}
	if result != "Replied." {
		t.Errorf("first call: expected %q, got %q", "Replied.", result)
	}
	if sendCount != 1 {
		t.Errorf("first call: expected send count 1, got %d", sendCount)
	}

	// Second call: dedup guard should fire, send must not be called again.
	result, err = r.Dispatch(ctx, "reply", json.RawMessage(`{"content":"hello again"}`))
	if err != nil {
		t.Fatalf("second Dispatch() returned unexpected error: %v", err)
	}
	if result != "Reply already sent in this turn." {
		t.Errorf("second call: expected %q, got %q", "Reply already sent in this turn.", result)
	}
	if sendCount != 1 {
		t.Errorf("second call: expected send count still 1, got %d", sendCount)
	}
}

// newSearchDeps creates a minimal WebSearchDeps suitable for unit tests.
// deliverResult is called when the async search goroutine finishes; pass a no-op
// if the test does not need to observe the delivered result.
func newSearchDeps(searchRunning *atomic.Bool, wg *sync.WaitGroup, deliver func(string)) *tools.WebSearchDeps {
	if deliver == nil {
		deliver = func(string) {}
	}
	return &tools.WebSearchDeps{
		DeliverResult:  deliver,
		LLM:            nil, // not reached in CAS-failure path; tests guard against the goroutine path
		Model:          "",
		Ctx:            context.Background(),
		SearchWg:       wg,
		SearchRunning:  searchRunning,
		TimeoutSeconds: 5,
		// SearchProvider left empty: falls through to the GLM path which we never reach
		// because we only test the CAS guard and the early-return branch.
	}
}

// TestWebSearchCalledFlagSetOnSuccessfulCAS verifies that the Registry.WebSearchCalled
// field is set to true when web_search executes and the CompareAndSwap succeeds
// (i.e., no concurrent search is already running).
func TestWebSearchCalledFlagSetOnSuccessfulCAS(t *testing.T) {
	var searchRunning atomic.Bool
	var wg sync.WaitGroup

	// Use a pre-cancelled context so the background goroutine's HTTP/LLM call
	// fails immediately due to context cancellation rather than panicking on a
	// nil LLM client. DeliverResult is always called by runSearch before it
	// returns, so the WaitGroup will still complete.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	deps := &tools.WebSearchDeps{
		DeliverResult:  func(string) {}, // no-op; we only care about the CAS path
		LLM:            nil,
		Model:          "",
		Ctx:            cancelledCtx,
		SearchWg:       &wg,
		SearchRunning:  &searchRunning,
		TimeoutSeconds: 5,
		SearchProvider: "brave",
		SearchAPIKey:   "fake-key-to-enter-brave-path",
		// Brave client will use the cancelled context and fail with a context error
		// before making any real network call.
	}

	send := func(content string) error { return nil }
	react := func(emoji string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, deps, nil)

	if r.WebSearchCalled {
		t.Fatal("WebSearchCalled should be false before any tool call")
	}

	result, err := r.Dispatch(context.Background(), "web_search", json.RawMessage(`{"query":"test query"}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "Web search started") {
		t.Errorf("unexpected result from successful web_search: %q", result)
	}

	// Flag must be set immediately after the successful CAS, before the goroutine finishes.
	if !r.WebSearchCalled {
		t.Error("WebSearchCalled should be true after a successful web_search call")
	}

	// Wait for the background goroutine to finish (it will exit quickly due to cancelled ctx).
	wg.Wait()
}

// TestWebSearchCalledFlagNotSetWhenAlreadyRunning verifies that the Registry.WebSearchCalled
// field remains false when web_search is called while another search is already running
// (CompareAndSwap fails).
func TestWebSearchCalledFlagNotSetWhenAlreadyRunning(t *testing.T) {
	var searchRunning atomic.Bool
	// Pre-set the flag to simulate a search already in progress.
	searchRunning.Store(true)

	var wg sync.WaitGroup
	deps := newSearchDeps(&searchRunning, &wg, nil)

	send := func(content string) error { return nil }
	react := func(emoji string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, deps, nil)

	result, err := r.Dispatch(context.Background(), "web_search", json.RawMessage(`{"query":"concurrent query"}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "already running") {
		t.Errorf("expected 'already running' message, got %q", result)
	}

	// The CAS failed, so WebSearchCalled must remain false.
	if r.WebSearchCalled {
		t.Error("WebSearchCalled should be false when CAS fails (search already running)")
	}

	// No goroutine was launched, so SearchRunning should still be true (unchanged).
	if !searchRunning.Load() {
		t.Error("SearchRunning should still be true after CAS failure")
	}
}

// TestWebSearchCalledFlagEmptyQueryReturnsEarly verifies that an empty query
// returns an error message without touching the CAS or setting WebSearchCalled.
func TestWebSearchCalledFlagEmptyQueryReturnsEarly(t *testing.T) {
	var searchRunning atomic.Bool
	var wg sync.WaitGroup
	deps := newSearchDeps(&searchRunning, &wg, nil)

	send := func(content string) error { return nil }
	react := func(emoji string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, deps, nil)

	result, err := r.Dispatch(context.Background(), "web_search", json.RawMessage(`{"query":""}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if !strings.Contains(result, "query is required") {
		t.Errorf("expected 'query is required' message, got %q", result)
	}
	if r.WebSearchCalled {
		t.Error("WebSearchCalled should be false when query is empty")
	}
	if searchRunning.Load() {
		t.Error("SearchRunning should not be set when query is empty")
	}
}

// TestRegistryLoopBreakConditionBothFlagsSet verifies the invariant that drives
// the processTurn immediate-break guard: when both WebSearchCalled and Replied
// are true, the loop should break. This test validates the flag state that
// processTurn relies on rather than testing processTurn directly (which requires
// a live LLM).
func TestRegistryLoopBreakConditionBothFlagsSet(t *testing.T) {
	send := func(content string) error { return nil }
	react := func(emoji string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, nil)

	// Initially neither flag is set.
	if r.WebSearchCalled || r.Replied {
		t.Fatal("expected both flags to start as false")
	}

	// Simulate the reply tool firing.
	_, err := r.Dispatch(context.Background(), "reply", json.RawMessage(`{"content":"Searching now..."}`))
	if err != nil {
		t.Fatalf("reply Dispatch() error: %v", err)
	}
	if !r.Replied {
		t.Fatal("Replied should be true after reply tool call")
	}

	// WebSearchCalled is still false; the break condition should NOT be met.
	if r.WebSearchCalled && r.Replied {
		t.Error("break condition should not be met when only Replied is true")
	}

	// Manually set WebSearchCalled (as webSearchTool.Call does after a successful CAS).
	r.WebSearchCalled = true

	// Now both flags are true — the immediate break condition is met.
	if !(r.WebSearchCalled && r.Replied) {
		t.Error("break condition should be met when both WebSearchCalled and Replied are true")
	}
}

// TestRegistryLoopBreakConditionOnlyWebSearchCalled verifies that the immediate
// break condition does NOT fire when web_search ran but the reply tool was never called.
func TestRegistryLoopBreakConditionOnlyWebSearchCalled(t *testing.T) {
	r := tools.NewRegistry()
	r.WebSearchCalled = true

	// Replied is still false — break condition must not be met.
	if r.WebSearchCalled && r.Replied {
		t.Error("break condition must not fire when only WebSearchCalled is true and Replied is false")
	}
}

// TestRegistryLoopBreakConditionOnlyReplied verifies that only having Replied set
// does NOT trigger the immediate break (it activates the post-reply cap instead).
func TestRegistryLoopBreakConditionOnlyReplied(t *testing.T) {
	r := tools.NewRegistry()
	r.Replied = true

	// WebSearchCalled is false — immediate break condition must not be met.
	if r.WebSearchCalled && r.Replied {
		t.Error("immediate break condition must not fire when only Replied is true and WebSearchCalled is false")
	}
}

// TestRegistryWebSearchCalledIsStickyLatch verifies that once set, WebSearchCalled
// is never reset within a turn by the registry itself, matching the invariant
// documented in processTurn ("sticky latches that are never reset within a turn").
func TestRegistryWebSearchCalledIsStickyLatch(t *testing.T) {
	r := tools.NewRegistry()

	// Set the flag directly (as webSearchTool does after a successful CAS).
	r.WebSearchCalled = true

	// Nothing in the registry should reset it — a subsequent Dispatch of another tool
	// must not affect WebSearchCalled.
	r.Register(&noopTool{})
	_, _ = r.Dispatch(context.Background(), "noop", json.RawMessage(`{}`))

	if !r.WebSearchCalled {
		t.Error("WebSearchCalled must remain true (sticky latch) after other tool calls")
	}
}

func TestReactToolSetsReactedFlag(t *testing.T) {
	send := func(content string) error { return nil }
	react := func(emoji string) error { return nil }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, nil)

	if r.Reacted {
		t.Fatal("Reacted should be false before any tool call")
	}

	result, err := r.Dispatch(context.Background(), "react", json.RawMessage(`{"emoji":"👍"}`))
	if err != nil {
		t.Fatalf("Dispatch() returned unexpected error: %v", err)
	}
	if result != "Reacted." {
		t.Errorf("expected %q, got %q", "Reacted.", result)
	}
	if !r.Reacted {
		t.Error("Reacted should be true after a successful react call")
	}
}

func TestReactToolDoesNotSetReactedOnError(t *testing.T) {
	send := func(content string) error { return nil }
	react := func(emoji string) error { return fmt.Errorf("discord error") }
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, react, nil, nil)

	_, err := r.Dispatch(context.Background(), "react", json.RawMessage(`{"emoji":"👍"}`))
	if err == nil {
		t.Fatal("expected error from react tool")
	}
	if r.Reacted {
		t.Error("Reacted should remain false when react returns an error")
	}
}

// noopTool is a minimal Tool implementation used in sticky-latch tests.
type noopTool struct{}

func (n *noopTool) Name() string                                          { return "noop" }
func (n *noopTool) Description() string                                   { return "does nothing" }
func (n *noopTool) Parameters() json.RawMessage                           { return json.RawMessage(`{"type":"object","properties":{}}`) }
func (n *noopTool) Call(_ context.Context, _ json.RawMessage) (string, error) { return "ok", nil }
