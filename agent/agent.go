// Package agent manages per-channel conversation goroutines and the agent router.
package agent

import (
	"context"
	"encoding/json"
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
	channelID string
	serverID  string

	cfgStore  *config.Store
	llm       *llm.Client
	resources *AgentResources
	logger    *slog.Logger

	soulText   string
	history    []llm.Message  // capped to cfg.Agent.HistoryLimit
	lastActive atomic.Int64   // UnixNano; written by agent goroutine, read by Status()

	msgCh chan *discordgo.MessageCreate // buffered 100
}

// buildUserMessage converts a Discord message into an llm.Message, attaching
// any image URLs as vision content parts when present.
func buildUserMessage(msg *discordgo.MessageCreate) llm.Message {
	text := fmt.Sprintf("%s: %s", msg.Author.Username, msg.Content)

	var images []*discordgo.MessageAttachment
	for _, a := range msg.Attachments {
		if strings.HasPrefix(a.ContentType, "image/") {
			images = append(images, a)
		}
	}
	if len(images) == 0 {
		return llm.Message{Role: "user", Content: text}
	}

	parts := make([]llm.ContentPart, 0, 1+len(images))
	parts = append(parts, llm.ContentPart{Type: "text", Text: text})
	for _, a := range images {
		parts = append(parts, llm.ContentPart{
			Type:     "image_url",
			ImageURL: &llm.ImageURL{URL: a.URL},
		})
	}
	return llm.Message{Role: "user", ContentParts: parts}
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
		logger:    slog.With("server_id", serverID, "channel_id", channelID),
	}
}

func (a *ChannelAgent) run(ctx context.Context) {
	idleTimeout := time.Duration(a.cfgStore.Get().Agent.IdleTimeoutMinutes) * time.Minute
	for {
		select {
		case msg := <-a.msgCh:
			a.handleMessage(ctx, msg)
		case <-time.After(idleTimeout):
			a.logger.Info("channel agent idle timeout")
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

func (a *ChannelAgent) backfillHistory(ctx context.Context, beforeID string) []llm.Message {
	limit := a.cfgStore.Get().Agent.HistoryBackfillLimit
	if limit <= 0 {
		return nil
	}
	if limit > 100 {
		limit = 100 // Discord API max
	}
	msgs, err := a.resources.Session.ChannelMessages(a.channelID, limit, beforeID, "", "")
	if err != nil {
		a.logger.Warn("failed to backfill channel history", "error", err)
		return nil
	}
	botID := a.resources.Session.State.User.ID
	// msgs is newest-first; reverse to chronological order
	history := make([]llm.Message, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Author == nil || m.Content == "" {
			continue
		}
		if m.Author.ID == botID {
			history = append(history, llm.Message{Role: "assistant", Content: m.Content})
		} else if !m.Author.Bot {
			history = append(history, llm.Message{Role: "user", Content: fmt.Sprintf("%s: %s", m.Author.Username, m.Content)})
		}
	}
	return history
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

	if len(a.history) == 0 {
		a.history = a.backfillHistory(ctx, msg.ID)
		if len(a.history) > cfg.Agent.HistoryLimit {
			a.history = a.history[len(a.history)-cfg.Agent.HistoryLimit:]
		}
	}

	// Recall memories
	memories, err := a.resources.Memory.Recall(ctx, msg.Content, a.serverID, 10)
	if err != nil {
		a.logger.Warn("memory recall error", "error", err)
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
	userMsg := buildUserMessage(msg)
	msgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(msgs, a.history)
	msgs = append(msgs, userMsg)

	// Tool-call loop
	type toolCallRecord struct {
		Name   string `json:"name"`
		Result string `json:"result"`
	}
	var toolCalls []toolCallRecord
	var assistantContent string
	for iter := 0; iter < cfg.Agent.MaxToolIterations; iter++ {
		choice, err := a.llm.Chat(ctx, buildMessages(systemPrompt, msgs), reg.Definitions())
		if err != nil {
			a.logger.Error("llm chat error", "error", err)
			if err := sendFn("I encountered an error. Please try again."); err != nil {
				a.logger.Error("send message", "err", err)
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
			a.logger.Debug("tool call", "tool", tc.Function.Name)
			result, err := reg.Dispatch(ctx, tc.Function.Name, []byte(tc.Function.Arguments))
			if err != nil {
				a.logger.Warn("tool dispatch error", "tool", tc.Function.Name, "error", err)
				result = fmt.Sprintf("Error: %s", err)
			}
			toolCalls = append(toolCalls, toolCallRecord{Name: tc.Function.Name, Result: result})
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}

		if iter == cfg.Agent.MaxToolIterations-1 {
			if err := sendFn("I got stuck in a loop. Please try again."); err != nil {
				a.logger.Error("send message", "err", err)
			}
			return
		}
	}

	// Log conversation on success — either plain-text reply or reply-tool response.
	if assistantContent != "" || reg.Replied {
		var toolCallsJSON string
		if len(toolCalls) > 0 {
			if b, err := json.Marshal(toolCalls); err == nil {
				toolCallsJSON = string(b)
			}
		}
		responseText := assistantContent
		if responseText == "" && reg.Replied {
			responseText = reg.ReplyText
		}
		// Use the formatted user message (as seen by the LLM), not the raw Discord content.
		userMsgText := fmt.Sprintf("%s: %s", msg.Author.Username, msg.Content)
		if err := a.resources.Memory.LogConversation(ctx, a.channelID, userMsgText, toolCallsJSON, responseText); err != nil {
			a.logger.Warn("log conversation error", "error", err)
		}
	}

	// If assistant replied with text content (not via reply tool), send it
	if assistantContent != "" && !reg.Replied {
		parts := tools.SplitMessage(assistantContent, 2000)
		for _, p := range parts {
			if err := sendFn(p); err != nil {
				a.logger.Error("send message", "err", err)
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
