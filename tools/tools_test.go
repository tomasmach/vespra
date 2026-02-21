package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDispatchUnknownToolReturnsError(t *testing.T) {
	r := NewRegistry()
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
	parts := SplitMessage(long, 2000)
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
	parts := SplitMessage(msg, 2000)
	if len(parts) != 1 {
		t.Errorf("expected 1 part for short string, got %d", len(parts))
	}
	if parts[0] != msg {
		t.Errorf("expected %q, got %q", msg, parts[0])
	}
}

func TestSplitMessageUnicode(t *testing.T) {
	// Each emoji is 1 rune but multiple bytes; limit should be in runes
	emoji := "😀"
	long := strings.Repeat(emoji, 2500)
	parts := SplitMessage(long, 2000)
	for i, p := range parts {
		runeCount := len([]rune(p))
		if runeCount > 2000 {
			t.Errorf("part %d has %d runes, exceeds limit", i, runeCount)
		}
	}
	got := strings.Join(parts, "")
	if got != long {
		t.Error("SplitMessage() lost content with unicode input")
	}
}

func TestSplitMessageExactLimit(t *testing.T) {
	msg := strings.Repeat("x", 2000)
	parts := SplitMessage(msg, 2000)
	if len(parts) != 1 {
		t.Errorf("string exactly at limit should be 1 part, got %d", len(parts))
	}
}
