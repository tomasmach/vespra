package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RunRequest describes one non-interactive bash command to execute in a
// per-server sandbox workspace.
type RunRequest struct {
	ServerID        string `json:"server_id"`
	ChannelID       string `json:"channel_id,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	MessageID       string `json:"message_id,omitempty"`
	Command         string `json:"command"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	MaxOutputBytes  int    `json:"max_output_bytes"`
	MaxCommandBytes int    `json:"max_command_bytes"`
	JobImage        string `json:"job_image"`
	VolumePrefix    string `json:"volume_prefix"`
	EgressNetwork   string `json:"egress_network"`
	CPUs            string `json:"cpus"`
	Memory          string `json:"memory"`
	PidsLimit       int    `json:"pids_limit"`
}

// RunResult is the capped output returned from a sandboxed command.
type RunResult struct {
	ExitCode        int    `json:"exit_code"`
	TimedOut        bool   `json:"timed_out"`
	DurationMS      int64  `json:"duration_ms"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	ContainerID     string `json:"container_id,omitempty"`
}

// Runner executes a bash command in an external sandbox service.
type Runner interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// HTTPRunner calls vespra-runnerd over HTTP.
type HTTPRunner struct {
	baseURL string
	token   string
	client  *http.Client
}

func NewHTTPRunner(baseURL, token string, client *http.Client) *HTTPRunner {
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	return &HTTPRunner{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		client:  client,
	}
}

func (r *HTTPRunner) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if r.baseURL == "" {
		return RunResult{}, fmt.Errorf("bash runner URL is not configured")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return RunResult{}, fmt.Errorf("marshal bash run request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+"/v1/run", bytes.NewReader(body))
	if err != nil {
		return RunResult{}, fmt.Errorf("build bash runner request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(httpReq)
	if err != nil {
		return RunResult{}, fmt.Errorf("call bash runner: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return RunResult{}, fmt.Errorf("read bash runner response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RunResult{}, fmt.Errorf("bash runner returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var result RunResult
	if err := json.Unmarshal(respBody, &result); err != nil {
		return RunResult{}, fmt.Errorf("decode bash runner response: %w", err)
	}
	return result, nil
}

func TimeoutDuration(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 30
	}
	return time.Duration(seconds) * time.Second
}
