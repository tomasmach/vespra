package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/memory"
)

var manageGuildPerm = int64(discordgo.PermissionManageServer)

// modechoices are the valid response mode values shared across commands.
var modeChoices = []*discordgo.ApplicationCommandOptionChoice{
	{Name: "smart", Value: "smart"},
	{Name: "mention", Value: "mention"},
	{Name: "all", Value: "all"},
	{Name: "none", Value: "none"},
}

// commandDefinitions is the full set of slash commands registered for each guild.
var commandDefinitions = []*discordgo.ApplicationCommand{
	{
		Name:                     "init",
		Description:              "Set up Vespra for this server",
		DefaultMemberPermissions: &manageGuildPerm,
	},
	{
		Name:                     "mode",
		Description:              "Set the default response mode for this server",
		DefaultMemberPermissions: &manageGuildPerm,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "mode",
				Description: "Response mode",
				Required:    true,
				Choices:     modeChoices,
			},
		},
	},
	{
		Name:                     "channel",
		Description:              "Configure per-channel response mode overrides",
		DefaultMemberPermissions: &manageGuildPerm,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "set",
				Description: "Set the response mode for a specific channel",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:         discordgo.ApplicationCommandOptionChannel,
						Name:         "channel",
						Description:  "The channel to configure",
						Required:     true,
						ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText},
					},
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "mode",
						Description: "Response mode for this channel",
						Required:    true,
						Choices:     modeChoices,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "remove",
				Description: "Remove the response mode override for a specific channel",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:         discordgo.ApplicationCommandOptionChannel,
						Name:         "channel",
						Description:  "The channel to remove the override from",
						Required:     true,
						ChannelTypes: []discordgo.ChannelType{discordgo.ChannelTypeGuildText},
					},
				},
			},
		},
	},
	{
		Name:                     "language",
		Description:              "Set the language preference for this server",
		DefaultMemberPermissions: &manageGuildPerm,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionString,
				Name:        "language",
				Description: "Language name or code (leave empty to clear)",
				Required:    true,
			},
		},
	},
	{
		Name:                     "status",
		Description:              "Show current Vespra configuration for this server",
		DefaultMemberPermissions: &manageGuildPerm,
	},
	{
		Name:                     "memory",
		Description:              "Manage the bot's memories for this server",
		DefaultMemberPermissions: &manageGuildPerm,
		Options: []*discordgo.ApplicationCommandOption{
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "search",
				Description: "Search through stored memories",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "query",
						Description: "Search query",
						Required:    true,
					},
				},
			},
			{
				Type:        discordgo.ApplicationCommandOptionSubCommand,
				Name:        "forget",
				Description: "Delete a specific memory by ID",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Type:        discordgo.ApplicationCommandOptionString,
						Name:        "id",
						Description: "Memory ID to forget",
						Required:    true,
					},
				},
			},
		},
	},
	{
		Name:                     "restart",
		Description:              "Restart the agent, clearing all active channel sessions",
		DefaultMemberPermissions: &manageGuildPerm,
	},
}

// RegisterCommands bulk-overwrites all slash commands for a guild.
func RegisterCommands(s *discordgo.Session, guildID string) {
	_, err := s.ApplicationCommandBulkOverwrite(s.State.User.ID, guildID, commandDefinitions)
	if err != nil {
		slog.Error("register commands", "error", err, "guild_id", guildID)
	}
}

// respondEphemeral sends an ephemeral (only-visible-to-invoker) reply to an interaction.
func respondEphemeral(s *discordgo.Session, i *discordgo.InteractionCreate, content string) {
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: content,
			Flags:   discordgo.MessageFlagsEphemeral,
		},
	})
}

// subcommandData extracts the subcommand name and its options from an interaction.
func subcommandData(i *discordgo.InteractionCreate) (string, []*discordgo.ApplicationCommandInteractionDataOption) {
	data := i.ApplicationCommandData()
	if len(data.Options) > 0 && data.Options[0].Type == discordgo.ApplicationCommandOptionSubCommand {
		return data.Options[0].Name, data.Options[0].Options
	}
	return "", nil
}

// handleSlashCommand dispatches an ApplicationCommand interaction to the correct handler.
func (b *Bot) handleSlashCommand(s *discordgo.Session, i *discordgo.InteractionCreate) {
	switch i.ApplicationCommandData().Name {
	case "init":
		b.handleInit(s, i)
	case "mode":
		b.handleMode(s, i)
	case "channel":
		b.handleChannel(s, i)
	case "language":
		b.handleLanguage(s, i)
	case "status":
		b.handleStatus(s, i)
	case "memory":
		b.handleMemory(s, i)
	case "restart":
		b.handleRestart(s, i)
	}
}

func (b *Bot) handleInit(s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.wizard.startInit(s, i)
}

func (b *Bot) handleMode(s *discordgo.Session, i *discordgo.InteractionCreate) {
	mode := i.ApplicationCommandData().Options[0].StringValue()
	if err := b.ops.UpdateAgentMode(i.GuildID, mode); err != nil {
		respondEphemeral(s, i, fmt.Sprintf("Failed to update mode: %v", err))
		return
	}
	respondEphemeral(s, i, fmt.Sprintf("Response mode updated to **%s**.", mode))
}

