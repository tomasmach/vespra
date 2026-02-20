// Package soul loads the personality system prompt for the bot.
package soul

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tomasmach/mnemon-bot/config"
)

const defaultSoul = `You are Mnemon, a thoughtful and curious AI companion on this Discord server.
You remember everything people tell you and bring it up naturally in conversation.
You are warm but not sycophantic. You never pretend to know things you don't.

You have access to memory tools to save and recall information. Use them proactively.`

// Load returns the soul/system prompt for the given server.
// Resolution order:
// 1. Server-specific soul file (from cfg.Servers match on serverID)
// 2. Global soul file (cfg.Bot.SoulFile)
// 3. Built-in default constant
func Load(cfg *config.Config, serverID string) string {
	// 1. Server-specific soul file
	for _, s := range cfg.Servers {
		if s.ID == serverID && s.SoulFile != "" {
			if content := readFile(s.SoulFile); content != "" {
				return content
			}
			break
		}
	}

	// 2. Global soul file
	if cfg.Bot.SoulFile != "" {
		if content := readFile(cfg.Bot.SoulFile); content != "" {
			return content
		}
	}

	// 3. Built-in default
	return defaultSoul
}

func expandPath(path string) string {
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	return path
}

// readFile expands env vars and ~, then reads the file.
// Returns empty string on any error.
func readFile(path string) string {
	data, err := os.ReadFile(expandPath(path))
	if err != nil {
		return ""
	}
	return string(data)
}
