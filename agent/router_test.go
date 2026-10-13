package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/tomasmach/vespra/config"
	"github.com/tomasmach/vespra/llm"
	"github.com/tomasmach/vespra/schedule"
)

func newTestRouter(t *testing.T) *Router {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Response: config.ResponseConfig{DefaultMode: "all"},
		Agent: config.TurnConfig{
			HistoryLimit:       20,
			IdleTimeoutMinutes: 10,
			MaxToolIterations:  10,
		},
		Memory: config.MemoryConfig{DBPath: filepath.Join(dir, "test.db")},
	}
	cfgStore := config.NewStoreFromConfig(cfg)
	llmClient := llm.New(cfgStore)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	r, err := NewRouter(ctx, cfgStore, llmClient, nil, make(map[string]*AgentResources))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return r
}

func fakeMsg(guildID, channelID, userID string) *discordgo.MessageCreate {
	return &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "msg-" + userID,
			ChannelID: channelID,
			GuildID:   guildID,
			Content:   "hello",
			Author:    &discordgo.User{ID: userID, Bot: false},
		},
	}
}

func fakeMsgWithContent(guildID, channelID, userID, content string) *discordgo.MessageCreate {
	msg := fakeMsg(guildID, channelID, userID)
	msg.Content = content
	return msg
}

func newTestDiscordSession(botID, botName string) *discordgo.Session {
	state := discordgo.NewState()
	state.User = &discordgo.User{ID: botID, Username: botName}
	return &discordgo.Session{
		State: state,
	}
}

func TestRouteIgnoresUnknownServer(t *testing.T) {
	r := newTestRouter(t)
	// guild1 is not in agentsByServerID and not in config.Agents,
	// so Route should silently return without panicking or creating an agent.
	r.Route(fakeMsg("guild1", "chan1", "user1"))

	statuses := r.Status()
	for _, s := range statuses {
		if s.ChannelID == "chan1" {
			t.Error("expected no agent for unconfigured server, but found one in Status()")
		}
	}
}

func TestUnloadAgentEvictsFromCache(t *testing.T) {
	r := newTestRouter(t)

	// Manually register a fake agent resource for srv1
	r.mu.Lock()
	r.agentsByServerID["srv1"] = &AgentResources{
		Config:  &config.AgentConfig{},
		Memory:  nil,
		Session: nil,
	}
	r.mu.Unlock()

	r.UnloadAgent("srv1")

	r.mu.Lock()
	_, exists := r.agentsByServerID["srv1"]
	r.mu.Unlock()

	if exists {
		t.Error("UnloadAgent() should remove server from agentsByServerID")
	}
}

func TestUnloadAgentNonExistent(t *testing.T) {
	r := newTestRouter(t)
	// Should not panic when unloading a server that was never loaded
	r.UnloadAgent("nonexistent-server")
}

func registerFakeAgent(t *testing.T, r *Router, serverID string, ignoreUsers []string) {
	t.Helper()
	r.mu.Lock()
	r.agentsByServerID[serverID] = &AgentResources{
		Config:  &config.AgentConfig{IgnoreUsers: ignoreUsers},
		Memory:  nil,
		Session: nil,
	}
	r.mu.Unlock()
}

func TestSpamBlockAfterThreshold(t *testing.T) {
	r := newTestRouter(t)

	// Call checkSpam spamThreshold-1 times — should not be blocked yet.
	r.mu.Lock()
	for i := 0; i < spamThreshold-1; i++ {
		blocked, _ := r.checkSpam("srv1", "user1", true)
		if blocked {
			r.mu.Unlock()
			t.Fatalf("user blocked early at call %d", i+1)
		}
	}

	// The threshold-th call triggers the block.
	blocked, justBlocked := r.checkSpam("srv1", "user1", true)
	r.mu.Unlock()

	if !blocked {
		t.Error("user should be blocked after reaching threshold")
	}
	if !justBlocked {
		t.Error("justBlocked should be true on the triggering call")
	}

	// Subsequent call should return blocked but not justBlocked.
	r.mu.Lock()
	blocked2, justBlocked2 := r.checkSpam("srv1", "user1", true)
	r.mu.Unlock()

	if !blocked2 {
		t.Error("user should remain blocked")
	}
	if justBlocked2 {
		t.Error("justBlocked should be false for subsequent blocked calls")
	}
}