func (b *Bot) handleChannel(s *discordgo.Session, i *discordgo.InteractionCreate) {
	sub, opts := subcommandData(i)
	switch sub {
	case "set":
		var channelID, mode string
		for _, opt := range opts {
			switch opt.Name {
			case "channel":
				channelID = opt.ChannelValue(s).ID
			case "mode":
				mode = opt.StringValue()
			}
		}
		if err := b.ops.UpdateAgentChannel(i.GuildID, channelID, mode); err != nil {
			respondEphemeral(s, i, fmt.Sprintf("Failed to set channel mode: %v", err))
			return
		}
		respondEphemeral(s, i, fmt.Sprintf("Channel <#%s> mode set to **%s**.", channelID, mode))
	case "remove":
		var channelID string
		for _, opt := range opts {
			if opt.Name == "channel" {
				channelID = opt.ChannelValue(s).ID
			}
		}
		if err := b.ops.UpdateAgentChannel(i.GuildID, channelID, ""); err != nil {
			respondEphemeral(s, i, fmt.Sprintf("Failed to remove channel override: %v", err))
			return
		}
		respondEphemeral(s, i, fmt.Sprintf("Channel override removed for <#%s>.", channelID))
	}
}

func (b *Bot) handleLanguage(s *discordgo.Session, i *discordgo.InteractionCreate) {
	language := i.ApplicationCommandData().Options[0].StringValue()
	if err := b.ops.UpdateAgentLanguage(i.GuildID, language); err != nil {
		respondEphemeral(s, i, fmt.Sprintf("Failed to update language: %v", err))
		return
	}
	if language == "" {
		respondEphemeral(s, i, "Language preference cleared.")
	} else {
		respondEphemeral(s, i, fmt.Sprintf("Language updated to **%s**.", language))
	}
}

func (b *Bot) handleStatus(s *discordgo.Session, i *discordgo.InteractionCreate) {
	cfg := b.ops.CfgStore().Get()

	var found bool
	for _, a := range cfg.Agents {
		if a.ServerID != i.GuildID {
			continue
		}
		found = true

		mode := a.ResponseMode
		if mode == "" {
			mode = "not configured"
		}
		lang := a.Language
		if lang == "" {
			lang = "default"
		}

		var channelLines string
		if len(a.Channels) == 0 {
			channelLines = "none"
		} else {
			for _, ch := range a.Channels {
				channelLines += fmt.Sprintf("\n  <#%s> (%s)", ch.ID, ch.ResponseMode)
			}
		}

		msg := fmt.Sprintf("**Vespra Configuration**\nResponse mode: %s\nLanguage: %s\nChannels: %s",
			mode, lang, channelLines)
		respondEphemeral(s, i, msg)
		break
	}

	if !found {
		respondEphemeral(s, i, "This server is not configured yet. Run `/init` to get started.")
	}
}

func (b *Bot) handleMemory(s *discordgo.Session, i *discordgo.InteractionCreate) {
	sub, opts := subcommandData(i)
	switch sub {
	case "search":
		var query string
		for _, opt := range opts {
			if opt.Name == "query" {
				query = opt.StringValue()
			}
		}

		mem := b.router.MemoryForServer(i.GuildID)
		if mem == nil {
			respondEphemeral(s, i, "This server has no memory store.")
			return
		}

		rows, total, err := mem.List(context.Background(), memory.ListOptions{
			ServerID: i.GuildID,
			Query:    query,
			Limit:    10,
		})
		if err != nil {
			respondEphemeral(s, i, fmt.Sprintf("Failed to search memories: %v", err))
			return
		}
		if len(rows) == 0 {
			respondEphemeral(s, i, "No memories found for that query.")
			return
		}

		result := fmt.Sprintf("**Found %d memories** (showing %d)\n", total, len(rows))
		for _, row := range rows {
			content := row.Content
			if len(content) > 100 {
				content = content[:100] + "…"
			}
			result += fmt.Sprintf("\n> [%s] %s", row.ID, content)
		}
		respondEphemeral(s, i, result)

	case "forget":
		var id string
		for _, opt := range opts {
			if opt.Name == "id" {
				id = opt.StringValue()
			}
		}

		mem := b.router.MemoryForServer(i.GuildID)
		if mem == nil {
			respondEphemeral(s, i, "This server has no memory store.")
			return
		}

		if err := mem.Forget(context.Background(), i.GuildID, id); err != nil {
			if errors.Is(err, memory.ErrMemoryNotFound) {
				respondEphemeral(s, i, "Memory not found.")
				return
			}
			respondEphemeral(s, i, fmt.Sprintf("Failed to forget memory: %v", err))
			return
		}
		respondEphemeral(s, i, fmt.Sprintf("Memory **%s** forgotten.", id))
	}
}

func (b *Bot) handleRestart(s *discordgo.Session, i *discordgo.InteractionCreate) {
	b.router.RestartAgent(i.GuildID)
	respondEphemeral(s, i, "Agent restarted. All channel sessions cleared.")
}
