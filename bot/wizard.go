package bot

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/config"
)

const wizardTimeout = 10 * time.Minute

type wizardStep int

const (
	wizardStepMode     wizardStep = iota
	wizardStepChannels
	wizardStepLanguage
)

type wizardState struct {
	step       wizardStep
	guildID    string
	userID     string
	mode       string
	channelIDs []string
	language   string
	createdAt  time.Time
}

// wizardHandler manages the multi-step /init setup wizard sessions.
type wizardHandler struct {
	mu       sync.Mutex
	sessions map[string]*wizardState // key: "guildID:userID"
	ops      AgentOps
}

// newWizardHandler returns a new wizardHandler backed by ops for config writes.
func newWizardHandler(ops AgentOps) *wizardHandler {
	return &wizardHandler{
		sessions: make(map[string]*wizardState),
		ops:      ops,
	}
}

// wizardKey produces the session map key for a guild/user pair.
func wizardKey(guildID, userID string) string {
	return guildID + ":" + userID
}

// intPtr returns a pointer to v; used for SelectMenu MinValues which requires *int.
func intPtr(v int) *int {
	return &v
}

// startInit begins the /init setup wizard for the invoking guild.
func (w *wizardHandler) startInit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	slog.Info("wizard: /init invoked", "guild_id", i.GuildID)

	// Only run in guild context; DMs have no GuildID.
	if i.GuildID == "" || i.Member == nil {
		respondEphemeral(s, i, "This command can only be used inside a server.")
		return
	}

	// If an agent is already configured for this server, decline the wizard.
	cfg := w.ops.CfgStore().Get()
	for _, a := range cfg.Agents {
		if a.ServerID == i.GuildID {
			slog.Info("wizard: server already configured", "guild_id", i.GuildID)
			respondEphemeral(s, i, "This server is already configured. Use `/mode`, `/channel`, `/language` to adjust settings, or `/status` to view current config.")
			return
		}
	}

	// Lazy-cleanup of stale sessions before adding a new one.
	now := time.Now()
	w.mu.Lock()
	for k, st := range w.sessions {
		if now.Sub(st.createdAt) > wizardTimeout {
			delete(w.sessions, k)
		}
	}

	state := &wizardState{
		step:      wizardStepMode,
		guildID:   i.GuildID,
		userID:    i.Member.User.ID,
		createdAt: now,
	}
	w.sessions[wizardKey(i.GuildID, i.Member.User.ID)] = state
	w.mu.Unlock()

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: "**Step 1/3 — Response Mode**\nHow should I respond to messages in this server?",
			Flags:   discordgo.MessageFlagsEphemeral,
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.SelectMenu{
							CustomID:    "vespra:init:mode",
							Placeholder: "Select response mode...",
							Options: []discordgo.SelectMenuOption{
								{Label: "Smart", Value: config.ModeSmart, Description: "AI decides when to respond based on context"},
								{Label: "Mention only", Value: config.ModeMention, Description: "Only respond when @mentioned or replied to"},
								{Label: "All messages", Value: config.ModeAll, Description: "Respond to every message"},
								{Label: "None", Value: config.ModeNone, Description: "Silent by default, enable per-channel"},
							},
						},
					},
				},
			},
		},
	}); err != nil {
		slog.Error("wizard: send step 1", "error", err, "guild_id", i.GuildID)
	}
}

// handleComponent routes message-component interactions to the correct wizard step handler.
func (w *wizardHandler) handleComponent(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if i.GuildID == "" || i.Member == nil {
		return
	}

	customID := i.MessageComponentData().CustomID

	// Only handle wizard-scoped interactions.
	if !strings.HasPrefix(customID, "vespra:init:") {
		return
	}

	key := wizardKey(i.GuildID, i.Member.User.ID)
	w.mu.Lock()
	state, ok := w.sessions[key]
	if !ok || time.Since(state.createdAt) > wizardTimeout {
		if ok {
			delete(w.sessions, key)
		}
		w.mu.Unlock()
		respondEphemeralUpdate(s, i, "Your setup wizard expired. Run `/init` again.")
		return
	}
	w.mu.Unlock()

	switch customID {
	case "vespra:init:mode":
		w.handleModeSelect(s, i, state)
	case "vespra:init:channels":
		w.handleChannelSelect(s, i, state)
	case "vespra:init:channels_skip":
		w.handleChannelSkip(s, i, state)
	case "vespra:init:language":
		w.handleLanguageSelect(s, i, state)
	}
}

