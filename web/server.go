package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/tomasmach/mnemon-bot/agent"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/memory"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	cfgStore   *config.Store
	cfgPath    string
	router     *agent.Router
	sseSubs    []chan string
	ssesMu     sync.Mutex
	httpServer *http.Server
}

func New(addr string, cfgStore *config.Store, cfgPath string, router *agent.Router) *Server {
	s := &Server{
		cfgStore: cfgStore,
		cfgPath:  cfgPath,
		router:   router,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("POST /api/config", s.handlePostConfig)
	mux.HandleFunc("GET /api/memories", s.handleListMemories)
	mux.HandleFunc("DELETE /api/memories/{id}", s.handleDeleteMemory)
	mux.HandleFunc("PATCH /api/memories/{id}", s.handlePatchMemory)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("GET /api/agents", s.handleListAgents)
	mux.HandleFunc("POST /api/agents", s.handleCreateAgent)
	mux.HandleFunc("PUT /api/agents/{id}", s.handleUpdateAgent)
	mux.HandleFunc("DELETE /api/agents/{id}", s.handleDeleteAgent)
	mux.HandleFunc("/", http.FileServer(http.FS(staticFiles)).ServeHTTP)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}
	return s
}

func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

func (s *Server) StartStatusPoller(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				statuses := s.router.Status()
				data, err := json.Marshal(statuses)
				if err != nil {
					slog.Error("marshal status", "error", err)
					continue
				}
				s.broadcast(fmt.Sprintf("event: status\ndata: %s\n\n", data))
			}
		}
	}()
}

func (s *Server) subscribe() chan string {
	ch := make(chan string, 16)
	s.ssesMu.Lock()
	s.sseSubs = append(s.sseSubs, ch)
	s.ssesMu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan string) {
	s.ssesMu.Lock()
	defer s.ssesMu.Unlock()
	for i, sub := range s.sseSubs {
		if sub == ch {
			s.sseSubs = append(s.sseSubs[:i], s.sseSubs[i+1:]...)
			return
		}
	}
}

