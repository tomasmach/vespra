// Package config handles TOML configuration loading and path resolution.
package config

import (
	"fmt"
	"log/slog"
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
	GLMKey                string `toml:"glm_key" json:"-"`
	GLMBaseURL            string `toml:"glm_base_url" json:"-"`
	Model                 string `toml:"model"`
	VisionModel           string `toml:"vision_model"`
	VisionBaseURL         string `toml:"vision_base_url" json:"-"`
	EmbeddingModel        string `toml:"embedding_model"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds"`
	BaseURL               string `toml:"base_url" json:"-"`
	EmbeddingBaseURL      string `toml:"embedding_base_url" json:"-"`
}

type MemoryConfig struct {
	DBPath string `toml:"db_path"`
}

type TurnConfig struct {
	HistoryLimit             int  `toml:"history_limit"`
	IdleTimeoutMinutes       int  `toml:"idle_timeout_minutes"`
	MaxToolIterations        int  `toml:"max_tool_iterations"`
	HistoryBackfillLimit     int  `toml:"history_backfill_limit"`
	MemoryExtractionInterval int  `toml:"memory_extraction_interval"` // -1 to disable
	CoalesceDisabled         bool    `toml:"coalesce_disabled"`
	CoalesceDebounceMs       int     `toml:"coalesce_debounce_ms"`
	CoalesceMaxWaitMs        int     `toml:"coalesce_max_wait_ms"`
	MemoryRecallLimit        int     `toml:"memory_recall_limit"`
	MemoryDedupThreshold     float64 `toml:"memory_dedup_threshold"`
	MemoryRecallThreshold    float64 `toml:"memory_recall_threshold"`
}

type ResponseConfig struct {
	DefaultMode string `toml:"default_mode"`
}

type ToolsConfig struct {
	WebTimeoutSeconds int         `toml:"web_timeout_seconds"`
	Search            SearchConfig `toml:"search"`
}

type SearchConfig struct {
	Provider string `toml:"provider"` // "brave" | "glm" (default)
	APIKey   string `toml:"api_key" json:"-"`
	Timeout  int    `toml:"timeout_seconds"` // default 30
}

type AgentConfig struct {
	ID           string          `toml:"id" json:"id"`
	ServerID     string          `toml:"server_id" json:"server_id"`
	Token        string          `toml:"token" json:"-"`
	SoulFile     string          `toml:"soul_file" json:"soul_file,omitempty"`
	DBPath       string          `toml:"db_path" json:"db_path,omitempty"`
	ResponseMode string          `toml:"response_mode" json:"response_mode,omitempty"`
	Language     string          `toml:"language" json:"language,omitempty"`
	Provider     string          `toml:"provider" json:"provider,omitempty"` // "openrouter" | "glm" | "" (inherit global)
	Model        string          `toml:"model" json:"model,omitempty"`       // model name override; "" = use global
	IgnoreUsers  []string        `toml:"ignore_users,omitempty" json:"ignore_users,omitempty"`
	Channels     []ChannelConfig `toml:"channels" json:"channels,omitempty"`
}

// ResolveDBPath returns the DB path for this agent.
// If db_path is set, it expands and returns it.
// Otherwise derives: ResolveDataDir(defaultDBPath)/agents/<server_id>/memory.db
func (a *AgentConfig) ResolveDBPath(defaultDBPath string) string {
	if a.DBPath != "" {
		return ExpandPath(a.DBPath)
	}
	return filepath.Join(ResolveDataDir(defaultDBPath), "agents", a.ServerID, "memory.db")
}

type ChannelConfig struct {
	ID           string `toml:"id" json:"id"`
	ResponseMode string `toml:"response_mode" json:"response_mode,omitempty"`
}

