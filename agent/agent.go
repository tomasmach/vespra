// Package agent manages per-channel conversation goroutines and the agent router.
package agent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/soul"
	"github.com/tomasmach/mnemon-bot/tools"
)

const extractionPrompt = `You are a memory extraction assistant. Your only job is to analyze the conversation and save important information to long-term memory.

Save a memory for each of the following you find:
- User preferences or opinions (favourites, dislikes, how they like to be addressed)
- Personal facts (location, job, age, relationships, pronouns, pets, hobbies, skills)
- Decisions made or actions agreed upon
- Goals, plans, or ongoing projects
- Tasks or follow-ups the user wants to remember
- Anything the user explicitly asked to be remembered

Before saving each memory, call memory_recall to check if it already exists. Skip duplicates.
Do not save trivial small talk or anything unlikely to be useful later.
When you have finished saving all notable memories, stop.`

// ChannelAgent is a per-channel conversation goroutine.
type ChannelAgent struct {
	channelID string
	serverID  string

	cfgStore   *config.Store
	llm        *llm.Client
	httpClient *http.Client
	resources  *AgentResources
	logger     *slog.Logger

	soulText   string
	history    []llm.Message // capped to cfg.Agent.HistoryLimit
	turnCount  int           // incremented each completed turn; triggers background extraction
	lastActive atomic.Int64  // UnixNano; written by agent goroutine, read by Status()

	msgCh chan *discordgo.MessageCreate // buffered 100
}

// historyUserContent formats the text content for a user message in backfilled
// history, annotating reply-to context when the message is a Discord reply.
func historyUserContent(m *discordgo.Message) string {
	if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil {
		return fmt.Sprintf("%s (replying to %s): %s", m.Author.Username, m.ReferencedMessage.Author.Username, m.Content)
	}
	return fmt.Sprintf("%s: %s", m.Author.Username, m.Content)
}

// buildUserMessage converts a Discord message into an llm.Message, downloading
// any image attachments as base64 data URLs for vision content parts.
// Discord CDN URLs require authentication, so images must be fetched server-side.
func buildUserMessage(ctx context.Context, httpClient *http.Client, msg *discordgo.MessageCreate) llm.Message {
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
		dataURL, err := downloadImageAsDataURL(ctx, httpClient, a)
		if err != nil {
			slog.Warn("failed to download image attachment, skipping", "error", err, "url", a.URL)
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type:     "image_url",
			ImageURL: &llm.ImageURL{URL: dataURL},
		})
	}
	if len(parts) == 1 {
		// all image downloads failed; fall back to plain text
		return llm.Message{Role: "user", Content: text}
	}
	return llm.Message{Role: "user", ContentParts: parts}
}

// downloadImageAsDataURL fetches an image attachment and returns it encoded as a base64 data URL.
func downloadImageAsDataURL(ctx context.Context, client *http.Client, a *discordgo.MessageAttachment) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build image request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch image: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read image body: %w", err)
	}
	mediaType := a.ContentType
	if mediaType == "" {
		mediaType = "image/jpeg"
	}
	return fmt.Sprintf("data:%s;base64,%s", mediaType, base64.StdEncoding.EncodeToString(data)), nil
}

func newChannelAgent(channelID, serverID string, cfgStore *config.Store, llmClient *llm.Client, resources *AgentResources) *ChannelAgent {
	return &ChannelAgent{
		channelID:  channelID,
		serverID:   serverID,
		cfgStore:   cfgStore,
		llm:        llmClient,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		resources:  resources,
		soulText: soul.Load(cfgStore.Get(), serverID),
		msgCh:    make(chan *discordgo.MessageCreate, 100),
		logger:     slog.With("server_id", serverID, "channel_id", channelID),
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
			history = append(history, llm.Message{Role: "user", Content: historyUserContent(m)})
		}
	}
	return history
}

