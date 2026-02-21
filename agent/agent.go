// Package agent manages per-channel conversation goroutines and the agent router.
package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/soul"
	"github.com/tomasmach/mnemon-bot/tools"
)

// ChannelAgent is a per-channel conversation goroutine.
type ChannelAgent struct {
	channelID  string
	serverID   string
	cfgStore   *config.Store
	llm        *llm.Client
	resources  *AgentResources
	soulText   string
	history    []llm.Message                // capped to cfg.Agent.HistoryLimit
	msgCh      chan *discordgo.MessageCreate // buffered 100
	lastActive atomic.Int64                 // UnixNano; written by agent goroutine, read by Status()
}

func newChannelAgent(channelID, serverID string, cfgStore *config.Store, llmClient *llm.Client, resources *AgentResources) *ChannelAgent {
	return &ChannelAgent{
		channelID: channelID,
		serverID:  serverID,
		cfgStore:  cfgStore,
		llm:       llmClient,
		resources: resources,
		soulText:  soul.Load(cfgStore.Get(), serverID),
		history:   make([]llm.Message, 0),
		msgCh:     make(chan *discordgo.MessageCreate, 100),
	}
}

func (a *ChannelAgent) run(ctx context.Context) {
	idleTimeout := time.Duration(a.cfgStore.Get().Agent.IdleTimeoutMinutes) * time.Minute
	for {
		select {
		case msg := <-a.msgCh:
			a.handleMessage(ctx, msg)
		case <-time.After(idleTimeout):
			slog.Info("channel agent idle timeout", "channel_id", a.channelID)
			return
		case <-ctx.Done():
			// drain only messages already buffered; no new ones can arrive after b.Stop()
			n := len(a.msgCh)
			for i := 0; i < n; i++ {
				msg := <-a.msgCh
				drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				a.handleMessage(drainCtx, msg)
				cancel()
			}
			return
		}
	}
}

func (a *ChannelAgent) handleMessage(ctx context.Context, msg *discordgo.MessageCreate) {
	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()

	// Check response mode
	mode := cfg.ResolveResponseMode(a.serverID, msg.ChannelID)
	switch mode {
	case "none":
		return
	case "mention":
		isDM := msg.GuildID == ""
		isMentioned := strings.Contains(msg.Content, "<@"+a.resources.Session.State.User.ID+">")
		isReplyToBot := msg.MessageReference != nil &&
			msg.ReferencedMessage != nil &&
			msg.ReferencedMessage.Author != nil &&
			msg.ReferencedMessage.Author.ID == a.resources.Session.State.User.ID
		if !isDM && !isMentioned && !isReplyToBot {
			return
		}
	case "all":
		// always respond
	case "smart":
		// always respond; model decides whether to use reply tool
	}

	// Recall memories
	memories, err := a.resources.Memory.Recall(ctx, msg.Content, a.serverID, 10)
	if err != nil {
		slog.Warn("memory recall error", "error", err, "channel_id", a.channelID)
	}

	// Build system prompt
	systemPrompt := a.soulText
	if len(memories) > 0 {
		systemPrompt += "\n\n## Relevant Memories\n"
		for _, m := range memories {
			systemPrompt += fmt.Sprintf("- [%s] %s\n", m.ID, m.Content)
		}
	}

	// Set up callbacks
	sendFn := func(content string) error {
		_, err := a.resources.Session.ChannelMessageSend(msg.ChannelID, content)
		return err
	}
	reactFn := func(emoji string) error {
		return a.resources.Session.MessageReactionAdd(msg.ChannelID, msg.ID, emoji)
	}

	// Build tool registry
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, cfg.Tools.WebSearchKey)

	// Add user message to history
	userMsg := llm.Message{Role: "user", Content: fmt.Sprintf("%s: %s", msg.Author.Username, msg.Content)}
	msgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(msgs, a.history)
	msgs = append(msgs, userMsg)

	// Tool-call loop
	var assistantContent string
	for iter := 0; iter < cfg.Agent.MaxToolIterations; iter++ {
		choice, err := a.llm.Chat(ctx, buildMessages(systemPrompt, msgs), reg.Definitions())
		if err != nil {
			slog.Error("llm chat error", "error", err, "channel_id", a.channelID)
			if err := sendFn("I encountered an error. Please try again."); err != nil {
				slog.Error("send message", "err", err)
			}
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
			slog.Info("tool call", "tool", tc.Function.Name, "channel_id", a.channelID)
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

		if iter == cfg.Agent.MaxToolIterations-1 {
			if err := sendFn("I got stuck in a loop. Please try again."); err != nil {
				slog.Error("send message", "err", err)
			}
			return
		}
	}

	// If assistant replied with text content (not via reply tool), send it
	if assistantContent != "" && !reg.Replied {
		parts := tools.SplitMessage(assistantContent, 2000)
		for _, p := range parts {
			if err := sendFn(p); err != nil {
				slog.Error("send message", "err", err)
			}
		}
	}

	// Update history
	if assistantContent != "" {
		msgs = append(msgs, llm.Message{Role: "assistant", Content: assistantContent})
	}
	if len(msgs) > cfg.Agent.HistoryLimit {
		msgs = msgs[len(msgs)-cfg.Agent.HistoryLimit:]
	}
	a.history = msgs
}

// buildMessages constructs the message slice for the LLM with system prompt prepended.
func buildMessages(systemPrompt string, history []llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})
	msgs = append(msgs, history...)
	return msgs
}
