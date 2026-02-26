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
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/memory"
	"github.com/tomasmach/vespra/soul"
	"github.com/tomasmach/vespra/tools"
)

// toolCallRecord is used to log tool calls made during a conversation turn.
type toolCallRecord struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

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

// sanitizeHistory drops leading messages that are not role "user", preventing
// orphaned tool-result or partial tool-call messages from corrupting history
// after a HistoryLimit trim.
func sanitizeHistory(msgs []llm.Message) []llm.Message {
	dropped := 0
	for len(msgs) > 0 && msgs[0].Role != "user" {
		msgs = msgs[1:]
		dropped++
	}
	if dropped > 0 {
		slog.Warn("dropped leading non-user messages after history trim", "count", dropped)
	}
	return msgs
}

// ChannelAgent is a per-channel conversation goroutine.
type ChannelAgent struct {
	channelID string
	serverID  string

	cfgStore   *config.Store
	llm        *llm.Client
	httpClient *http.Client
	resources  *AgentResources
	logger     *slog.Logger

	soulText          string
	history           []llm.Message  // capped to cfg.Agent.HistoryLimit
	turnCount         int            // incremented each completed turn; triggers background extraction
	lastActive        atomic.Int64   // UnixNano; written by agent goroutine, read by Status()
	extractionRunning atomic.Bool    // prevents concurrent extraction goroutines from piling up
	extractionWg      sync.WaitGroup // tracks in-flight memory extraction goroutines
	searchRunning     atomic.Bool    // prevents concurrent web searches
	searchWg          sync.WaitGroup // tracks in-flight web search goroutines

	ctx        context.Context    // agent's own context; set at the start of run()
	msgCh      chan *discordgo.MessageCreate // buffered 100
	internalCh chan string                   // buffered; receives system messages (e.g., web search results)
	cancel     context.CancelFunc           // cancels this agent's context
}

// resolveMentions replaces raw Discord mention syntax (<@ID> and <@!ID>) with
// readable display names using the resolved User objects Discord provides.
func resolveMentions(content string, mentions []*discordgo.User) string {
	for _, u := range mentions {
		name := u.GlobalName
		if name == "" {
			name = u.Username
		}
		content = strings.ReplaceAll(content, "<@"+u.ID+">", "@"+name)
		content = strings.ReplaceAll(content, "<@!"+u.ID+">", "@"+name)
	}
	return content
}

// hasImageAttachments reports whether the message has at least one image attachment.
func hasImageAttachments(m *discordgo.Message) bool {
	for _, a := range m.Attachments {
		if strings.HasPrefix(a.ContentType, "image/") {
			return true
		}
	}
	return false
}

// hasVideoAttachments reports whether the message has at least one video attachment.
func hasVideoAttachments(m *discordgo.Message) bool {
	for _, a := range m.Attachments {
		if strings.HasPrefix(a.ContentType, "video/") {
			return true
		}
	}
	return false
}

// hasGifEmbeds reports whether the message has at least one gifv embed with a thumbnail.
func hasGifEmbeds(m *discordgo.Message) bool {
	return len(gifEmbedURLs(m)) > 0
}

// gifEmbedURLs returns the thumbnail URLs of all gifv embeds in the message,
// preferring ProxyURL for Discord CDN stability.
func gifEmbedURLs(m *discordgo.Message) []string {
	var urls []string
	for _, e := range m.Embeds {
		if e.Type != discordgo.EmbedTypeGifv || e.Thumbnail == nil {
			continue
		}
		thumbnailURL := e.Thumbnail.ProxyURL
		if thumbnailURL == "" {
			thumbnailURL = e.Thumbnail.URL
		}
		if thumbnailURL != "" {
			urls = append(urls, thumbnailURL)
		}
	}
	return urls
}

// formatMessageContent replaces raw Discord mention syntax (<@ID> and <@!ID>)
// for the bot with a human-readable "@botName" so the LLM sees natural text.
func formatMessageContent(content, botID, botName string) string {
	content = strings.ReplaceAll(content, "<@"+botID+">", "@"+botName)
	content = strings.ReplaceAll(content, "<@!"+botID+">", "@"+botName)
	return content
}