func (a *ChannelAgent) handleMessage(ctx context.Context, msg *discordgo.MessageCreate) {
	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()

	// Check response mode
	mode := cfg.ResolveResponseMode(a.serverID, msg.ChannelID)
	botID := a.resources.Session.State.User.ID
	isDM := msg.GuildID == ""
	isMentioned := strings.Contains(msg.Content, "<@"+botID+">")
	isReplyToBot := msg.MessageReference != nil &&
		msg.ReferencedMessage != nil &&
		msg.ReferencedMessage.Author != nil &&
		msg.ReferencedMessage.Author.ID == botID
	isDirectlyAddressed := isDM || isMentioned || isReplyToBot
	switch mode {
	case "none":
		return
	case "mention":
		if !isDirectlyAddressed {
			return
		}
	case "all":
		// always respond
	case "smart":
		// model responds only via reply/react tools; plain-text output without a tool call is suppressed
	}

	stopTyping := func() {}
	if mode != "smart" || isDirectlyAddressed {
		stopTyping = a.startTyping(ctx)
	}
	defer stopTyping()

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
	if lang := cfg.ResolveLanguage(a.serverID, msg.ChannelID); lang != "" {
		systemPrompt += "\n\nAlways respond in " + lang + "."
	}
	if mode == "smart" {
		systemPrompt += "\n\nYou are in smart mode. Only respond via the `reply` or `react` tools when the message genuinely warrants a response. If you choose not to respond, produce no output at all — do NOT write explanations or meta-commentary about why you are staying silent."
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
	userMsg := buildUserMessage(ctx, a.httpClient, msg)
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
				a.logger.Error("send message", "error", err)
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
				a.logger.Error("send message", "error", err)
			}
			return
		}
	}

	if assistantContent != "" && looksLikeToolCall(assistantContent, reg.Definitions()) {
		a.logger.Warn("suppressed tool-call syntax leaked into content", "content", assistantContent)
		assistantContent = ""
		if !reg.Replied && mode != "smart" {
			if err := sendFn("I'm not sure how to respond. Please try again."); err != nil {
				a.logger.Error("send message", "error", err)
			}
		}
	}

	// In smart mode the model should only communicate via reply/react tools.
	// Suppress any leftover plain-text content that was not sent through a tool.
	if mode == "smart" && assistantContent != "" && !reg.Replied {
		a.logger.Debug("suppressed smart-mode plain-text non-reply", "content", assistantContent)
		assistantContent = ""
	}

	// Suppress stage-direction non-replies like "(staying silent)" in all modes.
	if assistantContent != "" && !reg.Replied && isStageDirection(assistantContent) {
		a.logger.Debug("suppressed stage-direction non-reply", "content", assistantContent)
		assistantContent = ""
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
				a.logger.Error("send message", "error", err)
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
	a.turnCount++
	cfg2 := a.cfgStore.Get()
	if interval := cfg2.Agent.MemoryExtractionInterval; interval > 0 && a.turnCount%interval == 0 {
		a.runMemoryExtraction(a.history)
	}
}

// runMemoryExtraction launches a background goroutine that reviews recent history
// and saves any important information the main turn may have missed.
func (a *ChannelAgent) runMemoryExtraction(history []llm.Message) {
	snapshot := make([]llm.Message, len(history))
	copy(snapshot, history)

	reg := tools.NewMemoryOnlyRegistry(a.resources.Memory, a.serverID)
	logger := a.logger

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		msgs := buildMessages(extractionPrompt, snapshot)
		cfg := a.cfgStore.Get()

		for iter := 0; iter < cfg.Agent.MaxToolIterations; iter++ {
			choice, err := a.llm.Chat(ctx, msgs, reg.Definitions())
			if err != nil {
				logger.Warn("memory extraction llm error", "error", err)
				return
			}
			if len(choice.Message.ToolCalls) == 0 {
				return
			}
			msgs = append(msgs, choice.Message)
			for _, tc := range choice.Message.ToolCalls {
				result, err := reg.Dispatch(ctx, tc.Function.Name, []byte(tc.Function.Arguments))
				if err != nil {
					logger.Warn("memory extraction dispatch error", "tool", tc.Function.Name, "error", err)
					result = fmt.Sprintf("Error: %s", err)
				}
				msgs = append(msgs, llm.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
				})
			}
		}
		logger.Warn("memory extraction hit max iterations")
	}()
}

// startTyping sends a typing indicator immediately and refreshes every 8 seconds
// until the returned cancel function is called.
func (a *ChannelAgent) startTyping(ctx context.Context) context.CancelFunc {
	typingCtx, cancel := context.WithCancel(ctx)
	go func() {
		if err := a.resources.Session.ChannelTyping(a.channelID); err != nil {
			a.logger.Warn("channel typing error", "error", err, "channel_id", a.channelID)
		}
		ticker := time.NewTicker(8 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := a.resources.Session.ChannelTyping(a.channelID); err != nil {
					a.logger.Debug("channel typing refresh error", "error", err, "channel_id", a.channelID)
				}
			case <-typingCtx.Done():
				return
			}
		}
	}()
	return cancel
}

// looksLikeToolCall returns true when any line of s looks like a text-based
// tool-call invocation (e.g. memory_save(content="...", ...)) rather than prose.
// Checking per-line handles models that emit a preamble sentence before the call.
func looksLikeToolCall(s string, defs []llm.ToolDefinition) bool {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, d := range defs {
			if strings.HasPrefix(trimmed, d.Function.Name+"(") {
				return true
			}
		}
	}
	return false
}

// isStageDirection reports whether s is a parenthesized stage direction
// like "(staying silent)" that the model sometimes emits as a non-reply.
// Multi-line strings are never stage directions.
func isStageDirection(s string) bool {
	s = strings.TrimSpace(s)
	return !strings.Contains(s, "\n") && strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")
}

// buildMessages constructs the message slice for the LLM with system prompt prepended.
func buildMessages(systemPrompt string, history []llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})
	msgs = append(msgs, history...)
	return msgs
}
