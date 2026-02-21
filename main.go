package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tomasmach/mnemon-bot/agent"
	"github.com/tomasmach/mnemon-bot/bot"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/memory"
	"github.com/tomasmach/mnemon-bot/web"
)

func main() {
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "text", "Log format: text or json")
	configPath := flag.String("config", "", "Path to config file")
	flag.Parse()

	setupLogger(*logLevel, *logFormat)

	cfgPath := config.Resolve()
	if *configPath != "" {
		cfgPath = *configPath
	}

	cfgStore, err := config.NewStore(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err, "path", cfgPath)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", cfgPath)

	cfg := cfgStore.Get()

	if cfg.Tools.WebSearchKey == "" {
		slog.Warn("tools.web_search_key not set, web_search tool disabled")
	}

	llmClient := llm.New(cfgStore)

	// Build per-agent resources
	agentsByServerID := make(map[string]*agent.AgentResources, len(cfg.Agents))
	var customBots []*bot.Bot // bots with their own tokens (need separate stop)

	for i := range cfg.Agents {
		agentCfg := &cfg.Agents[i]
		dbPath := agentCfg.ResolveDBPath(cfg.Memory.DBPath)
		mem, err := memory.New(&config.MemoryConfig{DBPath: dbPath}, llmClient)
		if err != nil {
			slog.Error("failed to open memory store for agent", "agent", agentCfg.ID, "error", err)
			os.Exit(1)
		}

		agentsByServerID[agentCfg.ServerID] = &agent.AgentResources{
			Config:  agentCfg,
			Memory:  mem,
			Session: nil, // filled in below
		}

		if agentCfg.Token != "" {
			b, err := bot.New(agentCfg.Token)
			if err != nil {
				slog.Error("failed to create bot for agent", "agent", agentCfg.ID, "error", err)
				os.Exit(1)
			}
			customBots = append(customBots, b)
			agentsByServerID[agentCfg.ServerID].Session = b.Session()
		}
	}

	slog.Info("agents initialized", "count", len(agentsByServerID))

	// Start default bot (handles DMs + agents without custom tokens)
	defaultBot, err := bot.New(cfg.Bot.Token)
	if err != nil {
		slog.Error("failed to create default bot", "error", err)
		os.Exit(1)
	}

	// Fill nil sessions with the default bot's session
	for serverID, res := range agentsByServerID {
		if res.Session == nil {
			agentsByServerID[serverID].Session = defaultBot.Session()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	router := agent.NewRouter(ctx, cfgStore, llmClient, defaultBot.Session(), agentsByServerID)

	// Wire router to all bots
	defaultBot.SetRouter(router)
	for _, b := range customBots {
		b.SetRouter(router)
	}

	// Start all bots
	if err := defaultBot.Start(); err != nil {
		slog.Error("failed to start default bot", "error", err)
		os.Exit(1)
	}
	slog.Info("default bot started")

	for _, b := range customBots {
		if err := b.Start(); err != nil {
			slog.Error("failed to start custom bot", "error", err)
			os.Exit(1)
		}
	}
	if len(customBots) > 0 {
		slog.Info("custom bots started", "count", len(customBots))
	}

	webAddr := cfgStore.Get().Web.Addr
	webServer := web.New(webAddr, cfgStore, cfgPath, router)
	webServer.StartStatusPoller(ctx)
	go func() {
		if err := webServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("web server", "error", err)
		}
	}()
	slog.Info("web server started", "addr", webAddr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	slog.Info("shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = webServer.Shutdown(shutCtx)
	if err := defaultBot.Stop(); err != nil {
		slog.Warn("failed to stop default bot", "error", err)
	}
	for _, b := range customBots {
		if err := b.Stop(); err != nil {
			slog.Warn("failed to stop custom bot", "error", err)
		}
	}
	cancel()
	router.WaitForDrain()
	slog.Info("shutdown complete")
}

func setupLogger(level, format string) {
	var l slog.Level
	switch level {
	case "debug":
		l = slog.LevelDebug
	case "warn":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		l = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: l}
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}
