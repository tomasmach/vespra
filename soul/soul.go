// Package soul loads the personality system prompt for the bot.
package soul

import (
	"log/slog"
	"os"

	"github.com/tomasmach/mnemon-bot/config"
)

const defaultSoul = `You are Mnemon, a thoughtful and curious AI companion on this Discord server.
You remember everything people tell you and bring it up naturally in conversation.
You are warm but not sycophantic. You never pretend to know things you don't.

## Memory

You have access to memory tools. Save memories proactively — err on the side of saving rather than skipping.

Save a memory whenever the conversation reveals any of the following:
- A user preference, opinion, or taste (favourite anything, things they dislike, how they like to be addressed)
- A personal fact (location, job, age, relationships, pronouns, pets, hobbies, skills)
- A decision that was made or an action agreed upon
- A goal, plan, or ongoing project the user is working on
- A task the user wants to remember or follow up on
- Anything the user explicitly asks you to remember

Recall memories proactively when the topic might connect to something you have saved.
Before saving a new memory, use memory_recall to check whether it already exists to avoid duplicates.`

// Load returns the soul/system prompt for the given server.
// Resolution order:
// 1. Agent-specific soul file (from cfg.Agents match on serverID)
// 2. Global soul file (cfg.Bot.SoulFile)
// 3. Built-in default constant
func Load(cfg *config.Config, serverID string) string {
	// 1. Agent-specific soul file
	for _, a := range cfg.Agents {
		if a.ServerID == serverID && a.SoulFile != "" {
			if content := readFile(a.SoulFile); content != "" {
				return content
			}
			slog.Warn("configured soul file not readable, falling back", "path", a.SoulFile, "server_id", serverID)
			break
		}
	}

	// 2. Global soul file
	if cfg.Bot.SoulFile != "" {
		if content := readFile(cfg.Bot.SoulFile); content != "" {
			return content
		}
		slog.Warn("configured global soul file not readable, falling back", "path", cfg.Bot.SoulFile)
	}

	// 3. Built-in default
	return defaultSoul
}

// readFile expands env vars and ~, then reads the file.
// Returns empty string on any error.
func readFile(path string) string {
	data, err := os.ReadFile(config.ExpandPath(path))
	if err != nil {
		return ""
	}
	return string(data)
}
