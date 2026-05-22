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

// Response mode constants.
const (
	ModeNone    = "none"
	ModeSmart   = "smart"
	ModeMention = "mention"
	ModeAll     = "all"
)

// ValidModes is the set of valid response mode values.
var ValidModes = map[string]bool{
	ModeSmart:   true,
	ModeMention: true,
	ModeAll:     true,
	ModeNone:    true,
}

type Config struct {
	Bot      BotConfig
	LLM      LLMConfig
	Memory   MemoryConfig
	Agent    TurnConfig `toml:"agent"`
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
	FireworksKey          string `toml:"fireworks_key" json:"-"`
	FireworksBaseURL      string `toml:"fireworks_base_url" json:"-"`
	Model                 string `toml:"model"`
	VisionModel           string `toml:"vision_model"`
	VisionBaseURL         string `toml:"vision_base_url" json:"-"`
	EmbeddingModel        string `toml:"embedding_model"`
	RequestTimeoutSeconds int    `toml:"request_timeout_seconds"`
	BaseURL               string `toml:"base_url" json:"-"`
	EmbeddingBaseURL      string `toml:"embedding_base_url" json:"-"`
	MediaDescriptions     *bool  `toml:"media_descriptions"` // nil = enabled when vision_model set
	MaxTokens             int    `toml:"max_tokens"`
}

type MemoryConfig struct {
	DBPath string `toml:"db_path"`
}

type TurnConfig struct {
	HistoryLimit             int     `toml:"history_limit"`
	IdleTimeoutMinutes       int     `toml:"idle_timeout_minutes"`
	MaxToolIterations        int     `toml:"max_tool_iterations"`
	HistoryBackfillLimit     int     `toml:"history_backfill_limit"`
	MemoryExtractionInterval int     `toml:"memory_extraction_interval"` // -1 to disable
	CoalesceDisabled         bool    `toml:"coalesce_disabled"`
	CoalesceDebounceMs       int     `toml:"coalesce_debounce_ms"`
	CoalesceMaxWaitMs        int     `toml:"coalesce_max_wait_ms"`
	MemoryRecallLimit        int     `toml:"memory_recall_limit"`
	MemoryDedupThreshold     float64 `toml:"memory_dedup_threshold"`
	MemoryRecallThreshold    float64 `toml:"memory_recall_threshold"`
	SendRateLimit            int     `toml:"send_rate_limit"`
	SendRateWindowSeconds    int     `toml:"send_rate_window_seconds"`
	MaxReplyParts            int     `toml:"max_reply_parts"`
}

type ResponseConfig struct {
	DefaultMode string `toml:"default_mode"`
}

type ToolsConfig struct {
	WebTimeoutSeconds int          `toml:"web_timeout_seconds"`
	Search            SearchConfig `toml:"search"`
	Image             ImageConfig  `toml:"image"`
	Bash              BashConfig   `toml:"bash"`
}

type ImageConfig struct {
	APIKey              string `toml:"api_key" json:"-"`
	Model               string `toml:"model"`
	EditModel           string `toml:"edit_model"`
	Resolution          string `toml:"resolution"`
	EnableSafetyChecker *bool  `toml:"enable_safety_checker"`
	TimeoutSeconds      int    `toml:"timeout_seconds"`
}

type SearchConfig struct {
	Provider string `toml:"provider"` // "brave" | "glm" (default)
	APIKey   string `toml:"api_key" json:"-"`
	Timeout  int    `toml:"timeout_seconds"` // default 30
}

