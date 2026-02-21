package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/tomasmach/mnemon-bot/agent"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/logstore"
	"github.com/tomasmach/mnemon-bot/memory"
)

//go:embed static
var staticFiles embed.FS

type Server struct {
	cfgStore   *config.Store
	cfgPath    string
	router     *agent.Router
	logStore   *logstore.Store
	sseSubs    []chan string
	ssesMu     sync.Mutex
	writeMu    sync.Mutex  // guards config file writes
	httpServer *http.Server
}

func New(addr string, cfgStore *config.Store, cfgPath string, router *agent.Router, logStore *logstore.Store) *Server {
	s := &Server{
		cfgStore: cfgStore,
		cfgPath:  cfgPath,
		router:   router,
		logStore: logStore,
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
	mux.HandleFunc("GET /api/agents/{id}/soul", s.handleGetAgentSoul)
	mux.HandleFunc("PUT /api/agents/{id}/soul", s.handlePutAgentSoul)
	mux.HandleFunc("GET /api/agents/{id}/logs", s.handleGetAgentLogs)
	mux.HandleFunc("GET /api/agents/{id}/conversations", s.handleGetAgentConversations)
	mux.HandleFunc("GET /api/soul", s.handleGetGlobalSoul)
	mux.HandleFunc("PUT /api/soul", s.handlePutGlobalSoul)
	sub, _ := fs.Sub(staticFiles, "static")
	mux.HandleFunc("/", http.FileServer(http.FS(sub)).ServeHTTP)

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
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

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

// memoryForRequest extracts the server_id query param and looks up the memory store.
// Returns nil, "", false and writes the appropriate HTTP error if not found.
func (s *Server) memoryForRequest(w http.ResponseWriter, r *http.Request) (*memory.Store, string, bool) {
	serverID := r.URL.Query().Get("server_id")
	if serverID == "" {
		http.Error(w, "server_id is required", http.StatusBadRequest)
		return nil, "", false
	}
	mem := s.router.MemoryForServer(serverID)
	if mem == nil {
		http.Error(w, "server not configured", http.StatusNotFound)
		return nil, "", false
	}
	return mem, serverID, true
}

func (s *Server) handleListMemories(w http.ResponseWriter, r *http.Request) {
	mem, serverID, ok := s.memoryForRequest(w, r)
	if !ok {
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
	mem, serverID, ok := s.memoryForRequest(w, r)
	if !ok {
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
	mem, serverID, ok := s.memoryForRequest(w, r)
	if !ok {
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

// Note: tokenless agents are hot-loaded on the next incoming message via tryHotLoad.
// Agents with custom tokens require a restart to open a new Discord session.
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

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	if findAgentIndex(cfg.Agents, input.ID) != -1 {
		http.Error(w, "agent id already exists", http.StatusConflict)
		return
	}
	for _, a := range cfg.Agents {
		if a.ServerID == input.ServerID {
			http.Error(w, "server_id already exists", http.StatusConflict)
			return
		}
	}

	newAgents := make([]config.AgentConfig, len(cfg.Agents)+1)
	copy(newAgents, cfg.Agents)
	newAgents[len(cfg.Agents)] = input
	if err := s.writeAgents(newAgents); err != nil {
		slog.Error("write agents", "error", err)
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

// Note: tokenless agents are hot-loaded on the next incoming message via tryHotLoad.
// Agents with custom tokens require a restart to open a new Discord session.
func (s *Server) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input config.AgentConfig
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	idx := findAgentIndex(cfg.Agents, id)
	if idx == -1 {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	oldServerID := cfg.Agents[idx].ServerID

	newAgents := make([]config.AgentConfig, len(cfg.Agents))
	copy(newAgents, cfg.Agents)
	if input.Token == "" {
		input.Token = newAgents[idx].Token // preserve existing token if not updated
	}
	input.ID = id // ensure ID unchanged
	newAgents[idx] = input

	if err := s.writeAgents(newAgents); err != nil {
		slog.Error("write agents", "error", err)
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}
	s.router.UnloadAgent(oldServerID)
	if input.ServerID != oldServerID {
		s.router.UnloadAgent(input.ServerID)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	idx := findAgentIndex(cfg.Agents, id)
	if idx == -1 {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	serverID := cfg.Agents[idx].ServerID

	newAgents := make([]config.AgentConfig, 0, len(cfg.Agents)-1)
	for i, a := range cfg.Agents {
		if i != idx {
			newAgents = append(newAgents, a)
		}
	}

	if err := s.writeAgents(newAgents); err != nil {
		slog.Error("write agents", "error", err)
		http.Error(w, "failed to save agent", http.StatusInternalServerError)
		return
	}

	s.router.UnloadAgent(serverID)
	w.WriteHeader(http.StatusNoContent)
}

// findAgentIndex returns the index of the agent with the given ID, or -1 if not found.
func findAgentIndex(agents []config.AgentConfig, id string) int {
	for i, a := range agents {
		if a.ID == id {
			return i
		}
	}
	return -1
}

// agentServerID resolves an agent ID to its server_id from config.
func (s *Server) agentServerID(id string) (string, bool) {
	cfg := s.cfgStore.Get()
	idx := findAgentIndex(cfg.Agents, id)
	if idx == -1 {
		return "", false
	}
	return cfg.Agents[idx].ServerID, true
}

// queryInt parses a query parameter as an integer, returning defaultVal
// when the parameter is absent or invalid.
func queryInt(r *http.Request, key string, defaultVal, minVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < minVal {
		return defaultVal
	}
	return n
}

func (s *Server) handleGetAgentLogs(w http.ResponseWriter, r *http.Request) {
	if s.logStore == nil {
		http.Error(w, "log store not available", http.StatusServiceUnavailable)
		return
	}

	id := r.PathValue("id")
	serverID, ok := s.agentServerID(id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	level := r.URL.Query().Get("level")
	limit := queryInt(r, "limit", 100, 1)
	offset := queryInt(r, "offset", 0, 0)

	rows, total, err := s.logStore.List(r.Context(), serverID, level, limit, offset)
	if err != nil {
		slog.Error("list agent logs", "error", err, "server_id", serverID)
		http.Error(w, "failed to list logs", http.StatusInternalServerError)
		return
	}

	if rows == nil {
		rows = []logstore.LogRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"logs":  rows,
		"total": total,
	})
}

func (s *Server) handleGetAgentConversations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	serverID, ok := s.agentServerID(id)
	if !ok {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	mem := s.router.MemoryForServer(serverID)
	if mem == nil {
		http.Error(w, "memory store not available", http.StatusServiceUnavailable)
		return
	}

	channelID := r.URL.Query().Get("channel_id")
	limit := queryInt(r, "limit", 50, 1)
	offset := queryInt(r, "offset", 0, 0)

	rows, total, err := mem.ListConversations(r.Context(), channelID, limit, offset)
	if err != nil {
		slog.Error("list agent conversations", "error", err, "server_id", serverID)
		http.Error(w, "failed to list conversations", http.StatusInternalServerError)
		return
	}

	if rows == nil {
		rows = []memory.ConversationRow{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"conversations": rows,
		"total":         total,
	})
}

func (s *Server) handleGetAgentSoul(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.cfgStore.Get()
	idx := findAgentIndex(cfg.Agents, id)
	if idx == -1 {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}
	respondSoulFile(w, cfg.Agents[idx].SoulFile)
}

func (s *Server) handlePutAgentSoul(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	agentIdx := findAgentIndex(cfg.Agents, id)
	if agentIdx == -1 {
		http.Error(w, "agent not found", http.StatusNotFound)
		return
	}

	agent := cfg.Agents[agentIdx]
	var soulPath string
	needsConfigUpdate := false

	if agent.SoulFile == "" {
		soulPath = filepath.Join(filepath.Dir(s.cfgPath), "souls", id+".md")
		needsConfigUpdate = true
	} else {
		soulPath = config.ExpandPath(agent.SoulFile)
	}

	if err := os.MkdirAll(filepath.Dir(soulPath), 0o755); err != nil {
		slog.Error("create soul dir", "error", err)
		http.Error(w, "failed to create directory", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(soulPath, []byte(body.Content), 0o644); err != nil {
		slog.Error("write soul file", "error", err)
		http.Error(w, "failed to write soul file", http.StatusInternalServerError)
		return
	}

	if needsConfigUpdate {
		newAgents := make([]config.AgentConfig, len(cfg.Agents))
		copy(newAgents, cfg.Agents)
		newAgents[agentIdx].SoulFile = soulPath
		if err := s.writeAgents(newAgents); err != nil {
			slog.Error("write agents config", "error", err)
			http.Error(w, "failed to update config", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"path": soulPath})
}

func (s *Server) handleGetGlobalSoul(w http.ResponseWriter, r *http.Request) {
	respondSoulFile(w, s.cfgStore.Get().Bot.SoulFile)
}

// respondSoulFile reads a soul file path and writes the JSON response.
// If soulFile is empty, responds with using_default: true.
func respondSoulFile(w http.ResponseWriter, soulFile string) {
	w.Header().Set("Content-Type", "application/json")
	if soulFile == "" {
		json.NewEncoder(w).Encode(map[string]any{
			"content":       "",
			"path":          "",
			"using_default": true,
		})
		return
	}
	expanded := config.ExpandPath(soulFile)
	data, err := os.ReadFile(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			json.NewEncoder(w).Encode(map[string]any{"content": "", "path": expanded})
			return
		}
		slog.Error("read soul file", "error", err)
		http.Error(w, "failed to read soul file", http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"content": string(data), "path": expanded})
}

func (s *Server) handlePutGlobalSoul(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg := s.cfgStore.Get()
	var soulPath string
	needsConfigUpdate := false

	if cfg.Bot.SoulFile == "" {
		soulPath = filepath.Join(filepath.Dir(s.cfgPath), "soul.md")
		needsConfigUpdate = true
	} else {
		soulPath = config.ExpandPath(cfg.Bot.SoulFile)
	}

	if err := os.MkdirAll(filepath.Dir(soulPath), 0o755); err != nil {
		slog.Error("create soul dir", "error", err)
		http.Error(w, "failed to create directory", http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(soulPath, []byte(body.Content), 0o644); err != nil {
		slog.Error("write global soul file", "error", err)
		http.Error(w, "failed to write soul file", http.StatusInternalServerError)
		return
	}

	if needsConfigUpdate {
		if err := s.patchConfig(func(raw map[string]any) {
			bot, _ := raw["bot"].(map[string]any)
			if bot == nil {
				bot = make(map[string]any)
			}
			bot["soul_file"] = soulPath
			raw["bot"] = bot
		}); err != nil {
			slog.Error("update config for global soul", "error", err)
			http.Error(w, fmt.Sprintf("failed to update config: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"path": soulPath})
}

// writeAgents replaces the [[agents]] section in the config file and reloads.
func (s *Server) writeAgents(agents []config.AgentConfig) error {
	// Build token map before marshaling — Token has json:"-" so Marshal drops it
	tokenByID := make(map[string]string, len(agents))
	for _, a := range agents {
		tokenByID[a.ID] = a.Token
	}

	agentsJSON, err := json.Marshal(agents)
	if err != nil {
		return err
	}
	var agentsRaw []any
	if err := json.Unmarshal(agentsJSON, &agentsRaw); err != nil {
		return err
	}
	// Restore tokens dropped by json:"-"
	for _, item := range agentsRaw {
		if m, ok := item.(map[string]any); ok {
			id, _ := m["id"].(string)
			if tok := tokenByID[id]; tok != "" {
				m["token"] = tok
			}
		}
	}

	return s.patchConfig(func(raw map[string]any) {
		if len(agentsRaw) == 0 {
			delete(raw, "agents")
		} else {
			raw["agents"] = agentsRaw
		}
	})
}

// patchConfig reads the config TOML into a generic map, applies mutate to modify it,
// then validates, writes atomically, reloads the store, and broadcasts a config_reloaded event.
func (s *Server) patchConfig(mutate func(raw map[string]any)) error {
	data, err := os.ReadFile(s.cfgPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}

	mutate(raw)

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(raw); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

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
