package memory

import (
	"context"
	"fmt"
	"testing"
)

func TestLogConversationPersistsRow(t *testing.T) {
	store := newTestStore(t, nil)
	ctx := context.Background()

	err := store.LogConversation(ctx, "chan1", "hello bot", `[{"name":"reply","result":"ok"}]`, "hello human")
	if err != nil {
		t.Fatalf("LogConversation() error: %v", err)
	}

	rows, total, err := store.ListConversations(ctx, "chan1", 10, 0)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}

	r := rows[0]
	if r.ChannelID != "chan1" {
		t.Errorf("expected channel_id %q, got %q", "chan1", r.ChannelID)
	}
	if r.UserMsg != "hello bot" {
		t.Errorf("expected user_msg %q, got %q", "hello bot", r.UserMsg)
	}
	if r.Response != "hello human" {
		t.Errorf("expected response %q, got %q", "hello human", r.Response)
	}
	if r.ToolCalls == "" {
		t.Error("expected tool_calls to be populated, got empty string")
	}
}

func TestLogConversationEmptyToolCalls(t *testing.T) {
	store := newTestStore(t, nil)
	ctx := context.Background()

	// toolCallsJSON is empty — should insert NULL and COALESCE gives ""
	err := store.LogConversation(ctx, "chan2", "a user message", "", "a response")
	if err != nil {
		t.Fatalf("LogConversation() error: %v", err)
	}

	rows, total, err := store.ListConversations(ctx, "chan2", 10, 0)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected total=1, got %d", total)
	}
	if rows[0].ToolCalls != "" {
		t.Errorf("expected empty tool_calls, got %q", rows[0].ToolCalls)
	}
}

func TestListConversationsFiltersByChannelID(t *testing.T) {
	store := newTestStore(t, nil)
	ctx := context.Background()

	if err := store.LogConversation(ctx, "chanA", "msg A", "", "resp A"); err != nil {
		t.Fatalf("LogConversation(chanA): %v", err)
	}
	if err := store.LogConversation(ctx, "chanB", "msg B", "", "resp B"); err != nil {
		t.Fatalf("LogConversation(chanB): %v", err)
	}

	rowsA, totalA, err := store.ListConversations(ctx, "chanA", 10, 0)
	if err != nil {
		t.Fatalf("ListConversations(chanA) error: %v", err)
	}
	if totalA != 1 {
		t.Errorf("expected totalA=1, got %d", totalA)
	}
	for _, r := range rowsA {
		if r.ChannelID != "chanA" {
			t.Errorf("got unexpected channel_id %q in chanA results", r.ChannelID)
		}
	}

	rowsB, totalB, err := store.ListConversations(ctx, "chanB", 10, 0)
	if err != nil {
		t.Fatalf("ListConversations(chanB) error: %v", err)
	}
	if totalB != 1 {
		t.Errorf("expected totalB=1, got %d", totalB)
	}
	for _, r := range rowsB {
		if r.ChannelID != "chanB" {
			t.Errorf("got unexpected channel_id %q in chanB results", r.ChannelID)
		}
	}
}

func TestListConversationsNoFilterReturnsAll(t *testing.T) {
	store := newTestStore(t, nil)
	ctx := context.Background()

	channels := []string{"ch1", "ch2", "ch3"}
	for _, ch := range channels {
		if err := store.LogConversation(ctx, ch, "msg", "", "resp"); err != nil {
			t.Fatalf("LogConversation(%s): %v", ch, err)
		}
	}

	// Empty channelID = all channels
	rows, total, err := store.ListConversations(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if total != len(channels) {
		t.Errorf("expected total=%d, got %d", len(channels), total)
	}
	if len(rows) != len(channels) {
		t.Errorf("expected %d rows, got %d", len(channels), len(rows))
	}
}

func TestListConversationsTotalCount(t *testing.T) {
	store := newTestStore(t, nil)
	ctx := context.Background()

	const n = 7
	for i := range n {
		if err := store.LogConversation(ctx, "chan1", fmt.Sprintf("msg %d", i), "", "resp"); err != nil {
			t.Fatalf("LogConversation(): %v", err)
		}
	}

	// Request only 3 rows but total should reflect all 7
	rows, total, err := store.ListConversations(ctx, "chan1", 3, 0)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if total != n {
		t.Errorf("expected total=%d, got %d", n, total)
	}
	if len(rows) != 3 {
		t.Errorf("expected 3 rows in page, got %d", len(rows))
	}
}

func TestListConversationsPruning(t *testing.T) {
	store := newTestStore(t, nil)
	ctx := context.Background()

	// Insert >10000 rows directly (bypassing the 1-in-500 prune probability)
	const overLimit = 10001
	for i := range overLimit {
		_, err := store.db.ExecContext(ctx,
			`INSERT INTO conversations (channel_id, user_msg, tool_calls, response, ts) VALUES (?, ?, NULL, ?, datetime('now'))`,
			"chan-prune", fmt.Sprintf("msg %d", i), "resp",
		)
		if err != nil {
			t.Fatalf("direct insert row %d: %v", i, err)
		}
	}

	// Trigger the prune directly by executing the same SQL used in LogConversation
	_, err := store.db.ExecContext(ctx,
		`DELETE FROM conversations WHERE id NOT IN (SELECT id FROM conversations ORDER BY id DESC LIMIT 10000)`,
	)
	if err != nil {
		t.Fatalf("prune exec: %v", err)
	}

	_, total, err := store.ListConversations(ctx, "chan-prune", 1, 0)
	if err != nil {
		t.Fatalf("ListConversations() error: %v", err)
	}
	if total > 10000 {
		t.Errorf("expected conversations table pruned to <=10000 rows, got %d", total)
	}
}
