package agent

import (
	"context"
	"path/filepath"
	"testing"

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
