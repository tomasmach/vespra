// Package bot provides the Discord gateway wrapper and message routing.
package bot

import (
	"log/slog"

	"github.com/bwmarrin/discordgo"
	"github.com/tomasmach/mnemon-bot/agent"
)

// Bot wraps the Discord session and message routing.
type Bot struct {
	session *discordgo.Session
	router  *agent.Router
}

// New creates a new Bot, configures intents, and registers message handlers.
// The router must be set via SetRouter before the bot starts receiving messages.
func New(token string) (*Bot, error) {
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent

	b := &Bot{session: session}
	session.AddHandler(b.onMessageCreate)

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
	if b.router == nil {
		slog.Warn("message received but router not set, dropping", "channel_id", msg.ChannelID)
		return
	}
	b.router.Route(msg)
}
