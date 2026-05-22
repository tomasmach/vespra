package main

import (
	"context"
	"encoding/json"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tomasmach/vespra/sandbox"
)

func main() {
	addr := flag.String("addr", envString("VESPRARUNNER_ADDR", ":8090"), "HTTP listen address")
	token := flag.String("token", envString("VESPRARUNNER_TOKEN", ""), "Bearer token required from Vespra")
	dockerPath := flag.String("docker", envString("VESPRARUNNER_DOCKER", "docker"), "docker CLI path")
	maxConcurrent := flag.Int("max-concurrent", envInt("VESPRARUNNER_MAX_CONCURRENT", 4), "maximum concurrent jobs")
	flag.Parse()

	if *token == "" {
		slog.Error("VESPRARUNNER_TOKEN or --token is required")
		os.Exit(1)
	}
	executor := &sandbox.DockerExecutor{DockerPath: *dockerPath}
	srv := &server{
		token:    *token,
		executor: executor,
		sem:      make(chan struct{}, *maxConcurrent),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/run", srv.handleRun)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	httpSrv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		slog.Info("runnerd listening", "addr", *addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("runnerd failed", "error", err)
			os.Exit(1)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
}

type server struct {
	token    string
	executor sandbox.Runner
	sem      chan struct{}
}

func (s *server) handleRun(w http.ResponseWriter, r *http.Request) {
	if !validBearer(r.Header.Get("Authorization"), s.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		http.Error(w, "runner concurrency limit reached", http.StatusTooManyRequests)
		return
	}

	var req sandbox.RunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	result, err := s.executor.Run(r.Context(), req)
	if err != nil {
		slog.Warn("sandbox run failed", "error", err, "server_id", req.ServerID, "channel_id", req.ChannelID, "user_id", req.UserID)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	slog.Info("sandbox run complete",
		"server_id", req.ServerID,
		"channel_id", req.ChannelID,
		"user_id", req.UserID,
		"exit_code", result.ExitCode,
		"timed_out", result.TimedOut,
		"duration_ms", result.DurationMS,
		"container_id", result.ContainerID,
	)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func validBearer(header, token string) bool {
	const prefix = "Bearer "
	return strings.HasPrefix(header, prefix) && strings.TrimPrefix(header, prefix) == token
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
