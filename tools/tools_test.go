package tools_test

import (
	"context"
	"encoding/json"
	"strings"
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