// BashConfig holds global defaults for the bash_exec tool and runner service.
// The tool is still disabled unless an agent opts in with [agents.bash].
type BashConfig struct {
	RunnerURL            string `toml:"runner_url"`
	RunnerToken          string `toml:"runner_token" json:"-"`
	JobImage             string `toml:"job_image"`
	VolumePrefix         string `toml:"volume_prefix"`
	EgressNetwork        string `toml:"egress_network"`
	TimeoutSeconds       int    `toml:"timeout_seconds"`
	MaxOutputBytes       int    `toml:"max_output_bytes"`
	MaxCommandBytes      int    `toml:"max_command_bytes"`
	GlobalConcurrency    int    `toml:"global_concurrency"`
	PerServerConcurrency int    `toml:"per_server_concurrency"`
	PerUserRateLimit     int    `toml:"per_user_rate_limit"`
	CPUs                 string `toml:"cpus"`
	Memory               string `toml:"memory"`
	PidsLimit            int    `toml:"pids_limit"`
}

type AgentConfig struct {
	ID           string           `toml:"id" json:"id"`
	ServerID     string           `toml:"server_id" json:"server_id"`
	Token        string           `toml:"token" json:"-"`
	SoulFile     string           `toml:"soul_file" json:"soul_file,omitempty"`
	DBPath       string           `toml:"db_path" json:"db_path,omitempty"`
	ResponseMode string           `toml:"response_mode" json:"response_mode,omitempty"`
	Language     string           `toml:"language" json:"language,omitempty"`
	Provider     string           `toml:"provider" json:"provider,omitempty"` // "openrouter" | "glm" | "fireworks" | "" (inherit global)
	Model        string           `toml:"model" json:"model,omitempty"`       // model name override; "" = use global
	IgnoreUsers  []string         `toml:"ignore_users,omitempty" json:"ignore_users,omitempty"`
	Channels     []ChannelConfig  `toml:"channels" json:"channels,omitempty"`
	Image        AgentImageConfig `toml:"image" json:"image,omitempty"`
	Bash         AgentBashConfig  `toml:"bash" json:"bash,omitempty"`
}

// AgentImageConfig holds per-agent image generation overrides.
// Non-zero fields override the global [tools.image] settings.
type AgentImageConfig struct {
	APIKey              string `toml:"api_key" json:"-"`
	Model               string `toml:"model" json:"model,omitempty"`
	EditModel           string `toml:"edit_model" json:"edit_model,omitempty"`
	Resolution          string `toml:"resolution" json:"resolution,omitempty"`
	EnableSafetyChecker *bool  `toml:"enable_safety_checker" json:"enable_safety_checker,omitempty"`
}

