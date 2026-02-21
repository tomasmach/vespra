package config

import (
	"testing"
)

func TestResolveResponseMode(t *testing.T) {
	cfg := &Config{
		Response: ResponseConfig{DefaultMode: "smart"},
		Agents: []AgentConfig{
			{
				ServerID:     "server1",
				ResponseMode: "all",
				Channels: []ChannelConfig{
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

func TestResolveResponseModeEmptyChannelOverride(t *testing.T) {
	// Channel entry with no ResponseMode set should fall through to agent-level
	cfg := &Config{
		Response: ResponseConfig{DefaultMode: "smart"},
		Agents: []AgentConfig{
			{
				ServerID:     "srv1",
				ResponseMode: "mention",
				Channels: []ChannelConfig{
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