// handleModeSelect processes the response-mode selection and advances to step 2.
func (w *wizardHandler) handleModeSelect(s *discordgo.Session, i *discordgo.InteractionCreate, state *wizardState) {
	values := i.MessageComponentData().Values
	if len(values) == 0 {
		respondEphemeralUpdate(s, i, "No mode selected. Run `/init` again.")
		return
	}
	state.mode = values[0]
	state.step = wizardStepChannels

	// Acknowledge immediately so the 3-second Discord deadline is met
	// before the GuildChannels REST call in showChannelStepDeferred.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		slog.Error("wizard: defer step 2", "error", err, "guild_id", state.guildID)
		return
	}

	w.showChannelStepDeferred(s, i, state)
}

// showChannelStepDeferred fetches guild channels and edits the deferred
// interaction response with the channel-selection step.
func (w *wizardHandler) showChannelStepDeferred(s *discordgo.Session, i *discordgo.InteractionCreate, state *wizardState) {
	channels, err := s.GuildChannels(state.guildID)
	if err != nil {
		slog.Error("wizard: fetch guild channels", "error", err, "guild_id", state.guildID)
		editDeferredMessage(s, i, "Failed to fetch channels. Run `/init` again.")
		return
	}

	// Keep only text channels.
	var textChannels []*discordgo.Channel
	for _, ch := range channels {
		if ch.Type == discordgo.ChannelTypeGuildText {
			textChannels = append(textChannels, ch)
		}
	}

	if len(textChannels) == 0 {
		// No text channels — skip straight to language step.
		state.step = wizardStepLanguage
		showLanguageEdit(s, i)
		return
	}

	truncated := len(textChannels) > 25
	if truncated {
		textChannels = textChannels[:25]
	}

	options := make([]discordgo.SelectMenuOption, len(textChannels))
	for idx, ch := range textChannels {
		options[idx] = discordgo.SelectMenuOption{
			Label: ch.Name,
			Value: ch.ID,
		}
	}

	content := "**Step 2/3 — Channel Setup**\nSelect channels to restrict where I'm active. Skip or leave empty to use all channels."
	if truncated {
		content += "\n\n*This server has more than 25 text channels. Showing the first 25 — use the web dashboard for full control.*"
	}

	maxValues := len(textChannels)

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:  "vespra:init:channels",
					MinValues: intPtr(0),
					MaxValues: maxValues,
					Options:   options,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					CustomID: "vespra:init:channels_skip",
					Label:    "Skip",
					Style:    discordgo.SecondaryButton,
				},
			},
		},
	}

	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &components,
	}); err != nil {
		slog.Error("wizard: edit step 2", "error", err, "guild_id", state.guildID)
	}
}

// handleChannelSelect records selected channel IDs and advances to step 3.
func (w *wizardHandler) handleChannelSelect(s *discordgo.Session, i *discordgo.InteractionCreate, state *wizardState) {
	state.channelIDs = i.MessageComponentData().Values
	state.step = wizardStepLanguage
	w.showLanguageStep(s, i)
}

// handleChannelSkip skips channel selection and advances to step 3.
func (w *wizardHandler) handleChannelSkip(s *discordgo.Session, i *discordgo.InteractionCreate, state *wizardState) {
	state.channelIDs = nil
	state.step = wizardStepLanguage
	w.showLanguageStep(s, i)
}

// showLanguageStep renders the language-selection step via a normal InteractionRespond.
// Used by handleChannelSelect and handleChannelSkip, which have not been deferred.
func (w *wizardHandler) showLanguageStep(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content: "**Step 3/3 — Language**\nWhat language should I reply in?",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.SelectMenu{
							CustomID:    "vespra:init:language",
							Placeholder: "Select language...",
							Options: []discordgo.SelectMenuOption{
								{Label: "Default (English)", Value: "default", Description: "No language preference"},
								{Label: "Czech", Value: "Czech"},
								{Label: "Slovak", Value: "Slovak"},
								{Label: "Spanish", Value: "Spanish"},
								{Label: "French", Value: "French"},
								{Label: "German", Value: "German"},
								{Label: "Portuguese", Value: "Portuguese"},
								{Label: "Italian", Value: "Italian"},
								{Label: "Japanese", Value: "Japanese"},
								{Label: "Chinese", Value: "Chinese"},
								{Label: "Korean", Value: "Korean"},
							},
						},
					},
				},
			},
		},
	}); err != nil {
		slog.Error("wizard: send step 3", "error", err)
	}
}

