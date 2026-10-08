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
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"golang.org/x/text/unicode/norm"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/memory"
	"github.com/tomasmach/vespra/soul"
	"github.com/tomasmach/vespra/tools"
)

// maxMediaDescriptionRunes is the maximum length of a media description before
// truncation. It leaves room for text transcribed from screenshots.
const maxMediaDescriptionRunes = 3000

// mediaDescriptionLabel prefixes the vision model's description in the user
// message. The wording tells the main model that it saw the media itself.
const mediaDescriptionLabel = "[What you see in the attached media: "

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
	// Merge consecutive plain-text assistant messages (no tool calls) into one.
	// Back-to-back assistant entries confuse some models and can cause empty responses.
	out := make([]llm.Message, 0, len(msgs))
	for _, m := range msgs {
		last := len(out) - 1
		if m.Role == "assistant" && len(m.ToolCalls) == 0 &&
			last >= 0 && out[last].Role == "assistant" && len(out[last].ToolCalls) == 0 {
			if out[last].Content != "" && m.Content != "" {
				out[last].Content += "\n\n" + m.Content
			} else if m.Content != "" {
				out[last].Content = m.Content
			}
			continue
		}
		out = append(out, m)
	}
	return out
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
	imageRunning      atomic.Bool    // prevents concurrent image generations
	imageWg           sync.WaitGroup // tracks in-flight image generation goroutines
	sendTimestamps    []time.Time    // sliding window for outgoing rate limit

	ctx        context.Context               // agent's own context; set at the start of run()
	msgCh      chan *discordgo.MessageCreate // buffered 100
	internalCh chan string                   // buffered; receives system messages (e.g., web search results)
	cancel     context.CancelFunc            // cancels this agent's context
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
const maxImageEditSourceImages = 14

// internalTurnMaxIter caps LLM completion round-trips for system-generated turns (e.g. web search
// results). Each iteration may produce multiple tool calls. Assumes at most one web_fetch call per
// internal turn; chaining two web_fetch calls before replying would tighten the budget unexpectedly.
const internalTurnMaxIter = 3

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

// buildUserMessage converts a Discord message into an llm.Message, attaching
// media from the message and the message it replies to as vision content parts.
func buildUserMessage(ctx context.Context, httpClient *http.Client, msg *discordgo.MessageCreate, botID, botName string) llm.Message {
	text := historyUserContent(msg.Message, botID, botName)
	return userMessageWithMedia(text, downloadMediaParts(ctx, httpClient, msg.Message, msg.ReferencedMessage))
}

// userMessageWithMedia builds a user message from text and optional media parts.
func userMessageWithMedia(text string, media []llm.ContentPart) llm.Message {
	if len(media) == 0 {
		return llm.Message{Role: "user", Content: text}
	}
	parts := make([]llm.ContentPart, 0, 1+len(media))
	parts = append(parts, llm.ContentPart{Type: "text", Text: text})
	parts = append(parts, media...)
	return llm.Message{Role: "user", ContentParts: parts}
}

