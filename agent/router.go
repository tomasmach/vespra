package agent

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/memory"
)

const (
	spamWindow    = 30 * time.Second
	spamThreshold = 10
	spamCooldown  = 60 * time.Minute
)

type spamRecord struct {
	timestamps   []time.Time
	blockedUntil time.Time
}

// ChannelStatus describes the current state of an active channel agent.
type ChannelStatus struct {
	ChannelID  string    `json:"channel_id"`
	ServerID   string    `json:"server_id"`
	LastActive time.Time `json:"last_active"`
	QueueDepth int       `json:"queue_depth"`
}

// AgentResources holds the config, memory store, and Discord session for a configured agent.
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
	defaultSession   *discordgo.Session
	agentsByServerID map[string]*AgentResources
	dmMemory         *memory.Store
	wg               sync.WaitGroup
	spamMap          map[string]*spamRecord // key: "serverID:userID", protected by mu
}

// NewRouter creates a new Router.
func NewRouter(ctx context.Context, cfgStore *config.Store, llmClient *llm.Client, defaultSession *discordgo.Session, agentsByServerID map[string]*AgentResources) *Router {
	cfg := cfgStore.Get()
	dmMem, err := memory.New(&config.MemoryConfig{DBPath: config.ExpandPath(cfg.Memory.DBPath)}, llmClient)
	if err != nil {
		slog.Error("failed to open DM memory store", "error", err)
	}
	return &Router{
		agents:           make(map[string]*ChannelAgent),
		ctx:              ctx,
		cfgStore:         cfgStore,
		llm:              llmClient,
		defaultSession:   defaultSession,
		agentsByServerID: agentsByServerID,
		dmMemory:         dmMem,
		spamMap:          make(map[string]*spamRecord),
	}
}

// Route delivers a message to the appropriate channel agent, spawning one if needed.
func (r *Router) Route(msg *discordgo.MessageCreate) {
	channelID := msg.ChannelID
	serverID := msg.GuildID
	if serverID == "" {
		serverID = "DM:" + msg.Author.ID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	resources, ok := r.agentsByServerID[serverID]
	if !ok {
		resources = r.tryHotLoad(serverID)
		if resources == nil {
			return // unconfigured server, silently ignore
		}
	}

	// Check manual ignore list.
	if resources.Config != nil && slices.Contains(resources.Config.IgnoreUsers, msg.Author.ID) {
		return
	}

	// Check spam rate limit.
	blocked, justBlocked := r.checkSpam(serverID, msg.Author.ID)
	if blocked {
		if justBlocked {
			if session := resources.Session; session != nil {
				cid := channelID
				uid := msg.Author.ID
				go session.ChannelMessageSend(cid, fmt.Sprintf("<@%s> You've been sending too many messages. I'll be back in %s.", uid, spamCooldown))
			}
		}
		return
	}

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
	r.mu.Lock()
	defer r.mu.Unlock()
	if res, ok := r.agentsByServerID[serverID]; ok {
		return res.Memory
	}
	if res := r.tryHotLoad(serverID); res != nil {
		return res.Memory
	}
	return nil
}

// tryHotLoad checks cfgStore for a newly added agent and loads it into agentsByServerID.
// Must be called with r.mu held. Only loads agents without custom tokens (those require restart).
// DM server IDs (prefixed with "DM:") are handled specially: they use the global default config
// and the default memory store so DMs are always served without requiring explicit configuration.
// Returns the resources if successfully loaded, nil otherwise.
func (r *Router) tryHotLoad(serverID string) *AgentResources {
	cfg := r.cfgStore.Get()

	if strings.HasPrefix(serverID, "DM:") {
		if r.dmMemory == nil {
			slog.Error("DM memory store not initialized", "server_id", serverID)
			return nil
		}
		res := &AgentResources{Config: &config.AgentConfig{}, Memory: r.dmMemory, Session: r.defaultSession}
		r.agentsByServerID[serverID] = res
		slog.Info("created DM agent resources", "server_id", serverID)
		return res
	}

	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		if a.ServerID != serverID {
			continue
		}
		if a.Token != "" {
			// Custom token agents require a restart to open a new Discord session.
			slog.Warn("agent has custom token and was added after startup; restart required", "agent", a.ID)
			return nil
		}
		mem, err := memory.New(&config.MemoryConfig{DBPath: a.ResolveDBPath(cfg.Memory.DBPath)}, r.llm)
		if err != nil {
			slog.Error("failed to hot-load memory store for agent", "agent", a.ID, "error", err)
			return nil
		}
		res := &AgentResources{Config: a, Memory: mem, Session: r.defaultSession}
		r.agentsByServerID[serverID] = res
		slog.Info("hot-loaded agent from config", "agent", a.ID, "server_id", serverID)
		return res
	}
	return nil
}

// UnloadAgent removes a hot-loaded agent from the in-memory cache.
// The next message to that server will find no entry and be silently ignored.
func (r *Router) UnloadAgent(serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.agentsByServerID, serverID)
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

// checkSpam checks whether a user on a server is sending too many messages.
// Must be called with r.mu held.
// Returns (blocked, justBlocked): blocked=true means the message should be dropped;
// justBlocked=true means this call is what triggered the block (send a notification).
func (r *Router) checkSpam(serverID, userID string) (blocked bool, justBlocked bool) {
	// For DMs, serverID is "DM:<userID>", so the key becomes "DM:<userID>:<userID>" (user ID appears twice, but is still unique and consistent).
	key := serverID + ":" + userID
	now := time.Now()

	rec, ok := r.spamMap[key]
	if !ok {
		rec = &spamRecord{}
		r.spamMap[key] = rec
	}

	if now.Before(rec.blockedUntil) {
		return true, false
	}

	// Trim timestamps outside the window.
	cutoff := now.Add(-spamWindow)
	i := 0
	for i < len(rec.timestamps) && rec.timestamps[i].Before(cutoff) {
		i++
	}
	rec.timestamps = append(rec.timestamps[i:], now)

	if len(rec.timestamps) >= spamThreshold {
		rec.blockedUntil = now.Add(spamCooldown)
		rec.timestamps = rec.timestamps[:0]
		slog.Warn("spam block applied", "server_id", serverID, "user_id", userID)
		return true, true
	}

	return false, false
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
