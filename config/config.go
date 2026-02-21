// Package config handles TOML configuration loading and path resolution.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Bot      BotConfig
	LLM      LLMConfig
	Memory   MemoryConfig
	Agent    TurnConfig    `toml:"agent"`
	Response ResponseConfig
	Tools    ToolsConfig
	Web      WebConfig
	Agents   []AgentConfig `toml:"agents"`
}

type WebConfig struct {
	Addr string `toml:"addr"` // default ":8080"
}

type BotConfig struct {
	Token    string `toml:"token" json:"-"`
	SoulFile string `toml:"soul_file"`
}

type LLMConfig struct {
	OpenRouterKey         string `toml:"openrouter_key" json:"-"`
	Model                 string `toml:"model"`
	EmbeddingModel        string `toml:"embedding_model"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds"`
	BaseURL               string `toml:"-" json:"-"` // override for tests; not read from TOML
}

type MemoryConfig struct {
	DBPath string `toml:"db_path"`
}

type TurnConfig struct {
	HistoryLimit         int `toml:"history_limit"`
	IdleTimeoutMinutes   int `toml:"idle_timeout_minutes"`
	MaxToolIterations    int `toml:"max_tool_iterations"`
	HistoryBackfillLimit int `toml:"history_backfill_limit"`
}

type ResponseConfig struct {
	DefaultMode string `toml:"default_mode"`
}

type ToolsConfig struct {
	WebSearchKey string `toml:"web_search_key" json:"-"`
}

type AgentConfig struct {
	ID           string          `toml:"id" json:"id"`
	ServerID     string          `toml:"server_id" json:"server_id"`
	Token        string          `toml:"token" json:"-"`
	SoulFile     string          `toml:"soul_file" json:"soul_file,omitempty"`
	DBPath       string          `toml:"db_path" json:"db_path,omitempty"`
	ResponseMode string          `toml:"response_mode" json:"response_mode,omitempty"`
	Channels     []ChannelConfig `toml:"channels" json:"channels,omitempty"`
}

// ResolveDBPath returns the DB path for this agent.
// If db_path is set, it expands and returns it.
// Otherwise derives: dir(defaultDBPath)/agents/<server_id>/memory.db
func (a *AgentConfig) ResolveDBPath(defaultDBPath string) string {
	if a.DBPath != "" {
		return ExpandPath(a.DBPath)
	}
	dir := filepath.Dir(ExpandPath(defaultDBPath))
	return filepath.Join(dir, "agents", a.ServerID, "memory.db")
}

type ChannelConfig struct {
	ID           string `toml:"id" json:"id"`
	ResponseMode string `toml:"response_mode" json:"response_mode,omitempty"`
}

// ExpandPath expands environment variables and ~ in a file path.
func ExpandPath(path string) string {
	path = os.ExpandEnv(path)
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	return path
}

func Load(path string) (*Config, error) {
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	// Env var overrides (take priority over config file)
	if v := os.Getenv("MNEMON_DB_PATH"); v != "" {
		cfg.Memory.DBPath = v
	}

	// Apply defaults
	if cfg.Web.Addr == "" {
		cfg.Web.Addr = ":8080"
	}
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
	if cfg.Agent.HistoryBackfillLimit == 0 {
		cfg.Agent.HistoryBackfillLimit = 50
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
	for _, agent := range cfg.Agents {
		if agent.ServerID == "" {
			return nil, fmt.Errorf("agent %q: server_id is required", agent.ID)
		}
		if agent.ResponseMode != "" && !validModes[agent.ResponseMode] {
			return nil, fmt.Errorf("agent %s response_mode %q is invalid (must be smart, mention, all, or none)", agent.ID, agent.ResponseMode)
		}
		for _, ch := range agent.Channels {
			if ch.ResponseMode != "" && !validModes[ch.ResponseMode] {
				return nil, fmt.Errorf("agent %s channel %s response_mode %q is invalid (must be smart, mention, all, or none)", agent.ID, ch.ID, ch.ResponseMode)
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

type Store struct {
	mu   sync.RWMutex
	cfg  *Config
	path string
}

// NewStoreFromConfig creates a Store from a pre-built Config (for testing).
func NewStoreFromConfig(cfg *Config) *Store {
	return &Store{cfg: cfg}
}

func NewStore(path string) (*Store, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Store{cfg: cfg, path: path}, nil
}

func (s *Store) Get() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Store) Reload() (*Config, error) {
	cfg, err := Load(s.path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return cfg, nil
}

// ResolveResponseMode returns the effective response mode for a server/channel pair.
// Priority: channel-level > agent-level > global default.
func (cfg *Config) ResolveResponseMode(serverID, channelID string) string {
	for _, agent := range cfg.Agents {
		if agent.ServerID != serverID {
			continue
		}
		for _, ch := range agent.Channels {
			if ch.ID == channelID && ch.ResponseMode != "" {
				return ch.ResponseMode
			}
		}
		if agent.ResponseMode != "" {
			return agent.ResponseMode
		}
		break
	}
	return cfg.Response.DefaultMode
}