func (s *Server) broadcast(msg string) {
	s.ssesMu.Lock()
	defer s.ssesMu.Unlock()
	for _, ch := range s.sseSubs {
		select {
		case ch <- msg:
		default:
			// drop slow subscriber
		}
	}
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		slog.Error("read config file", "error", err)
		http.Error(w, "failed to read config", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Write(data)
}

func (s *Server) handlePostConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	// Write to temp file and fully validate (including required fields) before touching the real config.
	tmpPath := s.cfgPath + ".tmp"
	if err := os.WriteFile(tmpPath, body, 0o644); err != nil {
		slog.Error("write temp config", "error", err)
		http.Error(w, "failed to write config", http.StatusInternalServerError)
		return
	}
	if _, err := config.Load(tmpPath); err != nil {
		os.Remove(tmpPath)
		http.Error(w, fmt.Sprintf("invalid config: %v", err), http.StatusBadRequest)
		return
	}
	if err := os.Rename(tmpPath, s.cfgPath); err != nil {
		os.Remove(tmpPath)
		slog.Error("rename config file", "error", err)
		http.Error(w, "failed to write config", http.StatusInternalServerError)
		return
	}

	if _, err := s.cfgStore.Reload(); err != nil {
		slog.Error("reload config", "error", err)
		http.Error(w, fmt.Sprintf("reload failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.broadcast("event: config_reloaded\ndata: {}\n\n")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	serverID := r.URL.Query().Get("server_id")
	if serverID == "" {
		http.Error(w, "server_id is required", http.StatusBadRequest)
		return
	}

	mem := s.router.MemoryForServer(serverID)
	if mem == nil {
		http.Error(w, "server not configured", http.StatusNotFound)
		return
	}

	opts := memory.ListOptions{
		ServerID: serverID,
		UserID:   r.URL.Query().Get("user_id"),
		Query:    r.URL.Query().Get("q"),
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		opts.Limit, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		opts.Offset, _ = strconv.Atoi(v)
	}

	rows, total, err := mem.List(r.Context(), opts)
	if err != nil {
		slog.Error("list memories", "error", err)
		http.Error(w, "failed to list memories", http.StatusInternalServerError)
		return
	}

	if rows == nil {
		rows = []memory.MemoryRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"memories": rows,
		"total":    total,
	})
}

func (s *Server) handleDeleteMemory(w http.ResponseWriter, r *http.Request) {
	serverID := r.URL.Query().Get("server_id")
	if serverID == "" {
		http.Error(w, "server_id is required", http.StatusBadRequest)
		return
	}

	mem := s.router.MemoryForServer(serverID)
	if mem == nil {
		http.Error(w, "server not configured", http.StatusNotFound)
		return
	}

	id := r.PathValue("id")
	if err := mem.Forget(r.Context(), serverID, id); err != nil {
		slog.Error("forget memory", "error", err, "id", id)
		http.Error(w, "failed to delete memory", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePatchMemory(w http.ResponseWriter, r *http.Request) {
	serverID := r.URL.Query().Get("server_id")
	if serverID == "" {
		http.Error(w, "server_id is required", http.StatusBadRequest)
		return
	}

	mem := s.router.MemoryForServer(serverID)
	if mem == nil {
		http.Error(w, "server not configured", http.StatusNotFound)
		return
	}

	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	id := r.PathValue("id")
	if err := mem.UpdateContent(r.Context(), id, serverID, body.Content); err != nil {
		slog.Error("update memory content", "error", err, "id", id)
		http.Error(w, "failed to update memory", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"agents": s.router.Status(),
		"config": s.cfgStore.Get(),
	})
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			fmt.Fprint(w, msg)
			flusher.Flush()
		}
	}
}

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfgStore.Get()
	type agentView struct {
		ID           string                `json:"id"`
		ServerID     string                `json:"server_id"`
		HasToken     bool                  `json:"has_token"`
		SoulFile     string                `json:"soul_file,omitempty"`
		DBPath       string                `json:"db_path,omitempty"`
		ResponseMode string                `json:"response_mode,omitempty"`
		Channels     []config.ChannelConfig `json:"channels,omitempty"`
	}
	views := make([]agentView, len(cfg.Agents))
	for i, a := range cfg.Agents {
		views[i] = agentView{
			ID:           a.ID,
			ServerID:     a.ServerID,
			HasToken:     a.Token != "",
			SoulFile:     a.SoulFile,
			DBPath:       a.DBPath,
			ResponseMode: a.ResponseMode,
			Channels:     a.Channels,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(views)
}

func (s *Server) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	var input config.AgentConfig
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if input.ID == "" {
		http.Error(w, "id is required", http.StatusBadRequest)
		return
	}
	if input.ServerID == "" {
		http.Error(w, "server_id is required", http.StatusBadRequest)
		return
	}

	cfg := s.cfgStore.Get()
	for _, a := range cfg.Agents {
		if a.ID == input.ID {
			http.Error(w, "agent id already exists", http.StatusConflict)
			return
		}
	}

	newAgents := append(cfg.Agents, input)
	if err := s.writeAgents(newAgents); err != nil {
		slog.Error("write agents", "error", err)
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input config.AgentConfig
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	cfg := s.cfgStore.Get()
	newAgents := make([]config.AgentConfig, len(cfg.Agents))
	copy(newAgents, cfg.Agents)
	found := false
	for i, a := range newAgents {
		if a.ID == id {
			input.ID = id // ensure ID unchanged
			newAgents[i] = input
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	if err := s.writeAgents(newAgents); err != nil {
		slog.Error("write agents", "error", err)
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	cfg := s.cfgStore.Get()
	newAgents := make([]config.AgentConfig, 0, len(cfg.Agents))
	found := false
	for _, a := range cfg.Agents {
		if a.ID == id {
			found = true
			continue
		}
		newAgents = append(newAgents, a)
	}
	if !found {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	if err := s.writeAgents(newAgents); err != nil {
		slog.Error("write agents", "error", err)
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeAgents replaces the [[agents]] section in the config file and reloads.
func (s *Server) writeAgents(agents []config.AgentConfig) error {
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	// Parse existing TOML into a generic map to preserve non-agent fields
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	// Replace agents section — convert via JSON round-trip
	agentsJSON, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	var agentsRaw []any
	if err := json.Unmarshal(agentsJSON, &agentsRaw); err != nil {
		return err
	}
	if len(agentsRaw) == 0 {
		delete(raw, "agents")
	} else {
		raw["agents"] = agentsRaw
	}

	// Encode back to TOML
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	// Validate before writing
	tmpPath := s.cfgPath + ".tmp"
	if err := os.WriteFile(tmpPath, buf.Bytes(), 0o644); err != nil {
		return err
	}
	if _, err := config.Load(tmpPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("invalid config: %w", err)
	}
	if err := os.Rename(tmpPath, s.cfgPath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	if _, err := s.cfgStore.Reload(); err != nil {
		return fmt.Errorf("reload: %w", err)
	}
	s.broadcast("event: config_reloaded\ndata: {}\n\n")
	return nil
}