// historyUserContent formats the text content for a user message in history,
// annotating reply-to context when the message is a Discord reply and
// sanitizing bot mentions into readable form.
func historyUserContent(m *discordgo.Message, botID, botName string) string {
	content := resolveMentions(formatMessageContent(m.Content, botID, botName), m.Mentions)
	if m.ReferencedMessage != nil && m.ReferencedMessage.Author != nil {
		refContent := resolveMentions(formatMessageContent(m.ReferencedMessage.Content, botID, botName), m.ReferencedMessage.Mentions)
		if len(refContent) > 200 {
			refContent = refContent[:200] + "..."
		}
		if refContent == "" {
			var labels []string
			if hasImageAttachments(m.ReferencedMessage) {
				labels = append(labels, "[image]")
			}
			if hasVideoAttachments(m.ReferencedMessage) {
				labels = append(labels, "[video]")
			}
			if hasGifEmbeds(m.ReferencedMessage) {
				labels = append(labels, "[gif]")
			}
			if len(labels) > 0 {
				refContent = strings.Join(labels, ", ")
			}
		}
		return fmt.Sprintf("%s (replying to %s: %q): %s",
			m.Author.Username,
			m.ReferencedMessage.Author.Username,
			refContent,
			content)
	}
	return fmt.Sprintf("%s: %s", m.Author.Username, content)
}

const maxVideoBytes = 50 * 1024 * 1024 // 50 MB

// classifyAttachments partitions attachments into images and videos,
// skipping videos that exceed maxVideoBytes.
func classifyAttachments(attachments []*discordgo.MessageAttachment) (images, videos []*discordgo.MessageAttachment) {
	for _, a := range attachments {
		if strings.HasPrefix(a.ContentType, "image/") {
			images = append(images, a)
		} else if strings.HasPrefix(a.ContentType, "video/") {
			if a.Size > maxVideoBytes {
				slog.Warn("skipping oversized video attachment", "size", a.Size, "url", a.URL)
				continue
			}
			videos = append(videos, a)
		}
	}
	return images, videos
}

// buildUserMessage converts a Discord message into an llm.Message, downloading
// any image, video attachments, or GIF embed thumbnails as base64 data URLs for vision content parts.
// Discord CDN URLs require authentication, so media must be fetched server-side.
func buildUserMessage(ctx context.Context, httpClient *http.Client, msg *discordgo.MessageCreate, botID, botName string) llm.Message {
	text := historyUserContent(msg.Message, botID, botName)

	images, videos := classifyAttachments(msg.Attachments)
	if msg.ReferencedMessage != nil {
		refImages, refVideos := classifyAttachments(msg.ReferencedMessage.Attachments)
		images = append(images, refImages...)
		videos = append(videos, refVideos...)
	}

	gifURLs := gifEmbedURLs(msg.Message)
	if msg.ReferencedMessage != nil {
		gifURLs = append(gifURLs, gifEmbedURLs(msg.ReferencedMessage)...)
	}

	if len(images) == 0 && len(videos) == 0 && len(gifURLs) == 0 {
		return llm.Message{Role: "user", Content: text}
	}

	parts := make([]llm.ContentPart, 0, 1+len(images)+len(videos)+len(gifURLs))
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
	for _, a := range videos {
		dataURL, err := downloadImageAsDataURL(ctx, httpClient, a)
		if err != nil {
			slog.Warn("failed to download video attachment, skipping", "error", err, "url", a.URL)
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type:     "video_url",
			VideoURL: &llm.VideoURL{URL: dataURL},
		})
	}
	for _, u := range gifURLs {
		dataURL, err := downloadURLAsDataURL(ctx, httpClient, u, "")
		if err != nil {
			slog.Warn("failed to download gif embed thumbnail, skipping", "error", err, "url", u)
			continue
		}
		parts = append(parts, llm.ContentPart{
			Type:     "image_url",
			ImageURL: &llm.ImageURL{URL: dataURL},
		})
	}
	if len(parts) == 1 {
		// all media downloads failed; fall back to plain text
		return llm.Message{Role: "user", Content: text}
	}
	return llm.Message{Role: "user", ContentParts: parts}
}

// downloadURLAsDataURL fetches url and returns it encoded as a base64 data URL.
// If contentType is empty, the response Content-Type header is used, defaulting to "image/jpeg".
func downloadURLAsDataURL(ctx context.Context, client *http.Client, url, contentType string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch url: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if contentType == "" {
		contentType, _, _ = strings.Cut(resp.Header.Get("Content-Type"), ";")
		contentType = strings.TrimSpace(contentType)
		if contentType == "" {
			contentType = "image/jpeg"
		}
	}
	return fmt.Sprintf("data:%s;base64,%s", contentType, base64.StdEncoding.EncodeToString(data)), nil
}