// downloadMediaParts downloads image and video attachments and GIF embed
// thumbnails from msgs as base64 data URL content parts. Discord CDN URLs
// require authentication, so media must be fetched server-side. Media shared by
// several messages (e.g. two replies to the same message) is included once, and
// media that fails to download is skipped. Nil messages are ignored.
func downloadMediaParts(ctx context.Context, httpClient *http.Client, msgs ...*discordgo.Message) []llm.ContentPart {
	var parts []llm.ContentPart
	seen := make(map[string]bool)
	add := func(partType, url, contentType string) {
		if seen[url] {
			return
		}
		seen[url] = true
		dataURL, err := downloadURLAsDataURL(ctx, httpClient, url, contentType)
		if err != nil {
			slog.Warn("failed to download media, skipping", "error", err, "url", url)
			return
		}
		part := llm.ContentPart{Type: partType}
		if partType == "video_url" {
			part.VideoURL = &llm.VideoURL{URL: dataURL}
		} else {
			part.ImageURL = &llm.ImageURL{URL: dataURL}
		}
		parts = append(parts, part)
	}
	for _, m := range msgs {
		if m == nil {
			continue
		}
		images, videos := classifyAttachments(m.Attachments)
		for _, a := range images {
			add("image_url", a.URL, a.ContentType)
		}
		for _, a := range videos {
			add("video_url", a.URL, a.ContentType)
		}
		for _, u := range gifEmbedURLs(m) {
			add("image_url", u, "")
		}
	}
	return parts
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

// collectImageDataURLs downloads image attachments from a message and its
// referenced message for use as fal.ai edit inputs.
func collectImageDataURLs(ctx context.Context, client *http.Client, msg *discordgo.Message) []string {
	if msg == nil {
		return nil
	}
	var urls []string
	collect := func(attachments []*discordgo.MessageAttachment) {
		for _, a := range attachments {
			if len(urls) >= maxImageEditSourceImages {
				return
			}
			if !strings.HasPrefix(a.ContentType, "image/") {
				continue
			}
			dataURL, err := downloadImageAsDataURL(ctx, client, a)
			if err != nil {
				slog.Warn("failed to download image edit source, skipping", "error", err, "url", a.URL)
				continue
			}
			urls = append(urls, dataURL)
		}
	}
	collect(msg.Attachments)
	if msg.ReferencedMessage != nil {
		collect(msg.ReferencedMessage.Attachments)
	}
	return urls
}

func collectImageDataURLsFromMessages(ctx context.Context, client *http.Client, msgs []*discordgo.MessageCreate) []string {
	var urls []string
	for _, m := range msgs {
		if len(urls) >= maxImageEditSourceImages {
			return urls
		}
		urls = append(urls, collectImageDataURLs(ctx, client, m.Message)...)
		if len(urls) > maxImageEditSourceImages {
			urls = urls[:maxImageEditSourceImages]
		}
	}
	return urls
}

// sendAllowed checks whether sending a message is within the configured rate
// limit. It prunes expired timestamps and records the current send if allowed.
// Only called from the agent's serial run loop, so no mutex is needed.
//
// Budget note: a single turn can consume up to 4 slots (2 reply-tool calls ×
// 2 Discord message parts each). With the default limit of 4 per 60s, a
// follow-up turn within the same window may have its reply dropped — this
// tight budget is intentional to prevent bot flooding.
func (a *ChannelAgent) sendAllowed() bool {
	cfg := a.cfgStore.Get()
	limit := cfg.Agent.SendRateLimit
	window := time.Duration(cfg.Agent.SendRateWindowSeconds) * time.Second

	now := time.Now()
	cutoff := now.Add(-window)
	valid := a.sendTimestamps[:0]
	for _, ts := range a.sendTimestamps {
		if ts.After(cutoff) {
			valid = append(valid, ts)
		}
	}
	a.sendTimestamps = valid

	if len(a.sendTimestamps) >= limit {
		return false
	}
	a.sendTimestamps = append(a.sendTimestamps, now)
	return true
}

// rateLimitedSendFn wraps a raw send function with the per-channel rate limiter.
func (a *ChannelAgent) rateLimitedSendFn(raw func(string) error) func(string) error {
	return func(content string) error {
		if !a.sendAllowed() {
			a.logger.Warn("outgoing rate limit exceeded, dropping message", "channel_id", a.channelID)
			// Return nil so the LLM does not see an error and retry — the LLM
			// may believe it communicated successfully, which is acceptable because
			// surfacing an error would trigger retry loops that worsen flooding.
			return nil
		}
		return raw(content)
	}
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
	defer a.imageWg.Wait()

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
			// Wait for all in-flight searches to finish, then drain any
			// additional results so everything is handled in one turn.
			a.searchWg.Wait()
			for {
				select {
				case extra := <-a.internalCh:
					intMsg += "\n\n" + extra
				default:
					goto drained
				}
			}
		drained:
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
	return containsBotName(m.Content, botName)
}

// plainTextMentionRe matches a @Username-like pattern at the start of a message.
// The name must begin with a letter and can contain letters, digits, and underscores.
var plainTextMentionRe = regexp.MustCompile(`^@(\pL[\pL\pN_]*)[\s,:]`)

// isDirectedAtOther reports whether a Discord message is clearly directed at
// another specific user (not the bot). It checks for Discord @mentions of
// other users and plain-text @Name patterns at the start of the message.
func isDirectedAtOther(msg *discordgo.MessageCreate, botID, botName string) bool {
	if msg.GuildID == "" {
		return false // DMs are never directed at "other"
	}

	// Discord @mentions: other users mentioned but NOT the bot.
	if len(msg.Mentions) > 0 {
		var hasBotMention, hasOtherMention bool
		for _, u := range msg.Mentions {
			if u.ID == botID {
				hasBotMention = true
			} else {
				hasOtherMention = true
			}
		}
		return hasOtherMention && !hasBotMention
	}

	// No Discord mentions — check for plain-text @Name at start.
	m := plainTextMentionRe.FindStringSubmatch(msg.Content)
	if m == nil {
		return false
	}
	name := strings.ToLower(m[1])
	if name == "everyone" || name == "here" {
		return false
	}

	// If the name closely matches the bot's name, this is addressing the bot,
	// not another user. Prefix matching handles morphological forms common in
	// inflected languages (e.g. Czech: Machmonstrum → Machmonstře).
	if looksLikeBotName(name, strings.ToLower(botName)) {
		return false
	}

	return true
}

// looksLikeBotName reports whether name is likely a morphological variant of
// botName by checking that the two share a long common prefix (≥75% of the
// longer string, minimum 4 runes). This handles declensions in inflected
// languages (e.g. Czech: Machmonstrum → Machmonstře) without false-positiving
// on short coincidental prefixes.
func looksLikeBotName(name, botName string) bool {
	return matchesBotNameRunes(name, []rune(norm.NFC.String(botName)))
}

// matchesBotNameRunes is the rune-level implementation of looksLikeBotName.
// Accepting pre-computed botRunes lets callers that compare many names against
// the same botName (e.g. containsBotName) avoid re-normalising it every iteration.
func matchesBotNameRunes(name string, botRunes []rune) bool {
	nr := []rune(norm.NFC.String(name))
	var shared int
	for shared < len(nr) && shared < len(botRunes) && nr[shared] == botRunes[shared] {
		shared++
	}
	longer := len(nr)
	if len(botRunes) > longer {
		longer = len(botRunes)
	}
	return shared >= 4 && shared*4 >= longer*3
}

// containsBotName reports whether any word in content looks like a
// morphological variant of botName (handles @-prefixed and punctuation-suffixed
// tokens). Note: bot names shorter than 4 runes will never match because the
// morphological matching threshold requires at least 4 shared leading runes.
func containsBotName(content, botName string) bool {
	if botName == "" {
		return false
	}
	botRunes := []rune(norm.NFC.String(strings.ToLower(botName)))
	for _, token := range strings.Fields(content) {
		// Secondary split on punctuation that users commonly omit spaces after
		// (e.g. "Botname,how are you?" → ["Botname", "how are you?"]).
		subTokens := strings.FieldsFunc(token, func(r rune) bool {
			return r == ',' || r == ';' || r == ':'
		})
		for _, sub := range subTokens {
			word := strings.TrimLeft(sub, "@")
			word = strings.TrimRight(word, `.,!?;:)'">\]}`)
			if word == "" {
				continue
			}
			if matchesBotNameRunes(strings.ToLower(word), botRunes) {
				return true
			}
		}
	}
	return false
}

// turnParams holds the inputs needed by processTurn, allowing handleMessage and
// handleMessages to share the tool-call loop and post-processing logic.
type turnParams struct {
	mode            string
	systemPrompt    string
	sendFn          func(string) error
	reg             *tools.Registry
	llmMsgs         []llm.Message
	userMsgText     string // human-readable user input for conversation logging
	internal        bool   // true for system-generated turns (e.g., web search results); skips LogConversation
	maxIter         int    // override cfg.Agent.MaxToolIterations; 0 = use config default
	addressed       bool   // true when the user directly @mentioned the bot
	directedAtOther bool   // true when the message targets a specific other user (not the bot); zero-value (false) is safe for internal paths
	// mediaDescription waits for the vision model's description of media the
	// main model saw directly; nil when there is none. See prepareMedia.
	mediaDescription func() string
}

func (a *ChannelAgent) handleMessage(ctx context.Context, msg *discordgo.MessageCreate) {
	a.lastActive.Store(time.Now().UnixNano())

	cfg := a.cfgStore.Get()
	mode := cfg.ResolveResponseMode(a.serverID, msg.ChannelID)
	botID := a.resources.Session.State.User.ID
	botName := a.resources.Session.State.User.Username
	addressed := isAddressedToBot(msg, botID, botName)

	switch mode {
	case config.ModeNone:
		return
	case config.ModeMention:
		if !addressed {
			return
		}
	}

	directedAtOther := !addressed && mode == "smart" && isDirectedAtOther(msg, botID, botName)

	stopTyping := func() {}
	if mode != config.ModeSmart || addressed {
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

	var userID string
	if msg.Author != nil {
		userID = msg.Author.ID
	}
	memories := a.recallMemories(ctx, cfg, userID, msg.Content)
	systemPrompt := a.buildSystemPrompt(cfg, mode, msg.ChannelID, memories, botName, addressed, directedAtOther)

	sendFn := a.rateLimitedSendFn(func(content string) error {
		_, err := a.resources.Session.ChannelMessageSend(msg.ChannelID, content)
		return err
	})
	reactFn := func(emoji string) error {
		return a.resources.Session.MessageReactionAdd(msg.ChannelID, msg.ID, emoji)
	}
	var sourceImageURLs []string
	if a.imageGenConfigured(cfg) {
		sourceImageURLs = collectImageDataURLs(ctx, a.httpClient, msg.Message)
	}
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, cfg.Agent.MemoryDedupThreshold, cfg.Agent.MemoryRecallLimit, sendFn, reactFn, a.webSearchDeps(), a.imageGenDeps(a.makeSendImageFn(msg.ChannelID), sendFn, sourceImageURLs, msg.ChannelID, msg.ID), cfg.Agent.MaxReplyParts)

	userMsg := buildUserMessage(ctx, a.httpClient, msg, botID, botName)
	mediaDescription := a.prepareMedia(ctx, cfg, &userMsg)
	llmMsgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(llmMsgs, a.history)
	llmMsgs = append(llmMsgs, userMsg)

	a.processTurn(ctx, cfg, turnParams{
		mode:             mode,
		systemPrompt:     systemPrompt,
		sendFn:           sendFn,
		reg:              reg,
		llmMsgs:          llmMsgs,
		userMsgText:      historyUserContent(msg.Message, botID, botName),
		addressed:        addressed,
		directedAtOther:  directedAtOther,
		mediaDescription: mediaDescription,
	})
}

// prepareMedia readies the media in msg for the main model. A main model that
// sees images keeps them in msg for the whole turn, and the vision model
// describes them in the background so history can keep what they showed. The
// returned function waits for that description; it is nil when there is none
// to wait for. Media the main model cannot see (videos, or images for a
// text-only model) is replaced by the vision model's description before the
// turn.
func (a *ChannelAgent) prepareMedia(ctx context.Context, cfg *config.Config, msg *llm.Message) func() string {
	if !hasMediaParts(msg.ContentParts) {
		return nil
	}
	describe := cfg.LLM.VisionModel != "" && (cfg.LLM.MediaDescriptions == nil || *cfg.LLM.MediaDescriptions)
	seesImages := a.llm.SeesImages(ctx, a.chatOptions())
	if seesImages && !hasVideoParts(msg.ContentParts) {
		if !describe {
			return nil
		}
		parts := slices.Clone(msg.ContentParts)
		done := make(chan string, 1)
		go func() { done <- a.describeMedia(ctx, cfg, parts) }()
		return sync.OnceValue(func() string { return <-done })
	}
	if describe {
		a.annotateMediaDescription(ctx, cfg, msg)
		stripMediaParts(msg, seesImages)
	}
	return nil
}

// hasMediaParts reports whether parts contains at least one image or video part.
func hasMediaParts(parts []llm.ContentPart) bool {
	for _, p := range parts {
		if p.Type == "image_url" || p.Type == "video_url" {
			return true
		}
	}
	return false
}

// hasVideoParts reports whether parts contains at least one video part.
func hasVideoParts(parts []llm.ContentPart) bool {
	return slices.ContainsFunc(parts, func(p llm.ContentPart) bool { return p.Type == "video_url" })
}

// stripMediaParts removes media parts from msg; keepImages leaves the image
// parts for a main model that sees them. A message left with text only becomes
// plain Content: while the current message has content parts and the main
// model cannot see images, Chat() routes the turn to the vision model.
func stripMediaParts(msg *llm.Message, keepImages bool) {
	var texts []string
	var images []llm.ContentPart
	for _, p := range msg.ContentParts {
		switch {
		case p.Type == "text":
			texts = append(texts, p.Text)
		case p.Type == "image_url" && keepImages:
			images = append(images, p)
		}
	}
	text := strings.Join(texts, "\n")
	if len(images) == 0 {
		msg.Content = text
		msg.ContentParts = nil
		return
	}
	parts := make([]llm.ContentPart, 0, 1+len(images))
	if text != "" {
		parts = append(parts, llm.ContentPart{Type: "text", Text: text})
	}
	msg.ContentParts = append(parts, images...)
}

// forgetMedia turns messages that still carry media into plain text, so
// history keeps no base64 blobs. description, when set, returns the vision
// model's description of the media the main model saw during the turn, and
// that description stays in history in place of the media.
func forgetMedia(msgs []llm.Message, description func() string) {
	for i := range msgs {
		if !hasMediaParts(msgs[i].ContentParts) {
			continue
		}
		stripMediaParts(&msgs[i], false)
		if description == nil {
			continue
		}
		if desc := description(); desc != "" {
			msgs[i].Content = strings.TrimSpace(msgs[i].Content + "\n" + mediaDescriptionLabel + desc + "]")
		}
	}
}

// describeMedia asks the vision model to describe the media in parts and
// returns the trimmed, length-capped description, or "" when the call fails.
// parts includes the user's text on purpose: it tells the vision model what
// the user asked about.
func (a *ChannelAgent) describeMedia(ctx context.Context, cfg *config.Config, parts []llm.ContentPart) string {
	// 4x: 1 per retry attempt (up to 3 retries) plus 1 buffer for backoff delays.
	timeout := time.Duration(cfg.LLM.RequestTimeoutSeconds) * time.Second * 4
	descCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	desc, err := a.llm.DescribeMedia(descCtx, parts)
	if err != nil {
		a.logger.Warn("media description failed", "error", err)
		return ""
	}
	desc = strings.TrimSpace(desc)
	if runes := []rune(desc); len(runes) > maxMediaDescriptionRunes {
		desc = string(runes[:maxMediaDescriptionRunes]) + "..."
	}
	return desc
}

// annotateMediaDescription injects the vision model's description of the media
// in msg.ContentParts into its text part. The description survives
// stripMediaParts(), so the main model can answer from it and reference it later.
func (a *ChannelAgent) annotateMediaDescription(ctx context.Context, cfg *config.Config, msg *llm.Message) {
	desc := a.describeMedia(ctx, cfg, msg.ContentParts)
	if desc == "" {
		return
	}
	for i := range msg.ContentParts {
		if msg.ContentParts[i].Type == "text" {
			msg.ContentParts[i].Text += "\n" + mediaDescriptionLabel + desc + "]"
			return
		}
	}
	// No text part found (image-only message): prepend a new text part so the
	// description is not silently dropped.
	msg.ContentParts = append([]llm.ContentPart{{Type: "text", Text: mediaDescriptionLabel + desc + "]"}}, msg.ContentParts...)
	a.logger.Debug("prepended media description text part for image-only message")
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
	case config.ModeNone:
		return
	case config.ModeMention:
		if !anyAddressed {
			return
		}
	}

	var allDirectedAtOther bool
	if !anyAddressed && mode == "smart" {
		allDirectedAtOther = true
		for _, m := range msgs {
			if !isDirectedAtOther(m, botID, botName) {
				allDirectedAtOther = false
				break
			}
		}
	}

	stopTyping := func() {}
	if mode != config.ModeSmart || anyAddressed {
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

	var lastAuthorID string
	if lastMsg.Author != nil {
		lastAuthorID = lastMsg.Author.ID
	}
	memories := a.recallMemories(ctx, cfg, lastAuthorID, recallQuery)

	systemPrompt := a.buildSystemPrompt(cfg, mode, lastMsg.ChannelID, memories, botName, anyAddressed, allDirectedAtOther)

	sendFn := a.rateLimitedSendFn(func(content string) error {
		_, err := a.resources.Session.ChannelMessageSend(lastMsg.ChannelID, content)
		return err
	})
	reactFn := func(emoji string) error {
		return a.resources.Session.MessageReactionAdd(lastMsg.ChannelID, lastMsg.ID, emoji)
	}
	var sourceImageURLs []string
	if a.imageGenConfigured(cfg) {
		sourceImageURLs = collectImageDataURLsFromMessages(ctx, a.httpClient, msgs)
	}
	reg := tools.NewDefaultRegistry(a.resources.Memory, a.serverID, cfg.Agent.MemoryDedupThreshold, cfg.Agent.MemoryRecallLimit, sendFn, reactFn, a.webSearchDeps(), a.imageGenDeps(a.makeSendImageFn(lastMsg.ChannelID), sendFn, sourceImageURLs, lastMsg.ChannelID, lastMsg.ID), cfg.Agent.MaxReplyParts)

	combinedUserMsg := a.buildCombinedUserMessage(ctx, msgs, botID, botName)
	mediaDescription := a.prepareMedia(ctx, cfg, &combinedUserMsg)

	llmMsgs := make([]llm.Message, len(a.history), len(a.history)+1)
	copy(llmMsgs, a.history)
	llmMsgs = append(llmMsgs, combinedUserMsg)

	userLogLines := make([]string, 0, len(msgs))
	for _, m := range msgs {
		userLogLines = append(userLogLines, historyUserContent(m.Message, botID, botName))
	}

	a.processTurn(ctx, cfg, turnParams{
		mode:             mode,
		systemPrompt:     systemPrompt,
		sendFn:           sendFn,
		reg:              reg,
		llmMsgs:          llmMsgs,
		userMsgText:      strings.Join(userLogLines, "\n"),
		addressed:        anyAddressed,
		directedAtOther:  allDirectedAtOther,
		mediaDescription: mediaDescription,
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
	sb.WriteString("You are receiving web search results. Summarize the findings for the user. Do NOT repeat or rephrase anything you said earlier in the conversation. Do NOT reference or claim to have previously told the user anything — you have not spoken to them yet in this context. Focus on presenting only the new information clearly, with relevant sources and links. Reply IMMEDIATELY with the data — do NOT send a status message first. You MUST call the reply tool exactly once with the complete answer.")
	if lang := cfg.ResolveLanguage(a.serverID, a.channelID); lang != "" {
		fmt.Fprintf(&sb, "\n\nAlways respond in %s.", lang)
	}

	sendFn := a.rateLimitedSendFn(func(text string) error {
		_, err := a.resources.Session.ChannelMessageSend(a.channelID, text)
		return err
	})
	reactFn := func(emoji string) error { return nil }
	// Minimal registry: only reply + react. No memory tools, no web_search/web_fetch.
	// The LLM's only job is to summarize the search snippets and reply.
	reg := tools.NewReplyOnlyRegistry(sendFn, reactFn, cfg.Agent.MaxReplyParts)

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
		maxIter:      internalTurnMaxIter,
	})
	// Trim back to the pre-turn history length, preserving any assistant reply that
	// processTurn appended, but dropping the injected system message entry.
	if len(a.history) > historyLen {
		a.history = a.history[:historyLen]
	}
}

// recallMemories runs the two-pass recall: user-specific memories first,
// then content-relevant memories, merged and capped at the configured limit.
func (a *ChannelAgent) recallMemories(ctx context.Context, cfg *config.Config, userID, contentQuery string) []memory.MemoryRow {
	limit := cfg.Agent.MemoryRecallLimit
	var userMems []memory.MemoryRow
	if userID != "" {
		var err error
		userMems, err = a.resources.Memory.RecallByUser(ctx, a.serverID, userID, limit/2)
		if err != nil {
			a.logger.Warn("user memory recall error", "error", err)
		}
	}
	contentMems, err := a.resources.Memory.Recall(ctx, contentQuery, a.serverID, limit, cfg.Agent.MemoryRecallThreshold)
	if err != nil {
		a.logger.Warn("content memory recall error", "error", err)
	}
	return mergeMemories(userMems, contentMems, limit)
}

// mergeMemories combines user-specific and content-relevant memories,
// deduplicating by ID. User-specific memories appear first. Result is
// capped at limit.
func mergeMemories(userMems, contentMems []memory.MemoryRow, limit int) []memory.MemoryRow {
	seen := make(map[string]bool, len(userMems)+len(contentMems))
	out := make([]memory.MemoryRow, 0, limit)

	for _, m := range userMems {
		if len(out) >= limit {
			break
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	for _, m := range contentMems {
		if len(out) >= limit {
			break
		}
		if !seen[m.ID] {
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}

// buildSystemPrompt assembles the system prompt from the soul text, memories,
// language override, and response mode.
func (a *ChannelAgent) buildSystemPrompt(cfg *config.Config, mode, channelID string, memories []memory.MemoryRow, botName string, addressed, directedAtOther bool) string {
	var sb strings.Builder
	if botName != "" {
		fmt.Fprintf(&sb, "Your Discord username is %s.\n\n", botName)
	}
	now := time.Now()
	fmt.Fprintf(&sb, "Today's date is %s.\n\n", now.Format("Monday, January 2, 2006"))
	sb.WriteString(a.soulText)
	if len(memories) > 0 {
		sb.WriteString("\n\n## Relevant Memories\n")
		for _, m := range memories {
			age := now.Sub(m.CreatedAt)
			var ageStr string
			switch {
			case age < 24*time.Hour:
				ageStr = "today"
			case age < 48*time.Hour:
				ageStr = "yesterday"
			case age < 7*24*time.Hour:
				ageStr = fmt.Sprintf("%d days ago", int(age.Hours()/24))
			case age < 30*24*time.Hour:
				weeks := int(age.Hours() / 24 / 7)
				if weeks == 1 {
					ageStr = "1 week ago"
				} else {
					ageStr = fmt.Sprintf("%d weeks ago", weeks)
				}
			default:
				months := int(age.Hours() / 24 / 30)
				if months == 1 {
					ageStr = "1 month ago"
				} else {
					ageStr = fmt.Sprintf("%d months ago", months)
				}
			}
			fmt.Fprintf(&sb, "- [%s] (importance: %.1f, %s) %s\n", m.ID, m.Importance, ageStr, m.Content)
		}
	}
	sb.WriteString("\n\n")
	sb.WriteString(soul.Humanizer)
	if lang := cfg.ResolveLanguage(a.serverID, channelID); lang != "" {
		fmt.Fprintf(&sb, "\n\nAlways respond in %s.", lang)
	}
	if mode == config.ModeSmart {
		if addressed {
			sb.WriteString("\n\nYou are in smart mode but the user directly mentioned or replied to you — you MUST respond using the `reply` or `react` tools. Prefer `reply` — only use `react` alone when a reaction is clearly more appropriate than words.")
		} else if directedAtOther {
			sb.WriteString("\n\nYou are in smart mode. This message is directed at another specific user — you MUST stay silent. Do NOT reply, react, or produce any output.")
		} else {
			sb.WriteString("\n\nYou are in smart mode. Decide whether to respond:\n- RESPOND (via `reply` or `react` tools) when: someone asks a question to the channel, continues a conversation with you, mentions your name, shares something interesting or relevant to you\n- STAY SILENT (produce no output at all) when: people are clearly talking to each other, the message is not directed at you, it's a side conversation you're not part of\nUse `react` sparingly — only when the message genuinely resonates, is funny, or deserves acknowledgment. Do not react to most messages; silence is the default.\nDo NOT write meta-commentary about why you are staying silent.")
		}
	}
	return sb.String()
}

// buildCombinedUserMessage builds an LLM user message from a batch of coalesced
// Discord messages, attaching media from each message and the message it replies to.
func (a *ChannelAgent) buildCombinedUserMessage(ctx context.Context, msgs []*discordgo.MessageCreate, botID, botName string) llm.Message {
	sources := make([]*discordgo.Message, 0, 2*len(msgs))
	for _, m := range msgs {
		sources = append(sources, m.Message, m.ReferencedMessage)
	}
	return userMessageWithMedia(buildCombinedContent(msgs, botID, botName), downloadMediaParts(ctx, a.httpClient, sources...))
}

// currentAgentConfig looks up the current per-agent config from the live
// config store by server ID. This ensures running channel agents always use
// the latest config rather than the snapshot captured at creation time.
// Returns nil for DMs or unconfigured servers.
func (a *ChannelAgent) currentAgentConfig() *config.AgentConfig {
	cfg := a.cfgStore.Get()
	for i := range cfg.Agents {
		if cfg.Agents[i].ServerID == a.serverID {
			return &cfg.Agents[i]
		}
	}
	return nil
}

// chatOptions returns ChatOptions for main chat completion calls, carrying the
// configured max_tokens cap and any per-agent provider/model overrides.
// Auxiliary calls (vision descriptions, web search summarization) must NOT use
// this function — they should construct their own ChatOptions without MaxTokens.
func (a *ChannelAgent) chatOptions() *llm.ChatOptions {
	globalCfg := a.cfgStore.Get()
	agentCfg := a.currentAgentConfig()

	opts := &llm.ChatOptions{
		MaxTokens:       globalCfg.LLM.MaxTokens,
		ReasoningEffort: globalCfg.LLM.ReasoningEffort,
	}
	if agentCfg != nil {
		opts.Provider = agentCfg.Provider
		opts.Model = agentCfg.Model
	}
	return opts
}

// webSearchDeps returns the dependency bundle for the async web search tool,
// or nil if web search is not configured (no GLM key or Brave key).
func (a *ChannelAgent) webSearchDeps() *tools.WebSearchDeps {
	cfg := a.cfgStore.Get()

	// Check if search is configured at all
	hasGLM := cfg.LLM.GLMKey != ""
	hasBrave := cfg.Tools.Search.Provider == "brave" && cfg.Tools.Search.APIKey != ""

	if !hasGLM && !hasBrave {
		slog.Warn("web_search tool disabled: no search provider configured", "server_id", a.serverID)
		return nil
	}

	// Resolve the model: use the agent's configured model when available.
	model := cfg.LLM.Model
	if agentCfg := a.currentAgentConfig(); agentCfg != nil && agentCfg.Model != "" {
		model = agentCfg.Model
	}

	// Use Brave timeout if configured, otherwise use web timeout
	timeout := cfg.Tools.WebTimeoutSeconds
	if cfg.Tools.Search.Timeout > 0 {
		timeout = cfg.Tools.Search.Timeout
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
		TimeoutSeconds: timeout,
		SearchProvider: cfg.Tools.Search.Provider,
		SearchAPIKey:   cfg.Tools.Search.APIKey,
	}
}

func (a *ChannelAgent) makeSendImageFn(channelID string) tools.SendImageFunc {
	return func(filename string, data io.Reader, caption string) error {
		_, err := a.resources.Session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content: caption,
			Files:   []*discordgo.File{{Name: filename, Reader: data}},
		})
		return err
	}
}

func (a *ChannelAgent) imageGenConfigured(cfg *config.Config) bool {
	if cfg.Tools.Image.APIKey != "" {
		return true
	}
	if agentCfg := a.currentAgentConfig(); agentCfg != nil && agentCfg.Image.APIKey != "" {
		return true
	}
	return false
}

func (a *ChannelAgent) imageGenDeps(sendImage tools.SendImageFunc, sendText tools.SendFunc, sourceImageURLs []string, sourceChannelID, sourceMessageID string) *tools.ImageGenDeps {
	cfg := a.cfgStore.Get()

	// Resolve per-agent overrides over global config.
	apiKey := cfg.Tools.Image.APIKey
	model := cfg.Tools.Image.Model
	editModel := cfg.Tools.Image.EditModel
	resolution := cfg.Tools.Image.Resolution
	safetyChecker := true
	if cfg.Tools.Image.EnableSafetyChecker != nil {
		safetyChecker = *cfg.Tools.Image.EnableSafetyChecker
	}
	if agentCfg := a.currentAgentConfig(); agentCfg != nil {
		if agentCfg.Image.APIKey != "" {
			apiKey = agentCfg.Image.APIKey
		}
		if agentCfg.Image.Model != "" {
			model = agentCfg.Image.Model
		}
		if agentCfg.Image.EditModel != "" {
			editModel = agentCfg.Image.EditModel
		}
		if agentCfg.Image.Resolution != "" {
			resolution = agentCfg.Image.Resolution
		}
		if agentCfg.Image.EnableSafetyChecker != nil {
			safetyChecker = *agentCfg.Image.EnableSafetyChecker
		}
	}

	if apiKey == "" {
		return nil
	}
	return &tools.ImageGenDeps{
		SendImage:       sendImage,
		SendText:        sendText,
		ImageWg:         &a.imageWg,
		ImageRunning:    &a.imageRunning,
		Ctx:             a.ctx,
		APIKey:          apiKey,
		Model:           model,
		EditModel:       editModel,
		SourceImageURLs: sourceImageURLs,
		Resolution:      resolution,
		SafetyChecker:   safetyChecker,
		TimeoutSeconds:  cfg.Tools.Image.TimeoutSeconds,
		VisualStore:     a.resources.Memory,
		ServerID:        a.serverID,
		SourceChannelID: sourceChannelID,
		SourceMessageID: sourceMessageID,
	}
}

// processTurn runs the tool-call loop, applies content suppression, logs the
// conversation, sends the reply, and updates history. Both handleMessage and
// handleMessages delegate here after preparing their inputs.
func (a *ChannelAgent) processTurn(ctx context.Context, cfg *config.Config, tp turnParams) {
	chatOpts := a.chatOptions()

	maxIter := cfg.Agent.MaxToolIterations
	if tp.maxIter > 0 {
		maxIter = tp.maxIter
	}

	var toolCalls []toolCallRecord
	var assistantContent string
	var lastChoice llm.Choice // most recent LLM response, kept for diagnostics
	var replyToolText string  // captures the text sent via the reply tool before ReplyText is zeroed
	postReplyIter := -1       // iteration at which the reply tool first fired
	for iter := 0; ; iter++ {
		if iter >= maxIter {
			a.logger.Warn("tool loop exhausted; staying silent", append([]any{"max_iterations", maxIter}, lastChoice.LogAttrs()...)...)
			return
		}

		choice, err := a.llm.Chat(ctx, buildMessages(tp.systemPrompt, tp.llmMsgs), tp.reg.Definitions(), chatOpts)
		if err != nil {
			effectiveModel := cfg.LLM.Model
			if chatOpts != nil && chatOpts.Model != "" {
				effectiveModel = chatOpts.Model
			}
			a.logger.Error("llm chat error", "error", err, "model", effectiveModel)
			return
		}
		lastChoice = choice

		if len(choice.Message.ToolCalls) == 0 {
			assistantContent = choice.Message.Content
			break
		}

		tp.llmMsgs = append(tp.llmMsgs, choice.Message)
		var hasFetchTool, hasWebFetch bool
		for _, tc := range choice.Message.ToolCalls {
			if tc.Function.Name == tools.ToolNameWebFetch {
				hasWebFetch = true
			}
			if tc.Function.Name == tools.ToolNameWebFetch || tc.Function.Name == tools.ToolNameWebSearch || tc.Function.Name == tools.ToolNameImageGen {
				hasFetchTool = true
			}
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

		// A reply sent alongside web_fetch is a status message whatever the call
		// order, so the answer that follows the fetched content may still be sent.
		if hasWebFetch && tp.reg.Replied {
			tp.reg.WebFetchCalled = true
		}

		// After executing tool calls, if the reply tool was used, record what was said
		// so subsequent LLM calls have context of what the assistant replied.
		// Reset ReplyText after appending so this only fires once (Replied is a sticky latch).
		if tp.reg.Replied && tp.reg.ReplyText != "" {
			replyToolText = tp.reg.ReplyText
			tp.llmMsgs = append(tp.llmMsgs, llm.Message{Role: "assistant", Content: tp.reg.ReplyText})
			tp.reg.ReplyText = ""
		}

		// If the LLM produced multi-line content alongside only side-effect tool calls
		// (memory ops, react — not web_fetch or web_search), treat it as the final reply
		// and stop. Multi-line content indicates a structured answer (e.g. a formatted
		// stock list); single-line content is a transitional status comment (e.g.
		// "Ukládám si tvoji strategii...") and should not be treated as a final reply.
		// Without this guard, the loop would continue and the LLM calls web_fetch
		// unnecessarily because it sees no sent reply in the context.
		if !tp.reg.Replied && !hasFetchTool && strings.Contains(choice.Message.Content, "\n") {
			assistantContent = choice.Message.Content
			break
		}

		// Immediate break: web_search was invoked — results arrive async via
		// handleInternalMessage. No need to wait for the reply tool; the LLM
		// often puts a short status message in content (not via the reply tool),
		// and continuing the loop lets it fire additional searches/fetches.
		// Capture the inline content so the post-loop code can send it as the
		// "searching" status message to Discord.
		if tp.reg.WebSearchCalled {
			if !tp.reg.Replied && choice.Message.Content != "" {
				assistantContent = choice.Message.Content
			}
			break
		}

		// Immediate break: generate_image was invoked — the image is sent
		// directly to Discord from the background goroutine.
		if tp.reg.ImageGenCalled {
			if !tp.reg.Replied && choice.Message.Content != "" {
				assistantContent = choice.Message.Content
			}
			break
		}

		// General post-reply cap: allow 1 more iteration for react/memory, then stop.
		if tp.reg.Replied && postReplyIter < 0 {
			postReplyIter = iter
		}
		if postReplyIter >= 0 && iter > postReplyIter+1 {
			break
		}
	}

	if tp.mode == "smart" && !tp.reg.Replied && !tp.reg.Reacted && assistantContent == "" {
		a.logger.Debug("smart mode: LLM chose not to respond", "addressed", tp.addressed)
	}

	if assistantContent != "" && looksLikeToolCall(assistantContent, tp.reg.Definitions()) {
		a.logger.Warn("suppressed tool-call syntax leaked into content", "content", assistantContent)
		assistantContent = ""
	}

	// In smart mode the model should only communicate via reply/react tools.
	// Suppress any leftover plain-text content that was not sent through a tool.
	// Messages directed at another user are always suppressed, even if the LLM
	// called web_search or image_gen.
	if shouldSuppressSmartMode(tp.mode, assistantContent != "", tp.reg, tp.addressed, tp.internal, tp.directedAtOther) {
		a.logger.Debug("suppressed smart-mode plain-text non-reply", "content", assistantContent, "directedAtOther", tp.directedAtOther)
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
			responseText = replyToolText
		}
		if err := a.resources.Memory.LogConversation(ctx, a.channelID, tp.userMsgText, toolCallsJSON, responseText); err != nil {
			a.logger.Warn("log conversation error", "error", err)
		}
	}

	if assistantContent != "" && !tp.reg.Replied {
		parts := tools.SplitAndCapMessage(assistantContent, 2000, cfg.Agent.MaxReplyParts)
		for _, p := range parts {
			if err := tp.sendFn(p); err != nil {
				a.logger.Error("send message", "error", err)
			}
		}
	}

	// Never send a generic error message to Discord. When the bot was addressed but
	// produced nothing (empty content, refusal, token limit), stay silent and log
	// the response metadata so the cause can be diagnosed.
	if producedNoOutput(tp.internal, tp.reg, assistantContent != "", tp.addressed) {
		a.logger.Warn("LLM produced no output for addressed message; staying silent", lastChoice.LogAttrs()...)
	}

	if assistantContent != "" {
		tp.llmMsgs = append(tp.llmMsgs, llm.Message{Role: "assistant", Content: assistantContent})
	}
	forgetMedia(tp.llmMsgs, tp.mediaDescription)
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

// shouldSuppressSmartMode reports whether plain-text content from the LLM should
// be dropped in smart mode. Content is preserved when:
//   - the reply tool was already used,
//   - the message was addressed (@mention),
//   - it's an internal turn (e.g. web search result delivery),
//   - the web_search or generate_image tool was invoked.
//
// Note: reg.Reacted is intentionally NOT an exemption. If the LLM only reacted
// (emoji reaction) without calling the reply tool, any trailing plain-text is
// still suppressed — the model should not leak text alongside a bare reaction
// in smart mode.
func shouldSuppressSmartMode(mode string, hasContent bool, reg *tools.Registry, addressed, internal, directedAtOther bool) bool {
	if mode != config.ModeSmart || !hasContent || reg.Replied || addressed || internal {
		return false
	}
	// Messages directed at another user are always suppressed, even if the LLM
	// happened to call web_search or image_gen.
	if directedAtOther {
		return true
	}
	return !reg.WebSearchCalled && !reg.ImageGenCalled
}

// producedNoOutput reports whether the bot was directly addressed but produced
// no visible output at all — no reply, no image, no reaction, and no plain-text
// content.
func producedNoOutput(internal bool, reg *tools.Registry, hasContent, addressed bool) bool {
	return !internal && !reg.Replied && !reg.ImageGenCalled && !reg.WebSearchCalled && !reg.Reacted && !hasContent && addressed
}

// runMemoryExtraction launches a background goroutine that reviews recent history
// and saves any important information the main turn may have missed.
func (a *ChannelAgent) runMemoryExtraction(ctx context.Context, history []llm.Message) {
	if !a.extractionRunning.CompareAndSwap(false, true) {
		return // extraction already in progress
	}

	snapshot := stripImageParts(history)
	reg := tools.NewMemoryOnlyRegistry(a.resources.Memory, a.serverID, a.cfgStore.Get().Agent.MemoryDedupThreshold, a.cfgStore.Get().Agent.MemoryRecallLimit)

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

// isStageDirection reports whether s is a stage direction like "(staying silent)",
// "[MLČÍM]", or "<IDLE/>" that the model sometimes emits as a non-reply.
// Multi-line strings are never stage directions.
func isStageDirection(s string) bool {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "\n") {
		return false
	}
	return (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) ||
		(strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) ||
		(strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"))
}

// buildMessages constructs the message slice for the LLM with system prompt prepended.
func buildMessages(systemPrompt string, history []llm.Message) []llm.Message {
	msgs := make([]llm.Message, 0, len(history)+1)
	msgs = append(msgs, llm.Message{Role: "system", Content: systemPrompt})
	msgs = append(msgs, history...)
	return msgs
}