// handleLanguageSelect completes the wizard, persists the agent config, and shows a summary.
func (w *wizardHandler) handleLanguageSelect(s *discordgo.Session, i *discordgo.InteractionCreate, state *wizardState) {
	values := i.MessageComponentData().Values
	if len(values) > 0 && values[0] != "default" {
		state.language = values[0]
	}

	// Acknowledge immediately so the 3-second Discord deadline is met
	// before the config file I/O in UpsertAgent.
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredMessageUpdate,
	}); err != nil {
		slog.Error("wizard: defer final step", "error", err, "guild_id", state.guildID)
		return
	}

	// Build channel overrides. When specific channels were selected, restrict
	// the bot to only those channels by setting the agent mode to "none" and
	// creating per-channel overrides with the user's chosen mode.
	// For "none" mode, selected channels default to "smart".
	var channels []config.ChannelConfig
	agentMode := state.mode
	if len(state.channelIDs) > 0 {
		channelMode := state.mode
		if channelMode == config.ModeNone {
			channelMode = config.ModeSmart
		}
		agentMode = config.ModeNone

		channels = make([]config.ChannelConfig, len(state.channelIDs))
		for idx, chID := range state.channelIDs {
			channels[idx] = config.ChannelConfig{ID: chID, ResponseMode: channelMode}
		}
	}

	agentCfg := config.AgentConfig{
		ID:           state.guildID,
		ServerID:     state.guildID,
		ResponseMode: agentMode,
		Language:     state.language,
		Channels:     channels,
	}

	if err := w.ops.UpsertAgent(agentCfg); err != nil {
		slog.Error("wizard: upsert agent", "error", err, "guild_id", state.guildID)
		editDeferredMessage(s, i, fmt.Sprintf("Setup failed: %v. Run `/init` again.", err))
		return
	}

	// Remove the session now that setup is complete.
	key := wizardKey(state.guildID, state.userID)
	w.mu.Lock()
	delete(w.sessions, key)
	w.mu.Unlock()

	// Build summary.
	modeDisplay := state.mode
	langDisplay := state.language
	if langDisplay == "" {
		langDisplay = "default"
	}

	var channelDisplay string
	if len(state.channelIDs) == 0 {
		channelDisplay = "all channels"
	} else {
		parts := make([]string, len(state.channelIDs))
		for idx, chID := range state.channelIDs {
			parts[idx] = fmt.Sprintf("<#%s>", chID)
		}
		channelDisplay = strings.Join(parts, ", ")
	}

	summary := fmt.Sprintf(
		"✅ **Setup Complete!**\n\nResponse mode: **%s**\nLanguage: **%s**\nChannels: %s\n\nUse `/mode`, `/channel`, `/language` to adjust settings anytime.",
		modeDisplay, langDisplay, channelDisplay,
	)

	editDeferredMessage(s, i, summary)
}

// respondEphemeralUpdate updates the existing ephemeral wizard message with a plain text reply
// and clears all components.
func respondEphemeralUpdate(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseUpdateMessage,
		Data: &discordgo.InteractionResponseData{
			Content:    content,
			Components: []discordgo.MessageComponent{},
		},
	}); err != nil {
		slog.Error("wizard: update message", "error", err)
	}
}

// editDeferredMessage edits a deferred interaction response with plain text and no components.
func editDeferredMessage(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	empty := []discordgo.MessageComponent{}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &empty,
	}); err != nil {
		slog.Error("wizard: edit deferred message", "error", err)
	}
}

// showLanguageEdit edits a deferred interaction response with the language-selection step.
// Used when skipping from showChannelStepDeferred, where the interaction has already been deferred.
func showLanguageEdit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	content := "**Step 3/3 — Language**\nWhat language should I reply in?"
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.SelectMenu{
					CustomID:    "vespra:init:language",
					Placeholder: "Select language...",
					Options: []discordgo.SelectMenuOption{
						{Label: "Default (English)", Value: "default", Description: "No language preference"},
						{Label: "Czech", Value: "Czech"},
						{Label: "Slovak", Value: "Slovak"},
						{Label: "Spanish", Value: "Spanish"},
						{Label: "French", Value: "French"},
						{Label: "German", Value: "German"},
						{Label: "Portuguese", Value: "Portuguese"},
						{Label: "Italian", Value: "Italian"},
						{Label: "Japanese", Value: "Japanese"},
						{Label: "Chinese", Value: "Chinese"},
						{Label: "Korean", Value: "Korean"},
					},
				},
			},
		},
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &components,
	}); err != nil {
		slog.Error("wizard: edit step 3", "error", err)
	}
}