// downloadImageAsDataURL fetches an image attachment and returns it encoded as a base64 data URL.
func downloadImageAsDataURL(ctx context.Context, client *http.Client, a *discordgo.MessageAttachment) (string, error) {
	ct := a.ContentType
	if ct == "" {
		ct = "image/jpeg"
	}
	return downloadURLAsDataURL(ctx, client, a.URL, ct)
}

func newChannelAgent(channelID, serverID string, cfgStore *config.Store, llmClient *llm.Client, resources *AgentResources) *ChannelAgent {
	return &ChannelAgent{
		channelID:  channelID,
		serverID:   serverID,
		cfgStore:   cfgStore,
		llm:        llmClient,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		resources:  resources,
		soulText:   soul.Load(cfgStore.Get(), serverID),
		msgCh:      make(chan *discordgo.MessageCreate, 100),
		internalCh: make(chan string, 10),
		logger:     slog.With("server_id", serverID, "channel_id", channelID),
	}
}

func (a *ChannelAgent) run(ctx context.Context) {
	a.ctx = ctx
	// Wait for all in-flight background goroutines before returning,
	// so that SQLite connections are not closed while they are still running.
	defer a.extractionWg.Wait()
	defer a.searchWg.Wait()

	idleTimeout := time.Duration(a.cfgStore.Get().Agent.IdleTimeoutMinutes) * time.Minute
	idleTimer := time.NewTimer(idleTimeout)
	defer idleTimer.Stop()

	var (
		coalesceBuffer []*discordgo.MessageCreate
		debounceTimer  *time.Timer
		deadlineTimer  *time.Timer
	)

	stopTimer := func(t *time.Timer) {
		if t == nil {
			return
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
	}

	resetIdleTimer := func() {
		stopTimer(idleTimer)
		idleTimer.Reset(idleTimeout)
	}

	flush := func(fctx context.Context) {
		if len(coalesceBuffer) == 0 {
			return
		}
		msgs := coalesceBuffer
		coalesceBuffer = nil
		stopTimer(debounceTimer)
		debounceTimer = nil
		stopTimer(deadlineTimer)
		deadlineTimer = nil
		a.handleMessages(fctx, msgs)
	}

	// timerC returns the timer channel or nil. A nil channel blocks forever
	// in a select, which is the desired "disabled" behavior.
	timerC := func(t *time.Timer) <-chan time.Time {
		if t == nil {
			return nil
		}
		return t.C
	}

	for {
		select {
		case msg := <-a.msgCh:
			resetIdleTimer()

			cfg := a.cfgStore.Get()
			if cfg.Agent.CoalesceDisabled {
				a.handleMessage(ctx, msg)
			} else {
				coalesceBuffer = append(coalesceBuffer, msg)
				stopTimer(debounceTimer)
				debounceTimer = time.NewTimer(time.Duration(cfg.Agent.CoalesceDebounceMs) * time.Millisecond)
				if deadlineTimer == nil {
					deadlineTimer = time.NewTimer(time.Duration(cfg.Agent.CoalesceMaxWaitMs) * time.Millisecond)
				}
			}

		case intMsg := <-a.internalCh:
			flush(ctx)
			resetIdleTimer()
			a.handleInternalMessage(ctx, intMsg)

		case <-timerC(debounceTimer):
			flush(ctx)
			resetIdleTimer()

		case <-timerC(deadlineTimer):
			flush(ctx)
			resetIdleTimer()

		case <-idleTimer.C:
			drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			flush(drainCtx)
			a.logger.Info("channel agent idle timeout")
			return

		case <-ctx.Done():
			drainCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			flush(drainCtx)
			n := len(a.msgCh)
			for i := 0; i < n; i++ {
				msg := <-a.msgCh
				a.handleMessage(drainCtx, msg)
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
	botName := a.resources.Session.State.User.Username
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
			history = append(history, llm.Message{Role: "user", Content: historyUserContent(m, botID, botName)})
		}
	}
	return history
}

// isAddressedToBot reports whether a Discord message is directly addressed to
// the bot via DM, @mention, reply, or plain-text name mention.
func isAddressedToBot(m *discordgo.MessageCreate, botID, botName string) bool {
	if m.GuildID == "" {
		return true // DMs are always addressed
	}
	if strings.Contains(m.Content, "<@"+botID+">") || strings.Contains(m.Content, "<@!"+botID+">") {
		return true
	}
	if m.MessageReference != nil &&
		m.ReferencedMessage != nil &&
		m.ReferencedMessage.Author != nil &&
		m.ReferencedMessage.Author.ID == botID {
		return true
	}
	if botName != "" && strings.Contains(strings.ToLower(m.Content), strings.ToLower(botName)) {
		return true
	}
	return false
}

// turnParams holds the inputs needed by processTurn, allowing handleMessage and
// handleMessages to share the tool-call loop and post-processing logic.
type turnParams struct {
	mode         string
	systemPrompt string
	sendFn       func(string) error
	reg          *tools.Registry
	llmMsgs      []llm.Message
	userMsgText  string // human-readable user input for conversation logging
	internal     bool   // true for system-generated turns (e.g., web search results); skips LogConversation
}

func (a *ChannelAgent) handleMessage(ctx context.Context, msg *discordgo.MessageCreate) {
	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()
	mode := cfg.ResolveResponseMode(a.serverID, msg.ChannelID)
	botID := a.resources.Session.State.User.ID
	botName := a.resources.Session.State.User.Username
	addressed := isAddressedToBot(msg, botID, botName)

	switch mode {
	case "none":
		return
	case "mention":
		if !addressed {
			return
		}
	}

	stopTyping := func() {}
	if mode != "smart" || addressed {
		stopTyping = a.startTyping(ctx)
	}
	defer stopTyping()

	if len(a.history) == 0 {
		a.history = a.backfillHistory(ctx, msg.ID)
		if len(a.history) > cfg.Agent.HistoryLimit {
			a.history = a.history[len(a.history)-cfg.Agent.HistoryLimit:]
		}
		a.history = sanitizeHistory(a.history)
	}

	memories, err := a.resources.Memory.Recall(ctx, msg.Content, a.serverID, 10)
	if err != nil {
		a.logger.Warn("memory recall error", "error", err)
	}
	systemPrompt := a.buildSystemPrompt(cfg, mode, msg.ChannelID, memories, botName)

	sendFn := func(content string) error {
		_, err := a.resources.Session.ChannelMessageSend(msg.ChannelID, content)
		return err
	}
	reactFn := func(emoji string) error {
		return a.resources.Session.MessageReactionAdd(msg.ChannelID, msg.ID, emoji)
	}
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, a.webSearchDeps())

	userMsg := buildUserMessage(ctx, a.httpClient, msg, botID, botName)
	llmMsgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(llmMsgs, a.history)
	llmMsgs = append(llmMsgs, userMsg)

	a.processTurn(ctx, cfg, turnParams{
		mode:         mode,
		systemPrompt: systemPrompt,
		sendFn:       sendFn,
		reg:          reg,
		llmMsgs:      llmMsgs,
		userMsgText:  historyUserContent(msg.Message, botID, botName),
	})
}

// buildCombinedContent builds the combined user content string for a batch of coalesced messages.
func buildCombinedContent(msgs []*discordgo.MessageCreate, botID, botName string) string {
	firstTime := msgs[0].Timestamp
	lines := make([]string, 0, len(msgs)+2)
	lines = append(lines, fmt.Sprintf("[%d messages arrived rapidly in quick succession]", len(msgs)))
	lines = append(lines, "")
	for _, m := range msgs {
		line := historyUserContent(m.Message, botID, botName)
		gap := m.Timestamp.Sub(firstTime)
		if gap >= time.Second {
			secs := int(gap.Seconds())
			line += fmt.Sprintf(" (+%ds)", secs)
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (a *ChannelAgent) handleMessages(ctx context.Context, msgs []*discordgo.MessageCreate) {
	if len(msgs) == 1 {
		a.handleMessage(ctx, msgs[0])
		return
	}

	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()
	botID := a.resources.Session.State.User.ID
	botName := a.resources.Session.State.User.Username
	lastMsg := msgs[len(msgs)-1]
	mode := cfg.ResolveResponseMode(a.serverID, lastMsg.ChannelID)

	var anyAddressed bool
	for _, m := range msgs {
		if isAddressedToBot(m, botID, botName) {
			anyAddressed = true
			break
		}
	}

	switch mode {
	case "none":
		return
	case "mention":
		if !anyAddressed {
			return
		}
	}

	stopTyping := func() {}
	if mode != "smart" || anyAddressed {
		stopTyping = a.startTyping(ctx)
	}
	defer stopTyping()

	if len(a.history) == 0 {
		a.history = a.backfillHistory(ctx, msgs[0].ID)
		if len(a.history) > cfg.Agent.HistoryLimit {
			a.history = a.history[len(a.history)-cfg.Agent.HistoryLimit:]
		}
	}

	recallParts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		recallParts = append(recallParts, m.Content)
	}
	recallQuery := strings.Join(recallParts, " ")

	memories, err := a.resources.Memory.Recall(ctx, recallQuery, a.serverID, 10)
	if err != nil {
		a.logger.Warn("memory recall error", "error", err)
	}

	systemPrompt := a.buildSystemPrompt(cfg, mode, lastMsg.ChannelID, memories, botName)

	sendFn := func(content string) error {
		_, err := a.resources.Session.ChannelMessageSend(lastMsg.ChannelID, content)
		return err
	}
	reactFn := func(emoji string) error {
		return a.resources.Session.MessageReactionAdd(lastMsg.ChannelID, lastMsg.ID, emoji)
	}
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, a.webSearchDeps())

	combinedUserMsg := a.buildCombinedUserMessage(ctx, msgs, botID, botName)

	llmMsgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(llmMsgs, a.history)
	llmMsgs = append(llmMsgs, combinedUserMsg)

	userLogLines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		userLogLines = append(userLogLines, historyUserContent(m.Message, botID, botName))
	}

	a.processTurn(ctx, cfg, turnParams{
		mode:         mode,
		systemPrompt: systemPrompt,
		sendFn:       sendFn,
		reg:          reg,
		llmMsgs:      llmMsgs,
		userMsgText:  strings.Join(userLogLines, "\n"),
	})
}

// handleInternalMessage processes a system-generated message (e.g., web search results)
// through the normal agent turn loop. Web search is NOT registered to prevent loops.
// The search result turn is not persisted in history after processTurn returns.
func (a *ChannelAgent) handleInternalMessage(ctx context.Context, content string) {
	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()
	mode := cfg.ResolveResponseMode(a.serverID, a.channelID)
	stopTyping := a.startTyping(ctx)
	defer stopTyping()

	// Build a focused system prompt — no soul/personality/memories to avoid
	// the LLM re-generating its earlier conversational response.
	var sb strings.Builder
	botName := a.resources.Session.State.User.Username
	if botName != "" {
		fmt.Fprintf(&sb, "Your Discord username is %s.\n\n", botName)
	}
	sb.WriteString("You are receiving web search results. If the results contain URLs with specific data you need, you may call web_fetch to read a page for more precise information. Then summarize the findings for the user. Do NOT repeat or rephrase anything you said earlier in the conversation. Focus on presenting the new information clearly, with relevant sources and links.")
	if lang := cfg.ResolveLanguage(a.serverID, a.channelID); lang != "" {
		fmt.Fprintf(&sb, "\n\nAlways respond in %s.", lang)
	}

	sendFn := func(text string) error {
		_, err := a.resources.Session.ChannelMessageSend(a.channelID, text)
		return err
	}
	reactFn := func(emoji string) error { return nil }
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, sendFn, reactFn, nil)

	userMsg := llm.Message{Role: "user", Content: content}
	llmMsgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(llmMsgs, a.history)
	llmMsgs = append(llmMsgs, userMsg)

	// Save history length before the turn so we can restore it after. The search
	// result is a transient system turn and must not pollute the persistent history.
	historyLen := len(a.history)
	a.processTurn(ctx, cfg, turnParams{
		mode:         mode,
		systemPrompt: sb.String(),
		sendFn:       sendFn,
		reg:          reg,
		llmMsgs:      llmMsgs,
		userMsgText:  content,
		internal:     true,
	})
	// Trim back to the pre-turn history length, preserving any assistant reply that
	// processTurn appended, but dropping the injected system message entry.
	if len(a.history) > historyLen {
		a.history = a.history[:historyLen]
	}
}