// ResolveDataDir returns the directory that should contain all DB files.
// If dbPath is set, it returns the directory of that file.
// Otherwise it returns ~/.local/share/vespra.
func ResolveDataDir(dbPath string) string {
	if dbPath != "" {
		return filepath.Dir(ExpandPath(dbPath))
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "vespra")
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

	// Env var overrides applied after TOML decode; priority: env var > config file.
	if v := os.Getenv("VESPRA_DB_PATH"); v != "" {
		cfg.Memory.DBPath = v
		slog.Info("db path overridden by env var", "VESPRA_DB_PATH", v)
	}
	if v := os.Getenv("BRAVE_API_KEY"); v != "" {
		cfg.Tools.Search.APIKey = v
		slog.Info("brave api key overridden by env var", "BRAVE_API_KEY", "***")
		if cfg.Tools.Search.Provider == "" || cfg.Tools.Search.Provider == "glm" {
			cfg.Tools.Search.Provider = "brave"
		}
	}

	// Apply defaults
	if cfg.Web.Addr == "" {
		cfg.Web.Addr = ":8080"
	}
	if cfg.LLM.GLMBaseURL == "" {
		cfg.LLM.GLMBaseURL = "https://open.bigmodel.cn/api/paas/v4"
	}
	if cfg.LLM.RequestTimeoutSeconds == 0 {
		cfg.LLM.RequestTimeoutSeconds = 60
	}
	if cfg.Agent.HistoryLimit <= 0 {
		cfg.Agent.HistoryLimit = 20
	}
	if cfg.Agent.IdleTimeoutMinutes <= 0 {
		cfg.Agent.IdleTimeoutMinutes = 10
	}
	if cfg.Agent.MaxToolIterations <= 0 {
		cfg.Agent.MaxToolIterations = 10
	}
	if cfg.Agent.HistoryBackfillLimit <= 0 {
		cfg.Agent.HistoryBackfillLimit = 50
	}
	if cfg.Agent.MemoryExtractionInterval == 0 {
		cfg.Agent.MemoryExtractionInterval = 5
	}
	if cfg.Agent.CoalesceDebounceMs == 0 {
		cfg.Agent.CoalesceDebounceMs = 1500
	}
	if cfg.Agent.CoalesceMaxWaitMs == 0 {
		cfg.Agent.CoalesceMaxWaitMs = 5000
	}
	if cfg.Agent.MemoryRecallLimit <= 0 {
		cfg.Agent.MemoryRecallLimit = 15
	}
	if cfg.Agent.MemoryDedupThreshold <= 0 {
		cfg.Agent.MemoryDedupThreshold = 0.85
	}
	if cfg.Agent.MemoryRecallThreshold <= 0 {
		cfg.Agent.MemoryRecallThreshold = 0.35
	}
	if cfg.Tools.WebTimeoutSeconds <= 0 {
		cfg.Tools.WebTimeoutSeconds = 120
	}
	if cfg.Tools.Search.Provider == "" {
		cfg.Tools.Search.Provider = "glm"
	}
	if cfg.Response.DefaultMode == "" {
		cfg.Response.DefaultMode = "smart"
	}

	// Validate required fields
	if cfg.Bot.Token == "" {
		return nil, fmt.Errorf("bot.token is required")
	}
	if cfg.LLM.OpenRouterKey == "" && cfg.LLM.GLMKey == "" {
		return nil, fmt.Errorf("llm.openrouter_key or llm.glm_key is required")
	}

	// Validate response mode values
	validModes := map[string]bool{"smart": true, "mention": true, "all": true, "none": true}
	if !validModes[cfg.Response.DefaultMode] {
		return nil, fmt.Errorf("response.default_mode %q is invalid (must be smart, mention, all, or none)", cfg.Response.DefaultMode)
	}
	validProviders := map[string]bool{"openrouter": true, "glm": true}
	for _, agent := range cfg.Agents {
		if agent.ServerID == "" {
			return nil, fmt.Errorf("agent %q: server_id is required", agent.ID)
		}
		if agent.ResponseMode != "" && !validModes[agent.ResponseMode] {
			return nil, fmt.Errorf("agent %s response_mode %q is invalid (must be smart, mention, all, or none)", agent.ID, agent.ResponseMode)
		}
		if agent.Provider != "" && !validProviders[agent.Provider] {
			return nil, fmt.Errorf("agent %s provider %q is invalid (must be openrouter or glm)", agent.ID, agent.Provider)
		}
		if agent.Provider == "glm" && cfg.LLM.GLMKey == "" {
			return nil, fmt.Errorf("agent %s uses provider %q but llm.glm_key is not configured", agent.ID, agent.Provider)
		}
		for _, ch := range agent.Channels {
			if ch.ResponseMode != "" && !validModes[ch.ResponseMode] {
				return nil, fmt.Errorf("agent %s channel %s response_mode %q is invalid (must be smart, mention, all, or none)", agent.ID, ch.ID, ch.ResponseMode)
			}
		}
	}

	if !cfg.Agent.CoalesceDisabled {
		if cfg.Agent.CoalesceDebounceMs < 0 {
			return nil, fmt.Errorf("agent.coalesce_debounce_ms (%d) must not be negative", cfg.Agent.CoalesceDebounceMs)
		}
		if cfg.Agent.CoalesceMaxWaitMs < 0 {
			return nil, fmt.Errorf("agent.coalesce_max_wait_ms (%d) must not be negative", cfg.Agent.CoalesceMaxWaitMs)
		}
		if cfg.Agent.CoalesceDebounceMs > cfg.Agent.CoalesceMaxWaitMs {
			return nil, fmt.Errorf("agent.coalesce_debounce_ms (%d) must not exceed coalesce_max_wait_ms (%d)",
				cfg.Agent.CoalesceDebounceMs, cfg.Agent.CoalesceMaxWaitMs)
		}
	}

	return &cfg, nil
}

// Resolve returns the config file path from VESPRA_CONFIG env var,
// falling back to ~/.config/vespra/config.toml.
// The --config CLI flag is handled separately in main.go.
func Resolve() string {
	path := os.Getenv("VESPRA_CONFIG")
	if path == "" {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".config", "vespra", "config.toml")
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

// ResolveLanguage returns the configured language for a server.
// Priority: agent-level > "" (no language override).
func (cfg *Config) ResolveLanguage(serverID, channelID string) string {
	for _, agent := range cfg.Agents {
		if agent.ServerID != serverID {
			continue
		}
		return agent.Language
	}
	return ""
}
