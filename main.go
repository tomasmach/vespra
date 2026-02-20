package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/tomasmach/mnemon-bot/agent"
	"github.com/tomasmach/mnemon-bot/bot"
	"github.com/tomasmach/mnemon-bot/config"
	"github.com/tomasmach/mnemon-bot/llm"
	"github.com/tomasmach/mnemon-bot/memory"
)

func main() {
	logLevel := flag.String("log-level", "info", "Log level: debug, info, warn, error")
	logFormat := flag.String("log-format", "text", "Log format: text or json")
	configPath := flag.String("config", "", "Path to config file")
	flag.Parse()

	setupLogger(*logLevel, *logFormat)

	// Config path: --config flag > MNEMON_CONFIG env > default
	cfgPath := config.Resolve()
	if *configPath != "" {
		cfgPath = *configPath
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		slog.Error("failed to load config", "error", err, "path", cfgPath)
		os.Exit(1)
	}
	slog.Info("config loaded", "path", cfgPath)

	if cfg.Tools.WebSearchKey == "" {
		slog.Warn("tools.web_search_key not set, web_search tool disabled")
	}

	llmClient := llm.New(&cfg.LLM)

	mem, err := memory.New(&cfg.Memory, llmClient)
	if err != nil {
		slog.Error("failed to open memory store", "error", err)
		os.Exit(1)
	}
	slog.Info("memory store opened")

	// Create bot first to get the Discord session.
	b, err := bot.New(cfg.Bot.Token)
	if err != nil {
		slog.Error("failed to create bot", "error", err)
		os.Exit(1)
	}

	// Create root context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create router with the session from bot.
	router := agent.NewRouter(ctx, cfg, llmClient, mem, b.Session())

	// Wire router into bot before opening the gateway.
	b.SetRouter(router)

	if err := b.Start(); err != nil {
		slog.Error("failed to start bot", "error", err)
		os.Exit(1)
	}
	slog.Info("bot started")

	// Block until SIGTERM or SIGINT.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	slog.Info("shutting down")
	cancel()
	b.Stop()
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