// buildSystemPrompt assembles the system prompt from the soul text, memories,
// language override, and response mode.
func (a *ChannelAgent) buildSystemPrompt(cfg *config.Config, mode, channelID string, memories []memory.MemoryRow, botName string) string {
	var sb strings.Builder
	if botName != "" {
		fmt.Fprintf(&sb, "Your Discord username is %s.\n\n", botName)
	}
	fmt.Fprintf(&sb, "Today's date is %s.\n\n", time.Now().Format("Monday, January 2, 2006"))
	sb.WriteString(a.soulText)
	if len(memories) > 0 {
		sb.WriteString("\n\n## Relevant Memories\n")
		for _, m := range memories {
			fmt.Fprintf(&sb, "- [%s] %s\n", m.ID, m.Content)
		}
	}
	if lang := cfg.ResolveLanguage(a.serverID, channelID); lang != "" {
		fmt.Fprintf(&sb, "\n\nAlways respond in %s.", lang)
	}
	if mode == "smart" {
		sb.WriteString("\n\nYou are in smart mode. Only respond via the `reply` or `react` tools when the message genuinely warrants a response. If you choose not to respond, produce no output at all — do NOT write explanations or meta-commentary about why you are staying silent.")
	}
	return sb.String()
}

// buildCombinedUserMessage builds an LLM user message from a batch of coalesced
// Discord messages, collecting text, image, and video attachments.
func (a *ChannelAgent) buildCombinedUserMessage(ctx context.Context, msgs []*discordgo.MessageCreate, botID, botName string) llm.Message {
	combinedContent := buildCombinedContent(msgs, botID, botName)

	var mediaParts []llm.ContentPart
	for _, m := range msgs {
		for _, att := range m.Attachments {
			if strings.HasPrefix(att.ContentType, "image/") {
				dataURL, err := downloadImageAsDataURL(ctx, a.httpClient, att)
				if err != nil {
					a.logger.Warn("failed to download image attachment, skipping", "error", err, "url", att.URL)
					continue
				}
				mediaParts = append(mediaParts, llm.ContentPart{
					Type:     "image_url",
					ImageURL: &llm.ImageURL{URL: dataURL},
				})
			} else if strings.HasPrefix(att.ContentType, "video/") {
				if att.Size > maxVideoBytes {
					a.logger.Warn("skipping oversized video attachment", "size", att.Size, "url", att.URL)
					continue
				}
				dataURL, err := downloadImageAsDataURL(ctx, a.httpClient, att)
				if err != nil {
					a.logger.Warn("failed to download video attachment, skipping", "error", err, "url", att.URL)
					continue
				}
				mediaParts = append(mediaParts, llm.ContentPart{
					Type:     "video_url",
					VideoURL: &llm.VideoURL{URL: dataURL},
				})
			}
		}
		for _, gifURL := range gifEmbedURLs(m.Message) {
			dataURL, err := downloadURLAsDataURL(ctx, a.httpClient, gifURL, "")
			if err != nil {
				a.logger.Warn("failed to download gif embed thumbnail, skipping", "error", err, "url", gifURL)
				continue
			}
			mediaParts = append(mediaParts, llm.ContentPart{
				Type:     "image_url",
				ImageURL: &llm.ImageURL{URL: dataURL},
			})
		}
	}

	if len(mediaParts) == 0 {
		return llm.Message{Role: "user", Content: combinedContent}
	}
	parts := make([]llm.ContentPart, 0, 1+len(mediaParts))
	parts = append(parts, llm.ContentPart{Type: "text", Text: combinedContent})
	parts = append(parts, mediaParts...)
	return llm.Message{Role: "user", ContentParts: parts}
}

