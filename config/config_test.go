package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tomasmach/mnemon-bot/config"
)

func TestResolveResponseMode(t *testing.T) {
	cfg := &config.Config{
		Response: config.ResponseConfig{DefaultMode: "smart"},
		Agents: []config.AgentConfig{
			{
				ServerID:     "server1",
				ResponseMode: "all",
				Channels: []config.ChannelConfig{
					{ID: "chan1", ResponseMode: "none"},
				},
			},
		},
	}

	tests := []struct {
		name      string
		serverID  string
		channelID string
		want      string
	}{
		{"channel override wins", "server1", "chan1", "none"},
		{"agent-level default", "server1", "chan2", "all"},
		{"global default for unknown server", "server2", "chan3", "smart"},
		{"global default when no agent config", "", "chan4", "smart"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ResolveResponseMode(tt.serverID, tt.channelID)
			if got != tt.want {
				t.Errorf("ResolveResponseMode(%q, %q) = %q, want %q",
					tt.serverID, tt.channelID, got, tt.want)
			}
		})
	}
}

func TestLoadMNEMON_DB_PATHEnvOverride(t *testing.T) {
	const minimalTOML = `
[bot]
token = "test-token"

[llm]
openrouter_key = "test-key"

[memory]
db_path = "/original/path/memory.db"
`
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte(minimalTOML), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	wantDBPath := "/override/path/memory.db"
	t.Setenv("MNEMON_DB_PATH", wantDBPath)

	cfg, err := config.Load(cfgFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Memory.DBPath != wantDBPath {
		t.Errorf("Memory.DBPath = %q, want %q (MNEMON_DB_PATH override not applied)", cfg.Memory.DBPath, wantDBPath)
	}
}

func TestResolveLanguage(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.AgentConfig{
			{
				ServerID: "server1",
				Language: "Czech",
			},
			{
				ServerID: "server2",
			},
		},
	}

	tests := []struct {
		name      string
		serverID  string
		channelID string
		want      string
	}{
		{"agent-level language", "server1", "chan1", "Czech"},
		{"unknown server returns empty", "server3", "chan1", ""},
		{"agent with no language returns empty", "server2", "chan1", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ResolveLanguage(tt.serverID, tt.channelID)
			if got != tt.want {
				t.Errorf("ResolveLanguage(%q, %q) = %q, want %q",
					tt.serverID, tt.channelID, got, tt.want)
			}
		})
	}
}

func TestResolveResponseModeEmptyChannelOverride(t *testing.T) {
	// Channel entry with no ResponseMode set should fall through to agent-level
	cfg := &config.Config{
		Response: config.ResponseConfig{DefaultMode: "smart"},
		Agents: []config.AgentConfig{
			{
				ServerID:     "srv1",
				ResponseMode: "mention",
				Channels: []config.ChannelConfig{
					{ID: "chan1", ResponseMode: ""},
				},
			},
		},
	}
	got := cfg.ResolveResponseMode("srv1", "chan1")
	if got != "mention" {
		t.Errorf("expected agent-level 'mention' when channel override is empty, got %q", got)
	}
}
