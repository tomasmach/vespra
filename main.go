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

	mem, err := memory.New(&cfg.Memory, llmClient)
	if err != nil {
		slog.Error("failed to open memory store", "error", err)
		os.Exit(1)
	}
	slog.Info("memory store opened")

	b, err := bot.New(cfg.Bot.Token)
	if err != nil {
		slog.Error("failed to create bot", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	router := agent.NewRouter(ctx, cfgStore, llmClient, mem, b.Session())
	b.SetRouter(router)

	if err := b.Start(); err != nil {
		slog.Error("failed to start bot", "error", err)
		os.Exit(1)
	}
	slog.Info("bot started")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh

	slog.Info("shutting down")
	b.Stop()
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
