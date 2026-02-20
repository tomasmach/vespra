package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/memory"
)

// ChannelStatus describes the current state of an active channel agent.
type ChannelStatus struct {
	ChannelID  string    `json:"channel_id"`
	ServerID   string    `json:"server_id"`
	LastActive time.Time `json:"last_active"`
	QueueDepth int       `json:"queue_depth"`
}

// AgentResources holds the Discord session and memory store for a configured agent.
type AgentResources struct {
	Config  *config.AgentConfig
	Memory  *memory.Store
	Session *discordgo.Session
}

// Router manages per-channel ChannelAgents.
type Router struct {
	mu               sync.Mutex
	agents           map[string]*ChannelAgent // keyed by channelID
	ctx              context.Context
	cfgStore         *config.Store
	llm              *llm.Client
	agentsByServerID map[string]*AgentResources
	wg               sync.WaitGroup
}

// NewRouter creates a new Router.
func NewRouter(ctx context.Context, cfgStore *config.Store, llmClient *llm.Client, agentsByServerID map[string]*AgentResources) *Router {
	return &Router{
		agents:           make(map[string]*ChannelAgent),
		ctx:              ctx,
		cfgStore:         cfgStore,
		llm:              llmClient,
		agentsByServerID: agentsByServerID,
	}
}

// Route delivers a message to the appropriate channel agent, spawning one if needed.
func (r *Router) Route(msg *discordgo.MessageCreate) {
	channelID := msg.ChannelID
	serverID := msg.GuildID
	if serverID == "" {
		serverID = "DM:" + msg.Author.ID
	}

	resources, ok := r.agentsByServerID[serverID]
	if !ok {
		return // unconfigured server, silently ignore
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if agent, ok := r.agents[channelID]; ok {
		select {
		case agent.msgCh <- msg:
			return
		default:
			// buffer full or agent gone — respawn
			slog.Warn("agent buffer full or gone, respawning", "channel_id", channelID)
			delete(r.agents, channelID)
		}
	}

	// spawn new agent
	a := newChannelAgent(channelID, serverID, r.cfgStore, r.llm, resources)
	r.agents[channelID] = a
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		a.run(r.ctx)
		r.mu.Lock()
		if r.agents[channelID] == a {
			delete(r.agents, channelID)
		}
		r.mu.Unlock()
	}()
	a.msgCh <- msg // guaranteed to succeed (buffer just created, size 100)
}

// MemoryForServer returns the memory store for a configured server, or nil if not configured.
func (r *Router) MemoryForServer(serverID string) *memory.Store {
	if res, ok := r.agentsByServerID[serverID]; ok {
		return res.Memory
	}
	return nil
}

// Status returns a snapshot of all active channel agents.
func (r *Router) Status() []ChannelStatus {
	r.mu.Lock()
	defer r.mu.Unlock()

	statuses := make([]ChannelStatus, 0, len(r.agents))
	for _, a := range r.agents {
		statuses = append(statuses, ChannelStatus{
			ChannelID:  a.channelID,
			ServerID:   a.serverID,
			LastActive: time.Unix(0, a.lastActive.Load()),
			QueueDepth: len(a.msgCh),
		})
	}
	return statuses
}

// WaitForDrain waits for all active agents to finish, up to 30 seconds.
func (r *Router) WaitForDrain() {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		slog.Warn("drain timeout: some agents did not finish within 30s")
	}
}