func TestSpamBlockExpires(t *testing.T) {
	r := newTestRouter(t)

	// Manually set an expired block.
	r.mu.Lock()
	r.spamMap["srv1:user1"] = &spamRecord{
		blockedUntil: time.Now().Add(-1 * time.Second),
	}
	blocked, _ := r.checkSpam("srv1", "user1", true)
	r.mu.Unlock()

	if blocked {
		t.Error("expired block should not still block the user")
	}
}

func TestRouteDoesNotSpamBlockUnaddressedGuildMessages(t *testing.T) {
	r := newTestRouter(t)

	r.mu.Lock()
	r.agentsByServerID["srv1"] = &AgentResources{
		Config:  &config.AgentConfig{},
		Memory:  nil,
		Session: newTestDiscordSession("bot1", "Vespra"),
	}
	r.agents["chan1"] = &ChannelAgent{
		channelID: "chan1",
		serverID:  "srv1",
		msgCh:     make(chan *discordgo.MessageCreate, 100),
		cancel:    func() {},
	}
	r.mu.Unlock()

	for i := 0; i < spamThreshold+1; i++ {
		r.Route(fakeMsgWithContent("srv1", "chan1", "user1", "random channel chatter"))
	}

	r.mu.Lock()
	rec, exists := r.spamMap["srv1:user1"]
	r.mu.Unlock()

	if exists && rec.blockedUntil.After(time.Now()) {
		t.Error("unaddressed guild messages should not trigger a spam block")
	}
	if exists && len(rec.timestamps) != 0 {
		t.Errorf("unaddressed guild messages should not be counted as spam, got %d timestamps", len(rec.timestamps))
	}
}

func TestRouteDropsUnaddressedMessagesDuringSpamBlock(t *testing.T) {
	r := newTestRouter(t)

	msgCh := make(chan *discordgo.MessageCreate, 100)
	r.mu.Lock()
	r.agentsByServerID["srv1"] = &AgentResources{
		Config:  &config.AgentConfig{},
		Memory:  nil,
		Session: newTestDiscordSession("bot1", "Vespra"),
	}
	r.agents["chan1"] = &ChannelAgent{
		channelID: "chan1",
		serverID:  "srv1",
		msgCh:     msgCh,
		cancel:    func() {},
	}
	r.spamMap["srv1:user1"] = &spamRecord{blockedUntil: time.Now().Add(spamCooldown)}
	r.mu.Unlock()

	r.Route(fakeMsgWithContent("srv1", "chan1", "user1", "random channel chatter"))

	if len(msgCh) != 0 {
		t.Errorf("blocked user's unaddressed message should be dropped, got %d queued", len(msgCh))
	}
}

func TestRestartAgentEvictsChannelAgents(t *testing.T) {
	r := newTestRouter(t)
	registerFakeAgent(t, r, "srv1", nil)

	// Add two fake channel agents for srv1 directly to r.agents.
	cancel1 := func() {}
	cancel2 := func() {}

	r.mu.Lock()
	r.agents["chan1"] = &ChannelAgent{channelID: "chan1", serverID: "srv1", msgCh: make(chan *discordgo.MessageCreate, 100), cancel: cancel1}
	r.agents["chan2"] = &ChannelAgent{channelID: "chan2", serverID: "srv1", msgCh: make(chan *discordgo.MessageCreate, 100), cancel: cancel2}
	r.mu.Unlock()

	r.RestartAgent("srv1")

	r.mu.Lock()
	_, exists1 := r.agents["chan1"]
	_, exists2 := r.agents["chan2"]
	r.mu.Unlock()

	if exists1 {
		t.Error("RestartAgent() should remove chan1 from agents map")
	}
	if exists2 {
		t.Error("RestartAgent() should remove chan2 from agents map")
	}
}

func TestRestartAgentEvictsResourceCache(t *testing.T) {
	r := newTestRouter(t)
	registerFakeAgent(t, r, "srv1", nil)

	r.RestartAgent("srv1")

	r.mu.Lock()
	_, exists := r.agentsByServerID["srv1"]
	r.mu.Unlock()

	if exists {
		t.Error("RestartAgent() should remove server from agentsByServerID")
	}
}

func TestIgnoreUserDropsMessage(t *testing.T) {
	r := newTestRouter(t)
	registerFakeAgent(t, r, "srv1", []string{"ignored-user"})

	r.Route(fakeMsg("srv1", "chan1", "ignored-user"))

	// No agent should have been spawned for the ignored user.
	r.mu.Lock()
	_, exists := r.agents["chan1"]
	r.mu.Unlock()
	if exists {
		t.Error("ignored user message should not spawn an agent")
	}
}

type scheduledTaskTransport struct {
	baseURL string
}

