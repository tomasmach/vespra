// Package config handles TOML configuration loading and path resolution.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Bot      BotConfig
	LLM      LLMConfig
	Memory   MemoryConfig
	Agent    AgentConfig
	Response ResponseConfig
	Tools    ToolsConfig
	Servers  []ServerConfig
}

type BotConfig struct {
	Token    string `toml:"token"`
	SoulFile string `toml:"soul_file"`
}

type LLMConfig struct {
	OpenRouterKey         string `toml:"openrouter_key"`
	Model                 string `toml:"model"`
	EmbeddingModel        string `toml:"embedding_model"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds"`
}

type MemoryConfig struct {
	DBPath string `toml:"db_path"`
}

type AgentConfig struct {
	HistoryLimit       int `toml:"history_limit"`
	IdleTimeoutMinutes int `toml:"idle_timeout_minutes"`
	MaxToolIterations  int `toml:"max_tool_iterations"`
}

type ResponseConfig struct {
	DefaultMode string `toml:"default_mode"`
}

type ToolsConfig struct {
	WebSearchKey string `toml:"web_search_key"`
}

type ServerConfig struct {
	ID           string          `toml:"id"`
	SoulFile     string          `toml:"soul_file"`
	ResponseMode string          `toml:"response_mode"`
	Channels     []ChannelConfig `toml:"channels"`
}

type ChannelConfig struct {
	ID           string `toml:"id"`
	ResponseMode string `toml:"response_mode"`
}

func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	// Apply defaults
	if cfg.LLM.RequestTimeoutSeconds == 0 {
		cfg.LLM.RequestTimeoutSeconds = 60
	}
	if cfg.Agent.HistoryLimit == 0 {
		cfg.Agent.HistoryLimit = 20
	}
	if cfg.Agent.IdleTimeoutMinutes == 0 {
		cfg.Agent.IdleTimeoutMinutes = 10
	}
	if cfg.Agent.MaxToolIterations == 0 {
		cfg.Agent.MaxToolIterations = 10
	}
	if cfg.Response.DefaultMode == "" {
		cfg.Response.DefaultMode = "smart"
	}

	// Validate required fields
	if cfg.Bot.Token == "" {
		return nil, fmt.Errorf("bot.token is required")
	}
	if cfg.LLM.OpenRouterKey == "" {
		return nil, fmt.Errorf("llm.openrouter_key is required")
	}

	// Validate response mode values
	validModes := map[string]bool{"smart": true, "mention": true, "all": true, "none": true}
	if !validModes[cfg.Response.DefaultMode] {
		return nil, fmt.Errorf("response.default_mode %q is invalid (must be smart, mention, all, or none)", cfg.Response.DefaultMode)
	}
	for _, server := range cfg.Servers {
		if server.ResponseMode != "" && !validModes[server.ResponseMode] {
			return nil, fmt.Errorf("server %s response_mode %q is invalid (must be smart, mention, all, or none)", server.ID, server.ResponseMode)
		}
		for _, ch := range server.Channels {
			if ch.ResponseMode != "" && !validModes[ch.ResponseMode] {
				return nil, fmt.Errorf("server %s channel %s response_mode %q is invalid (must be smart, mention, all, or none)", server.ID, ch.ID, ch.ResponseMode)
			}
		}
	}

	return &cfg, nil
}

// Resolve returns the config file path from MNEMON_CONFIG env var,
// falling back to ~/.config/mnemon-bot/config.toml.
// The --config CLI flag is handled separately in main.go.
func Resolve() string {
	path := os.Getenv("MNEMON_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "mnemon-bot", "config.toml")
	}
	path = os.ExpandEnv(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

// ResolveResponseMode returns the effective response mode for a server/channel pair.
// Priority: channel-level > server-level > global default.
func (cfg *Config) ResolveResponseMode(serverID, channelID string) string {
	for _, server := range cfg.Servers {
		if server.ID != serverID {
			continue
		}
		for _, ch := range server.Channels {
			if ch.ID == channelID && ch.ResponseMode != "" {
				return ch.ResponseMode
			}
		}
		if server.ResponseMode != "" {
			return server.ResponseMode
		}
		break
	}
	return cfg.Response.DefaultMode
}