// chatOptions returns per-agent ChatOptions when the agent has a provider or
// model override configured, or nil to use global defaults.
func (a *ChannelAgent) chatOptions() *llm.ChatOptions {
	cfg := a.resources.Config
	if cfg == nil || (cfg.Provider == "" && cfg.Model == "") {
		return nil
	}
	return &llm.ChatOptions{Provider: cfg.Provider, Model: cfg.Model}
}

// webSearchDeps returns the dependency bundle for the async web search tool,
// or nil if web search is not configured (no GLM key).
func (a *ChannelAgent) webSearchDeps() *tools.WebSearchDeps {
	cfg := a.cfgStore.Get()
	if cfg.LLM.GLMKey == "" {
		slog.Warn("web_search tool disabled: llm.glm_key is not configured", "server_id", a.serverID)
		return nil
	}

	// Resolve the model: use the agent's configured model when available.
	model := cfg.LLM.Model
	if a.resources.Config != nil && a.resources.Config.Model != "" {
		model = a.resources.Config.Model
	}

	return &tools.WebSearchDeps{
		DeliverResult: func(result string) {
			select {
			case a.internalCh <- result:
			default:
				a.logger.Warn("internal channel full, dropping web search result")
			}
		},
		LLM:            a.llm,
		Model:          model,
		Ctx:            a.ctx,
		SearchWg:       &a.searchWg,
		SearchRunning:  &a.searchRunning,
		TimeoutSeconds: cfg.Tools.WebTimeoutSeconds,
	}
}