// AgentBashConfig enables bash access for a single Discord server and lets that
// server choose stricter limits than the global [tools.bash] defaults.
type AgentBashConfig struct {
	Enabled        bool `toml:"enabled" json:"enabled,omitempty"`
	TimeoutSeconds int  `toml:"timeout_seconds" json:"timeout_seconds,omitempty"`
	MaxOutputBytes int  `toml:"max_output_bytes" json:"max_output_bytes,omitempty"`
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
	if v := os.Getenv("FAL_API_KEY"); v != "" {
		cfg.Tools.Image.APIKey = v
		slog.Info("fal api key overridden by env var", "FAL_API_KEY", "***")
	}
	if v := os.Getenv("BRAVE_API_KEY"); v != "" {
		cfg.Tools.Search.APIKey = v
		slog.Info("brave api key overridden by env var", "BRAVE_API_KEY", "***")
		if cfg.Tools.Search.Provider == "" || cfg.Tools.Search.Provider == "glm" {
			cfg.Tools.Search.Provider = "brave"
		}
	}
	if v := os.Getenv("VESPRA_BASH_RUNNER_URL"); v != "" {
		cfg.Tools.Bash.RunnerURL = v
		slog.Info("bash runner URL overridden by env var", "VESPRA_BASH_RUNNER_URL", v)
	}
	if v := os.Getenv("VESPRA_BASH_RUNNER_TOKEN"); v != "" {
		cfg.Tools.Bash.RunnerToken = v
		slog.Info("bash runner token overridden by env var", "VESPRA_BASH_RUNNER_TOKEN", "***")
	}

	// Apply defaults
	if cfg.Web.Addr == "" {
		cfg.Web.Addr = ":8080"
	}
	if cfg.LLM.GLMBaseURL == "" {
		cfg.LLM.GLMBaseURL = "https://open.bigmodel.cn/api/paas/v4"
	}
	if cfg.LLM.FireworksBaseURL == "" {
		cfg.LLM.FireworksBaseURL = "https://api.fireworks.ai/inference/v1"
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
	if cfg.Agent.SendRateLimit <= 0 {
		cfg.Agent.SendRateLimit = 4
	}
	if cfg.Agent.SendRateWindowSeconds <= 0 {
		cfg.Agent.SendRateWindowSeconds = 60
	}
	if cfg.Agent.MaxReplyParts <= 0 {
		cfg.Agent.MaxReplyParts = 2
	}
	if cfg.LLM.MaxTokens <= 0 {
		cfg.LLM.MaxTokens = 1024
	}
	if cfg.Tools.WebTimeoutSeconds <= 0 {
		cfg.Tools.WebTimeoutSeconds = 120
	}
	if cfg.Tools.Search.Provider == "" {
		cfg.Tools.Search.Provider = "glm"
	}
	if cfg.Tools.Image.Model == "" {
		cfg.Tools.Image.Model = "fal-ai/flux/schnell"
	}
	if cfg.Tools.Image.EditModel == "" {
		cfg.Tools.Image.EditModel = "fal-ai/nano-banana-2/edit"
	}
	if cfg.Tools.Image.Resolution == "" {
		cfg.Tools.Image.Resolution = "1K"
	}
	if cfg.Tools.Image.TimeoutSeconds <= 0 {
		cfg.Tools.Image.TimeoutSeconds = 120
	}
	applyBashDefaults(&cfg.Tools.Bash)
	if cfg.Response.DefaultMode == "" {
		cfg.Response.DefaultMode = ModeSmart
	}

	// Validate required fields
	if cfg.Bot.Token == "" {
		return nil, fmt.Errorf("bot.token is required")
	}
	if cfg.LLM.OpenRouterKey == "" && cfg.LLM.GLMKey == "" && cfg.LLM.FireworksKey == "" {
		return nil, fmt.Errorf("llm.openrouter_key, llm.glm_key, or llm.fireworks_key is required")
	}

	// Validate response mode values
	if !ValidModes[cfg.Response.DefaultMode] {
		return nil, fmt.Errorf("response.default_mode %q is invalid (must be smart, mention, all, or none)", cfg.Response.DefaultMode)
	}
	if err := validateBashConfig(cfg.Tools.Bash); err != nil {
		return nil, err
	}
	validProviders := map[string]bool{"openrouter": true, "glm": true, "fireworks": true}
	for _, agent := range cfg.Agents {
		if agent.ServerID == "" {
			return nil, fmt.Errorf("agent %q: server_id is required", agent.ID)
		}
		if agent.ResponseMode != "" && !ValidModes[agent.ResponseMode] {
			return nil, fmt.Errorf("agent %s response_mode %q is invalid (must be smart, mention, all, or none)", agent.ID, agent.ResponseMode)
		}
		if agent.Provider != "" && !validProviders[agent.Provider] {
			return nil, fmt.Errorf("agent %s provider %q is invalid (must be openrouter, glm, or fireworks)", agent.ID, agent.Provider)
		}
		if agent.Provider == "glm" && cfg.LLM.GLMKey == "" {
			return nil, fmt.Errorf("agent %s uses provider %q but llm.glm_key is not configured", agent.ID, agent.Provider)
		}
		if agent.Provider == "fireworks" && cfg.LLM.FireworksKey == "" {
			return nil, fmt.Errorf("agent %s uses provider %q but llm.fireworks_key is not configured", agent.ID, agent.Provider)
		}
		if agent.Bash.Enabled {
			if strings.HasPrefix(agent.ServerID, "DM:") {
				return nil, fmt.Errorf("agent %s enables bash for a DM server_id; bash is only available for Discord guilds", agent.ID)
			}
			if cfg.Tools.Bash.RunnerURL == "" {
				return nil, fmt.Errorf("agent %s enables bash but tools.bash.runner_url is not configured", agent.ID)
			}
			if cfg.Tools.Bash.RunnerToken == "" {
				return nil, fmt.Errorf("agent %s enables bash but tools.bash.runner_token is not configured", agent.ID)
			}
			if agent.Bash.TimeoutSeconds < 0 {
				return nil, fmt.Errorf("agent %s bash.timeout_seconds (%d) must not be negative", agent.ID, agent.Bash.TimeoutSeconds)
			}
			if agent.Bash.MaxOutputBytes < 0 {
				return nil, fmt.Errorf("agent %s bash.max_output_bytes (%d) must not be negative", agent.ID, agent.Bash.MaxOutputBytes)
			}
			if agent.Bash.TimeoutSeconds > cfg.Tools.Bash.TimeoutSeconds {
				return nil, fmt.Errorf("agent %s bash.timeout_seconds (%d) must not exceed tools.bash.timeout_seconds (%d)", agent.ID, agent.Bash.TimeoutSeconds, cfg.Tools.Bash.TimeoutSeconds)
			}
			if agent.Bash.MaxOutputBytes > cfg.Tools.Bash.MaxOutputBytes {
				return nil, fmt.Errorf("agent %s bash.max_output_bytes (%d) must not exceed tools.bash.max_output_bytes (%d)", agent.ID, agent.Bash.MaxOutputBytes, cfg.Tools.Bash.MaxOutputBytes)
			}
		}
		for _, ch := range agent.Channels {
			if ch.ResponseMode != "" && !ValidModes[ch.ResponseMode] {
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

func applyBashDefaults(cfg *BashConfig) {
	if cfg.JobImage == "" {
		cfg.JobImage = "vespra-bash-job:latest"
	}
	if cfg.VolumePrefix == "" {
		cfg.VolumePrefix = "vespra-bash-workspace"
	}
	if cfg.EgressNetwork == "" {
		cfg.EgressNetwork = "vespra-bash-egress"
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 32 * 1024
	}
	if cfg.MaxCommandBytes <= 0 {
		cfg.MaxCommandBytes = 8192
	}
	if cfg.GlobalConcurrency <= 0 {
		cfg.GlobalConcurrency = 4
	}
	if cfg.PerServerConcurrency <= 0 {
		cfg.PerServerConcurrency = 1
	}
	if cfg.PerUserRateLimit <= 0 {
		cfg.PerUserRateLimit = 10
	}
	if cfg.CPUs == "" {
		cfg.CPUs = "0.5"
	}
	if cfg.Memory == "" {
		cfg.Memory = "256m"
	}
	if cfg.PidsLimit <= 0 {
		cfg.PidsLimit = 128
	}
}

func validateBashConfig(cfg BashConfig) error {
	if cfg.RunnerURL != "" && cfg.RunnerToken == "" {
		return fmt.Errorf("tools.bash.runner_token is required when runner_url is configured")
	}
	if strings.ContainsAny(cfg.CPUs, " \t\r\n") {
		return fmt.Errorf("tools.bash.cpus must not contain whitespace")
	}
	if strings.ContainsAny(cfg.Memory, " \t\r\n") {
		return fmt.Errorf("tools.bash.memory must not contain whitespace")
	}
	if cfg.EgressNetwork == "host" {
		return fmt.Errorf("tools.bash.egress_network must not be host")
	}
	if cfg.TimeoutSeconds <= 0 {
		return fmt.Errorf("tools.bash.timeout_seconds must be positive")
	}
	if cfg.MaxOutputBytes <= 0 {
		return fmt.Errorf("tools.bash.max_output_bytes must be positive")
	}
	if cfg.MaxCommandBytes <= 0 {
		return fmt.Errorf("tools.bash.max_command_bytes must be positive")
	}
	return nil
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
