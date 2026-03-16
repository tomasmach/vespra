package web

import (
	"fmt"
	"slices"

	"github.com/tomasmach/vespra/config"
)

// UpsertAgent creates a new agent config entry for the given server.
// Returns an error if an agent with the same ID or server_id already exists.
// If input.ResponseMode is empty, it defaults to "none" (explicit channel allowlisting).
func (s *Server) UpsertAgent(input config.AgentConfig) error {
	if input.ID == "" {
		return fmt.Errorf("id is required")
	}
	if !validAgentID(input.ID) {
		return fmt.Errorf("invalid agent id: must not contain slashes or null bytes")
	}
	if input.ServerID == "" {
		return fmt.Errorf("server_id is required")
	}
	if input.ResponseMode == "" {
		input.ResponseMode = "none"
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	if findAgentIndex(cfg.Agents, input.ID) != -1 {
		return fmt.Errorf("agent id already exists")
	}
	for _, a := range cfg.Agents {
		if a.ServerID == input.ServerID {
			return fmt.Errorf("server_id already configured")
		}
	}

	newAgents := append(slices.Clone(cfg.Agents), input)
	return s.writeAgents(newAgents)
}

// UpdateAgentMode updates the response_mode for the agent matching serverID.
// Creates a new agent entry if none exists for the server (auto-upsert).
func (s *Server) UpdateAgentMode(serverID, mode string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	idx := findAgentByServerID(cfg.Agents, serverID)
	if idx == -1 {
		// Auto-create agent entry.
		input := config.AgentConfig{
			ID:           serverID,
			ServerID:     serverID,
			ResponseMode: mode,
		}
		newAgents := append(slices.Clone(cfg.Agents), input)
		return s.writeAgents(newAgents)
	}

	newAgents := slices.Clone(cfg.Agents)
	newAgents[idx].ResponseMode = mode
	if err := s.writeAgents(newAgents); err != nil {
		return err
	}
	s.router.UnloadAgent(serverID)
	return nil
}

// UpdateAgentChannel adds or removes a per-channel response mode override.
// If mode is empty, the channel override is removed. Otherwise it is set.
// Creates a new agent entry if none exists for the server (auto-upsert).
func (s *Server) UpdateAgentChannel(serverID, channelID, mode string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	idx := findAgentByServerID(cfg.Agents, serverID)
	if idx == -1 {
		if mode == "" {
			return fmt.Errorf("no agent configured for this server")
		}
		// Auto-create agent entry with channel override.
		input := config.AgentConfig{
			ID:           serverID,
			ServerID:     serverID,
			ResponseMode: "none",
			Channels:     []config.ChannelConfig{{ID: channelID, ResponseMode: mode}},
		}
		newAgents := append(slices.Clone(cfg.Agents), input)
		return s.writeAgents(newAgents)
	}

	newAgents := slices.Clone(cfg.Agents)
	channels := slices.Clone(newAgents[idx].Channels)

	if mode == "" {
		// Remove the channel override.
		channels = slices.DeleteFunc(channels, func(c config.ChannelConfig) bool {
			return c.ID == channelID
		})
	} else {
		// Update or add the channel override.
		found := false
		for i, c := range channels {
			if c.ID == channelID {
				channels[i].ResponseMode = mode
				found = true
				break
			}
		}
		if !found {
			channels = append(channels, config.ChannelConfig{ID: channelID, ResponseMode: mode})
		}
	}

	newAgents[idx].Channels = channels
	if err := s.writeAgents(newAgents); err != nil {
		return err
	}
	s.router.UnloadAgent(serverID)
	return nil
}

// UpdateAgentLanguage updates the language for the agent matching serverID.
// Creates a new agent entry if none exists for the server (auto-upsert).
func (s *Server) UpdateAgentLanguage(serverID, language string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	idx := findAgentByServerID(cfg.Agents, serverID)
	if idx == -1 {
		// Auto-create agent entry.
		input := config.AgentConfig{
			ID:           serverID,
			ServerID:     serverID,
			ResponseMode: "none",
			Language:     language,
		}
		newAgents := append(slices.Clone(cfg.Agents), input)
		return s.writeAgents(newAgents)
	}

	newAgents := slices.Clone(cfg.Agents)
	newAgents[idx].Language = language
	if err := s.writeAgents(newAgents); err != nil {
		return err
	}
	s.router.UnloadAgent(serverID)
	return nil
}

// findAgentByServerID returns the index of the agent with the given server_id, or -1 if not found.
func findAgentByServerID(agents []config.AgentConfig, serverID string) int {
	for i, a := range agents {
		if a.ServerID == serverID {
			return i
		}
	}
	return -1
}

// CfgStore returns the config store (used by bot slash commands for read-only access).
func (s *Server) CfgStore() *config.Store {
	return s.cfgStore
}