// processTurn runs the tool-call loop, applies content suppression, logs the
// conversation, sends the reply, and updates history. Both handleMessage and
// handleMessages delegate here after preparing their inputs.
func (a *ChannelAgent) processTurn(ctx context.Context, cfg *config.Config, tp turnParams) {
	chatOpts := a.chatOptions()

	// Capture visionResponse before the tool-call loop so that tool-call messages
	// appended to tp.llmMsgs don't corrupt the check.
	visionResponse := len(tp.llmMsgs) > 0 && len(tp.llmMsgs[len(tp.llmMsgs)-1].ContentParts) > 0

	var toolCalls []toolCallRecord
	var assistantContent string
	for iter := 0; ; iter++ {
		if iter >= cfg.Agent.MaxToolIterations {
			if err := tp.sendFn("I got stuck in a loop. Please try again."); err != nil {
				a.logger.Error("send message", "error", err)
			}
			return
		}

		choice, err := a.llm.Chat(ctx, buildMessages(tp.systemPrompt, tp.llmMsgs), tp.reg.Definitions(), chatOpts)
		if err != nil {
			effectiveModel := cfg.LLM.Model
			if chatOpts != nil && chatOpts.Model != "" {
				effectiveModel = chatOpts.Model
			}
			a.logger.Error("llm chat error", "error", err, "model", effectiveModel)
			if err := tp.sendFn("I encountered an error. Please try again."); err != nil {
				a.logger.Error("send message", "error", err)
			}
			return
		}

		if len(choice.Message.ToolCalls) == 0 {
			assistantContent = choice.Message.Content
			break
		}

		tp.llmMsgs = append(tp.llmMsgs, choice.Message)
		for _, tc := range choice.Message.ToolCalls {
			a.logger.Debug("tool call", "tool", tc.Function.Name)
			result, err := tp.reg.Dispatch(ctx, tc.Function.Name, []byte(tc.Function.Arguments))
			if err != nil {
				a.logger.Warn("tool dispatch error", "tool", tc.Function.Name, "error", err)
				result = fmt.Sprintf("Error: %s", err)
			}
			toolCalls = append(toolCalls, toolCallRecord{Name: tc.Function.Name, Result: result})
			tp.llmMsgs = append(tp.llmMsgs, llm.Message{
				Role:       "tool",
				Content:    result,
				ToolCallID: tc.ID,
			})
		}

		// After executing tool calls, if the reply tool was used, record what was said
		// so subsequent LLM calls have context of what the assistant replied.
		// Reset ReplyText after appending so this only fires once (Replied is a sticky latch).
		if tp.reg.Replied && tp.reg.ReplyText != "" {
			tp.llmMsgs = append(tp.llmMsgs, llm.Message{Role: "assistant", Content: tp.reg.ReplyText})
			tp.reg.ReplyText = ""
		}
	}

	if assistantContent != "" && looksLikeToolCall(assistantContent, tp.reg.Definitions()) {
		a.logger.Warn("suppressed tool-call syntax leaked into content", "content", assistantContent)
		assistantContent = ""
		if !tp.reg.Replied && tp.mode != "smart" {
			if err := tp.sendFn("I'm not sure how to respond. Please try again."); err != nil {
				a.logger.Error("send message", "error", err)
			}
		}
	}

	// In smart mode the model should only communicate via reply/react tools.
	// Suppress any leftover plain-text content that was not sent through a tool.
	// Exception: vision model responses are always plain text (tools are omitted for GLM vision).
	if tp.mode == "smart" && assistantContent != "" && !tp.reg.Replied && !visionResponse {
		a.logger.Debug("suppressed smart-mode plain-text non-reply", "content", assistantContent)
		assistantContent = ""
	}

	// Suppress stage-direction non-replies like "(staying silent)" in all modes.
	if assistantContent != "" && !tp.reg.Replied && isStageDirection(assistantContent) {
		a.logger.Debug("suppressed stage-direction non-reply", "content", assistantContent)
		assistantContent = ""
	}

	// Log conversation on success -- either plain-text reply or reply-tool response.
	// Internal turns (e.g., web search result delivery) are skipped to avoid polluting logs.
	if !tp.internal && (assistantContent != "" || tp.reg.Replied) {
		var toolCallsJSON string
		if len(toolCalls) > 0 {
			if b, err := json.Marshal(toolCalls); err == nil {
				toolCallsJSON = string(b)
			}
		}
		responseText := assistantContent
		if responseText == "" && tp.reg.Replied {
			responseText = tp.reg.ReplyText
		}
		if err := a.resources.Memory.LogConversation(ctx, a.channelID, tp.userMsgText, toolCallsJSON, responseText); err != nil {
			a.logger.Warn("log conversation error", "error", err)
		}
	}

	if assistantContent != "" && !tp.reg.Replied {
		parts := tools.SplitMessage(assistantContent, 2000)
		for _, p := range parts {
			if err := tp.sendFn(p); err != nil {
				a.logger.Error("send message", "error", err)
			}
		}
	}

	if assistantContent != "" {
		tp.llmMsgs = append(tp.llmMsgs, llm.Message{Role: "assistant", Content: assistantContent})
	}
	if len(tp.llmMsgs) > cfg.Agent.HistoryLimit {
		tp.llmMsgs = tp.llmMsgs[len(tp.llmMsgs)-cfg.Agent.HistoryLimit:]
	}
	tp.llmMsgs = sanitizeHistory(tp.llmMsgs)
	a.history = tp.llmMsgs
	if assistantContent != "" || tp.reg.Replied {
		a.turnCount++
		if interval := cfg.Agent.MemoryExtractionInterval; interval > 0 && a.turnCount%interval == 0 {
			a.runMemoryExtraction(ctx, a.history)
		}
	}
}

