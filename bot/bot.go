// Package bot provides the Discord gateway wrapper and message routing.
package bot

import (
	"log/slog"
	"sync"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/agent"
	"github.com/tomasmach/vespra/config"
)

// AgentOps abstracts agent configuration operations so the bot package
// does not depend on the web package directly.
type AgentOps interface {
	UpsertAgent(input config.AgentConfig) error
	UpdateAgentMode(serverID, mode string) error
	UpdateAgentChannel(serverID, channelID, mode string) error
	UpdateAgentLanguage(serverID, language string) error
	CfgStore() *config.Store
}

// Bot wraps the Discord session and message routing.
type Bot struct {
	session          *discordgo.Session
	router           *agent.Router
	ops              AgentOps
	wizard           *wizardHandler
	registeredGuilds sync.Map // tracks guild IDs that have had commands registered
}

// New creates a new Bot, configures intents, and registers message handlers.
// The router must be set via SetRouter before the bot starts receiving messages.
func New(token string) (*Bot, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}

	session.Identify.Intents = discordgo.IntentsGuilds |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent

	b := &Bot{session: session}
	session.AddHandler(b.onMessageCreate)
	session.AddHandler(b.onInteractionCreate)
	session.AddHandler(b.onGuildCreate)

	return b, nil
}

// Session returns the underlying Discord session.
func (b *Bot) Session() *discordgo.Session {
	return b.session
}

// SetRouter wires a Router into the bot for message dispatch.
func (b *Bot) SetRouter(r *agent.Router) {
	b.router = r
}

// SetOps wires an AgentOps implementation into the bot, enabling slash command handling.
// Must be called before the bot starts receiving interactions.
func (b *Bot) SetOps(ops AgentOps) {
	b.ops = ops
	b.wizard = newWizardHandler(ops)
}

// onGuildCreate registers slash commands when the bot joins a guild for the first time
// in this process lifetime. Unavailable guilds (Discord outages or unresolved stubs)
// are skipped, and already-registered guilds are skipped to avoid redundant API calls
// on reconnect (command definitions are static so BulkOverwrite need not be repeated).
func (b *Bot) onGuildCreate(s *discordgo.Session, g *discordgo.GuildCreate) {
	if b.ops == nil || g.Unavailable {
		return
	}
	if _, loaded := b.registeredGuilds.LoadOrStore(g.ID, struct{}{}); loaded {
		return
	}
	RegisterCommands(s, g.ID)
}

// onInteractionCreate dispatches incoming interactions to the appropriate handler.
func (b *Bot) onInteractionCreate(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if b.ops == nil {
		return
	}
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		b.handleSlashCommand(s, i)
	case discordgo.InteractionMessageComponent:
		if b.wizard != nil {
			b.wizard.handleComponent(s, i)
		}
	}
}

// Start opens the Discord gateway connection.
func (b *Bot) Start() error {
	return b.session.Open()
}

// Stop closes the Discord gateway connection.
func (b *Bot) Stop() error {
	return b.session.Close()
}

// onMessageCreate handles incoming Discord messages.
func (b *Bot) onMessageCreate(s *discordgo.Session, msg *discordgo.MessageCreate) {
	if msg.Author == nil {
		return
	}
	if msg.Author.ID == s.State.User.ID || msg.Author.Bot {
		return
	}
	// Skip Discord system messages (pins, joins, boosts, etc.); only handle actual user messages.
	if msg.Type != discordgo.MessageTypeDefault && msg.Type != discordgo.MessageTypeReply {
		return
	}
	if b.router == nil {
		slog.Warn("message received but router not set, dropping", "channel_id", msg.ChannelID)
		return
	}
	b.router.Route(msg)
}