func (t scheduledTaskTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	redirected := req.Clone(req.Context())
	urlCopy := *req.URL
	base, err := url.Parse(t.baseURL)
	if err != nil {
		return nil, err
	}
	urlCopy.Scheme, urlCopy.Host = base.Scheme, base.Host
	redirected.URL = &urlCopy
	return http.DefaultTransport.RoundTrip(redirected)
}

func TestScheduledTaskRunsOnChannelAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  string
		dm    bool
		batch bool
	}{
		{"smart", config.ModeSmart, false, false},
		{"mention batch", config.ModeMention, false, true},
		{"dm", config.ModeMention, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var chats []struct {
				Messages []llm.Message        `json:"messages"`
				Tools    []llm.ToolDefinition `json:"tools"`
			}
			var sentChannels []string
			replies := make(chan struct{}, 4)
			backfilled := false
			future := time.Now().Add(time.Hour).Format("2006-01-02 15:04")
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(req.URL.Path, "/embeddings"):
					fmt.Fprint(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
				case strings.HasSuffix(req.URL.Path, "/chat/completions"):
					var chat struct {
						Messages []llm.Message        `json:"messages"`
						Tools    []llm.ToolDefinition `json:"tools"`
					}
					if err := json.NewDecoder(req.Body).Decode(&chat); err != nil {
						t.Error(err)
					}
					mu.Lock()
					chats = append(chats, chat)
					call := len(chats)
					mu.Unlock()
					if call == 2 {
						args, err := json.Marshal(map[string]string{"prompt": "another reminder", "at": future})
						if err != nil {
							t.Error(err)
						}
						json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "schedule", Type: "function", Function: llm.FunctionCall{Name: "schedule_task", Arguments: string(args)}}}}}}})
					} else {
						fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"Tram reminder sent."}}]}`)
					}
				case req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/messages"):
					mu.Lock()
					backfilled = true
					mu.Unlock()
					fmt.Fprint(w, `[{"id":"older","content":"earlier conversation","author":{"id":"user1","username":"alice"}}]`)
				case req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/messages"):
					mu.Lock()
					sentChannels = append(sentChannels, req.URL.Path)
					mu.Unlock()
					fmt.Fprint(w, `{"id":"reply","content":"Tram reminder sent."}`)
					replies <- struct{}{}
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer srv.Close()
			cfg := &config.Config{
				LLM:      config.LLMConfig{BaseURL: srv.URL, Model: "test", RequestTimeoutSeconds: 2},
				Agent:    config.TurnConfig{HistoryLimit: 20, HistoryBackfillLimit: 2, IdleTimeoutMinutes: 10, MaxToolIterations: 5, MaxReplyParts: 2, MemoryRecallLimit: 5, SendRateLimit: 20, SendRateWindowSeconds: 60, CoalesceDisabled: !tc.batch, CoalesceDebounceMs: 20, CoalesceMaxWaitMs: 50},
				Response: config.ResponseConfig{DefaultMode: tc.mode},
				Memory:   config.MemoryConfig{DBPath: filepath.Join(t.TempDir(), "memory.db")},
				Agents:   []config.AgentConfig{{ServerID: "server1", Language: "Czech"}},
			}
			cfgStore := config.NewStoreFromConfig(cfg)
			session, err := discordgo.New("Bot fake")
			if err != nil {
				t.Fatal(err)
			}
			session.State.User = &discordgo.User{ID: "bot", Username: "Vespra"}
			session.Client = &http.Client{Transport: scheduledTaskTransport{baseURL: srv.URL}, Timeout: 2 * time.Second}
			ctx, cancel := context.WithCancel(context.Background())
			router, err := NewRouter(ctx, cfgStore, llm.New(cfgStore), session, map[string]*AgentResources{})
			if err != nil {
				cancel()
				t.Fatal(err)
			}
			defer func() { cancel(); router.WaitForDrain() }()
			store, err := schedule.Open(filepath.Join(t.TempDir(), "schedule.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			router.SetScheduleStore(store)
			serverID := "server1"
			if tc.dm {
				serverID = "DM:user1"
			}
			task, err := store.Create(context.Background(), serverID, "channel1", "user1", "Remind <@user1> that their tram leaves at 16:06", future, "")
			if err != nil {
				t.Fatal(err)
			}
			original := time.Now().Add(-time.Hour).Truncate(time.Minute).In(time.Local)
			task.NextRun = original
			if !router.FireTask(ctx, task) {
				t.Fatal("task was dropped")
			}
			select {
			case <-replies:
			case <-time.After(3 * time.Second):
				t.Fatal("scheduled turn did not reply")
			}
			message := fakeMsgWithContent("server1", "channel1", "user1", "<@bot> follow up")
			if tc.dm {
				message.GuildID = ""
			}
			if tc.batch {
				router.Route(fakeMsgWithContent("server1", "channel1", "first-user", "<@bot> first message"))
				message.Author.ID = "last-user"
				message.Content = "ok"
			}
			router.Route(message)
			select {
			case <-replies:
			case <-time.After(3 * time.Second):
				t.Fatal("follow-up turn did not reply")
			}
			cancel()
			router.WaitForDrain()
			mu.Lock()
			defer mu.Unlock()
			if !backfilled || len(chats) != 3 {
				t.Fatalf("backfill %v, chat count %d", backfilled, len(chats))
			}
			if len(sentChannels) != 2 {
				t.Fatalf("sent to %v", sentChannels)
			}
			for _, channel := range sentChannels {
				if !strings.Contains(channel, "/channels/channel1/messages") {
					t.Errorf("wrong channel %s", channel)
				}
			}
			for _, def := range chats[0].Tools {
				if strings.Contains(def.Function.Name, "task") {
					t.Errorf("scheduled turn has schedule tool %s", def.Function.Name)
				}
			}
			if !strings.Contains(chats[0].Messages[0].Content, "Current date and time:") || !strings.Contains(chats[0].Messages[0].Content, "UTC") {
				t.Error("system prompt lacks local clock")
			}
			if !tc.dm && !strings.Contains(chats[0].Messages[0].Content, "Always respond in Czech") {
				t.Error("scheduled turn lost language")
			}
			var followUpContext strings.Builder
			for _, message := range chats[1].Messages {
				followUpContext.WriteString(message.Content)
			}
			for _, want := range []string{"earlier conversation", "Scheduled task created by <@user1>", original.Format("2006-01-02 15:04"), task.Prompt, "Tram reminder sent."} {
				if !strings.Contains(followUpContext.String(), want) {
					t.Errorf("follow-up missing %q", want)
				}
			}
			pending, err := store.List(context.Background(), serverID)
			if err != nil || len(pending) != 1 {
				t.Fatalf("normal turn did not create task: %+v, %v", pending, err)
			}
			wantAuthor := "user1"
			if tc.batch {
				wantAuthor = "first-user"
			}
			if pending[0].UserID != wantAuthor || pending[0].ChannelID != "channel1" {
				t.Errorf("creator/channel: %+v", pending[0])
			}
		})
	}
}

func TestFireTaskDropsRemovedAgent(t *testing.T) {
	r := newTestRouter(t)
	if r.FireTask(context.Background(), schedule.Task{ServerID: "removed", ChannelID: "channel"}) {
		t.Error("task for a server without an agent was accepted")
	}
	if len(r.Status()) != 0 {
		t.Error("spawned an agent for removed server")
	}
}

func TestScheduledTaskSkipsDisabledChannel(t *testing.T) {
	r := newTestRouter(t)
	r.cfgStore.Get().Response.DefaultMode = config.ModeNone
	a := newChannelAgent("channel", "server", r.cfgStore, r.llm, nil)
	a.scheduleStore = openTestScheduleStore(t)
	task, err := a.scheduleStore.Create(context.Background(), "server", "channel", "user", "remind me", time.Now().Add(time.Hour).Format("2006-01-02 15:04"), "")
	if err != nil {
		t.Fatal(err)
	}
	a.handleScheduledTask(context.Background(), task)
	if len(a.history) != 0 {
		t.Error("disabled channel processed task")
	}
	if exists, err := a.scheduleStore.Exists(context.Background(), task.ID); err != nil || exists {
		t.Errorf("skipped one-shot task kept for retry: %v, %v", exists, err)
	}
}

func TestScheduledTaskCancelledInQueueDoesNotRun(t *testing.T) {
	r := newTestRouter(t)
	// nil resources: running the turn would panic, so returning early is the proof.
	a := newChannelAgent("channel", "server", r.cfgStore, r.llm, nil)
	a.scheduleStore = openTestScheduleStore(t)
	task, err := a.scheduleStore.Create(context.Background(), "server", "channel", "user", "daily news", "", "0 8 * * *")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.scheduleStore.Cancel(context.Background(), "server", task.ID); err != nil {
		t.Fatal(err)
	}
	a.handleScheduledTask(context.Background(), task)
}

func openTestScheduleStore(t *testing.T) *schedule.Store {
	t.Helper()
	store, err := schedule.Open(filepath.Join(t.TempDir(), "schedule.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
