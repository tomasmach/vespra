package agent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
)

func newTestRouter(t *testing.T) *Router {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Response: config.ResponseConfig{DefaultMode: "all"},
		Agent: config.TurnConfig{
			HistoryLimit:       20,
			IdleTimeoutMinutes: 10,
			MaxToolIterations:  10,
		},
		Memory: config.MemoryConfig{DBPath: filepath.Join(dir, "test.db")},
	}
	cfgStore := config.NewStoreFromConfig(cfg)
	llmClient := llm.New(cfgStore)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return NewRouter(ctx, cfgStore, llmClient, nil, make(map[string]*AgentResources))
}

func fakeMsg(guildID, channelID, userID string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "msg-" + userID,
			ChannelID: channelID,
			GuildID:   guildID,
			Content:   "hello",
			Author:    &discordgo.User{ID: userID, Bot: false},
		},
	}
}

func TestRouteIgnoresUnknownServer(t *testing.T) {
	r := newTestRouter(t)
	// guild1 is not in agentsByServerID and not in config.Agents,
	// so Route should silently return without panicking or creating an agent.
	r.Route(fakeMsg("guild1", "chan1", "user1"))

	statuses := r.Status()
	for _, s := range statuses {
		if s.ChannelID == "chan1" {
			t.Error("expected no agent for unconfigured server, but found one in Status()")
		}
	}
}

func TestUnloadAgentEvictsFromCache(t *testing.T) {
	r := newTestRouter(t)

	// Manually register a fake agent resource for srv1
	r.mu.Lock()
	r.agentsByServerID["srv1"] = &AgentResources{
		Config:  &config.AgentConfig{},
		Memory:  nil,
		Session: nil,
	}
	r.mu.Unlock()

	r.UnloadAgent("srv1")

	r.mu.Lock()
	_, exists := r.agentsByServerID["srv1"]
	r.mu.Unlock()

	if exists {
		t.Error("UnloadAgent() should remove server from agentsByServerID")
	}
}

func TestUnloadAgentNonExistent(t *testing.T) {
	r := newTestRouter(t)
	// Should not panic when unloading a server that was never loaded
	r.UnloadAgent("nonexistent-server")
}

func registerFakeAgent(t *testing.T, r *Router, serverID string, ignoreUsers []string) {
	t.Helper()
	r.mu.Lock()
	r.agentsByServerID[serverID] = &AgentResources{
		Config:  &config.AgentConfig{IgnoreUsers: ignoreUsers},
		Memory:  nil,
		Session: nil,
	}
	r.mu.Unlock()
}

func TestSpamBlockAfterThreshold(t *testing.T) {
	r := newTestRouter(t)

	// Call checkSpam spamThreshold-1 times — should not be blocked yet.
	r.mu.Lock()
	for i := 0; i < spamThreshold-1; i++ {
		blocked, _ := r.checkSpam("srv1", "user1")
		if blocked {
			r.mu.Unlock()
			t.Fatalf("user blocked early at call %d", i+1)
		}
	}

	// The threshold-th call triggers the block.
	blocked, justBlocked := r.checkSpam("srv1", "user1")
	r.mu.Unlock()

	if !blocked {
		t.Error("user should be blocked after reaching threshold")
	}
	if !justBlocked {
		t.Error("justBlocked should be true on the triggering call")
	}

	// Subsequent call should return blocked but not justBlocked.
	r.mu.Lock()
	blocked2, justBlocked2 := r.checkSpam("srv1", "user1")
	r.mu.Unlock()

	if !blocked2 {
		t.Error("user should remain blocked")
	}
	if justBlocked2 {
		t.Error("justBlocked should be false for subsequent blocked calls")
	}
}

func TestSpamBlockExpires(t *testing.T) {
	r := newTestRouter(t)

	// Manually set an expired block.
	r.mu.Lock()
	r.spamMap["srv1:user1"] = &spamRecord{
		blockedUntil: time.Now().Add(-1 * time.Second),
	}
	blocked, _ := r.checkSpam("srv1", "user1")
	r.mu.Unlock()

	if blocked {
		t.Error("expired block should not still block the user")
	}
}

func TestIgnoreUserDropsMessage(t *testing.T) {
	r := newTestRouter(t)
	registerFakeAgent(t, r, "srv1", []string{"ignored-user"})

	r.Route(fakeMsg("srv1", "chan1", "ignored-user"))

	// No agent should have been spawned for the ignored user.
	r.mu.Lock()
	_, exists := r.agents["chan1"]
	r.mu.Unlock()
	if exists {
		t.Error("ignored user message should not spawn an agent")
	}
}
