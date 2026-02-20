package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/memory"
	"github.com/tomasmach/mnemon-bot/soul"
	"github.com/tomasmach/mnemon-bot/tools"
)

// ChannelAgent is a per-channel conversation goroutine.
type ChannelAgent struct {
	channelID string
	serverID  string
	cfg       *config.Config
	llm       *llm.Client
	mem       *memory.Store
	session   *discordgo.Session
	soulText  string
	history   []llm.Message               // capped to cfg.Agent.HistoryLimit
	msgCh     chan *discordgo.MessageCreate // buffered 100
}

func newChannelAgent(channelID, serverID string, cfg *config.Config, llmClient *llm.Client, mem *memory.Store, session *discordgo.Session) *ChannelAgent {
	return &ChannelAgent{
		channelID: channelID,
		serverID:  serverID,
		cfg:       cfg,
		llm:       llmClient,
		mem:       mem,
		session:   session,
		soulText:  soul.Load(cfg, serverID),
		history:   make([]llm.Message, 0),
		msgCh:     make(chan *discordgo.MessageCreate, 100),
	}
}

func (a *ChannelAgent) run(ctx context.Context) {
	idleTimeout := time.Duration(a.cfg.Agent.IdleTimeoutMinutes) * time.Minute
	for {
		select {
		case msg := <-a.msgCh:
			a.handleMessage(ctx, msg)
		case <-time.After(idleTimeout):
			slog.Info("channel agent idle timeout", "channel_id", a.channelID)
			return
		case <-ctx.Done():
			// drain with 30s deadline
			deadline := time.After(30 * time.Second)
			for {
				select {
				case msg := <-a.msgCh:
					drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					a.handleMessage(drainCtx, msg)
					cancel()
				case <-deadline:
					return
				default:
					return
				}
			}
		}
	}
}

func (a *ChannelAgent) handleMessage(ctx context.Context, msg *discordgo.MessageCreate) {
	// 1. Determine serverID
	serverID := a.serverID
	if msg.GuildID == "" {
		serverID = "DM:" + msg.Author.ID
	}

	// 2. Check response mode
	mode := a.cfg.ResolveResponseMode(serverID, msg.ChannelID)
	switch mode {
	case "none":
		return
	case "mention":
		isDM := msg.GuildID == ""
		isMentioned := strings.Contains(msg.Content, "<@"+a.session.State.User.ID+">")
		if !isDM && !isMentioned {
			return
		}
	case "all":
		// always respond
	case "smart":
		// always respond; model decides whether to use reply tool
	}

	// 3. Recall memories
	memories, err := a.mem.Recall(ctx, msg.Content, serverID, 10)
	if err != nil {
		slog.Warn("memory recall error", "error", err, "channel_id", a.channelID)
	}

	// 4. Build system prompt
	systemPrompt := a.soulText
	if len(memories) > 0 {
		systemPrompt += "\n\n## Relevant Memories\n"
		for _, m := range memories {
			systemPrompt += fmt.Sprintf("- [%s] %s\n", m.ID, m.Content)
		}
	}

	// 5. Set up callbacks
	sendFn := func(content string) error {
		_, err := a.session.ChannelMessageSend(msg.ChannelID, content)
		return err
	}
	reactFn := func(emoji string) error {
		return a.session.MessageReactionAdd(msg.ChannelID, msg.ID, emoji)
	}

	// 6. Build tool registry
	reg := tools.NewDefaultRegistry(a.mem, serverID, sendFn, reactFn, a.cfg.Tools.WebSearchKey)

	// 7. Add user message to history
	userMsg := llm.Message{Role: "user", Content: fmt.Sprintf("%s: %s", msg.Author.Username, msg.Content)}
	msgs := append(append([]llm.Message(nil), a.history...), userMsg)

	// 8. Tool-call loop (up to MaxToolIterations)
	var assistantContent string
	for iter := 0; iter < a.cfg.Agent.MaxToolIterations; iter++ {
		choice, err := a.llm.Chat(ctx, buildMessages(systemPrompt, msgs), reg.Definitions())
		if err != nil {
			slog.Error("llm chat error", "error", err, "channel_id", a.channelID)
			sendFn("I encountered an error. Please try again.")
			return
		}

		if len(choice.Message.ToolCalls) == 0 {
			assistantContent = choice.Message.Content
			break
		}

		// append assistant message with tool calls
		msgs = append(msgs, choice.Message)

		// dispatch each tool call
		for _, tc := range choice.Message.ToolCalls {
			result, err := reg.Dispatch(ctx, tc.Function.Name, []byte(tc.Function.Arguments))
			if err != nil {
				slog.Warn("tool dispatch error", "tool", tc.Function.Name, "error", err)
				result = fmt.Sprintf("Error: %s", err)
			}
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}

		if iter == a.cfg.Agent.MaxToolIterations-1 {
			sendFn("I got stuck in a loop. Please try again.")
			return
		}
	}

	// 9. If assistant replied with text content (not via reply tool), send it
	if assistantContent != "" {
		parts := splitMessage(assistantContent, 2000)
		for _, p := range parts {
			sendFn(p)
		}
	}

	// 10. Update history
	// If the assistant replied with plain text content, append it to msgs before saving history.
	// (Tool-based replies are already captured in msgs via the tool-call loop.)
	if assistantContent != "" {
		msgs = append(msgs, llm.Message{Role: "assistant", Content: assistantContent})
	}
	a.history = make([]llm.Message, len(msgs))
	copy(a.history, msgs)
	// trim to HistoryLimit
	if len(a.history) > a.cfg.Agent.HistoryLimit {
		a.history = a.history[len(a.history)-a.cfg.Agent.HistoryLimit:]
	}
}

// buildMessages constructs the message slice for the LLM with system prompt prepended.
func buildMessages(systemPrompt string, history []llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})
	msgs = append(msgs, history...)
	return msgs
}

// splitMessage splits s into chunks of at most limit runes.
func splitMessage(s string, limit int) []string {
	runes := []rune(s)
	if len(runes) <= limit {
		return []string{s}
	}
	var parts []string
	for len(runes) > 0 {
		end := limit
		if end > len(runes) {
			end = len(runes)
		}
		parts = append(parts, string(runes[:end]))
		runes = runes[end:]
	}
	return parts
}