// runMemoryExtraction launches a background goroutine that reviews recent history
// and saves any important information the main turn may have missed.
func (a *ChannelAgent) runMemoryExtraction(ctx context.Context, history []llm.Message) {
	if !a.extractionRunning.CompareAndSwap(false, true) {
		return // extraction already in progress
	}

	snapshot := stripImageParts(history)
	reg := tools.NewMemoryOnlyRegistry(a.resources.Memory, a.serverID)

	a.extractionWg.Add(1)
	go func() {
		defer a.extractionWg.Done()
		defer a.extractionRunning.Store(false)

		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()

		msgs := buildMessages(extractionPrompt, snapshot)
		cfg := a.cfgStore.Get()

		for iter := 0; iter < cfg.Agent.MaxToolIterations; iter++ {
			choice, err := a.llm.Chat(ctx, msgs, reg.Definitions(), a.chatOptions())
			if err != nil {
				a.logger.Warn("memory extraction llm error", "error", err)
				return
			}
			if len(choice.Message.ToolCalls) == 0 {
				return
			}
			msgs = append(msgs, choice.Message)
			for _, tc := range choice.Message.ToolCalls {
				result, err := reg.Dispatch(ctx, tc.Function.Name, []byte(tc.Function.Arguments))
				if err != nil {
					a.logger.Warn("memory extraction dispatch error", "tool", tc.Function.Name, "error", err)
					result = fmt.Sprintf("Error: %s", err)
				}
				msgs = append(msgs, llm.Message{
					Role:       "tool",
					Content:    result,
					ToolCallID: tc.ID,
				})
			}
		}
		a.logger.Warn("memory extraction hit max iterations")
	}()
}

// stripImageParts returns a copy of history with ContentParts replaced by their
// text-only Content equivalent, suitable for the extraction LLM which has no use
// for image or video data.
func stripImageParts(history []llm.Message) []llm.Message {
	snapshot := make([]llm.Message, len(history))
	copy(snapshot, history)
	for i := range snapshot {
		if len(snapshot[i].ContentParts) == 0 {
			continue
		}
		for _, p := range snapshot[i].ContentParts {
			if p.Type == "text" {
				snapshot[i].Content = p.Text
				break
			}
		}
		snapshot[i].ContentParts = nil
	}
	return snapshot
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

// isStageDirection reports whether s is a stage direction like "(staying silent)"
// or "[MLČÍM]" that the model sometimes emits as a non-reply.
// Multi-line strings are never stage directions.
func isStageDirection(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "\n") {
		return false
	}
	return (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"))
}

// buildMessages constructs the message slice for the LLM with system prompt prepended.
func buildMessages(systemPrompt string, history []llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})
	msgs = append(msgs, history...)
	return msgs
}
