package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tomasmach/vespra/sandbox"
)

type BashDeps struct {
	Runner               sandbox.Runner
	ServerID             string
	ChannelID            string
	UserID               string
	MessageID            string
	TimeoutSeconds       int
	MaxOutputBytes       int
	MaxCommandBytes      int
	JobImage             string
	VolumePrefix         string
	EgressNetwork        string
	CPUs                 string
	Memory               string
	PidsLimit            int
	GlobalConcurrency    int
	PerServerConcurrency int
	PerUserRateLimit     int
}

type bashExecTool struct {
	deps       *BashDeps
	bashCalled *bool
}

func (t *bashExecTool) Name() string { return ToolNameBashExec }

func (t *bashExecTool) Description() string {
	return "Run a non-interactive bash command in this Discord server's isolated Docker sandbox. " +
		"The command runs from /workspace, which persists for this Discord server only. " +
		"Use this only when shell execution is directly useful. Do not request secrets, tokens, host files, or Docker access."
}

func (t *bashExecTool) Parameters() json.RawMessage {
	return json.RawMessage(`{
        "type": "object",
        "properties": {
            "command": {"type": "string", "description": "A non-interactive bash command to run from /workspace."},
            "reason": {"type": "string", "description": "Brief reason this command is needed."}
        },
        "required": ["command"]
    }`)
}

func (t *bashExecTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command string `json:"command"`
		Reason  string `json:"reason"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return "Error: command is required", nil
	}
	if t.deps == nil || t.deps.Runner == nil {
		return "Error: bash runner is not configured", nil
	}
	if t.deps.MaxCommandBytes > 0 && len(command) > t.deps.MaxCommandBytes {
		return fmt.Sprintf("Error: command exceeds max_command_bytes (%d)", t.deps.MaxCommandBytes), nil
	}
	release, limited := globalBashLimiter.acquire(t.deps)
	if limited != "" {
		return limited, nil
	}
	defer release()

	*t.bashCalled = true
	runCtx, cancel := context.WithTimeout(ctx, sandbox.TimeoutDuration(t.deps.TimeoutSeconds)+5*time.Second)
	defer cancel()
	result, err := t.deps.Runner.Run(runCtx, sandbox.RunRequest{
		ServerID:        t.deps.ServerID,
		ChannelID:       t.deps.ChannelID,
		UserID:          t.deps.UserID,
		MessageID:       t.deps.MessageID,
		Command:         command,
		TimeoutSeconds:  t.deps.TimeoutSeconds,
		MaxOutputBytes:  t.deps.MaxOutputBytes,
		MaxCommandBytes: t.deps.MaxCommandBytes,
		JobImage:        t.deps.JobImage,
		VolumePrefix:    t.deps.VolumePrefix,
		EgressNetwork:   t.deps.EgressNetwork,
		CPUs:            t.deps.CPUs,
		Memory:          t.deps.Memory,
		PidsLimit:       t.deps.PidsLimit,
	})
	if err != nil {
		return fmt.Sprintf("Error: %s", err), nil
	}
	return formatBashResult(result), nil
}

func formatBashResult(result sandbox.RunResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Command finished in %dms with exit code %d.", result.DurationMS, result.ExitCode)
	if result.TimedOut {
		sb.WriteString(" Timed out.")
	}
	if result.Stdout != "" {
		sb.WriteString("\n\nstdout:\n")
		sb.WriteString(redactOutput(result.Stdout))
		if result.StdoutTruncated {
			sb.WriteString("\n[stdout truncated]")
		}
	}
	if result.Stderr != "" {
		sb.WriteString("\n\nstderr:\n")
		sb.WriteString(redactOutput(result.Stderr))
		if result.StderrTruncated {
			sb.WriteString("\n[stderr truncated]")
		}
	}
	if result.Stdout == "" && result.Stderr == "" {
		sb.WriteString("\n\n(no output)")
	}
	return sb.String()
}

func redactOutput(s string) string {
	replacements := []string{
		"OPENROUTER_API_KEY=", "OPENROUTER_API_KEY=[redacted] ",
		"GLM_API_KEY=", "GLM_API_KEY=[redacted] ",
		"FAL_API_KEY=", "FAL_API_KEY=[redacted] ",
		"DISCORD_TOKEN=", "DISCORD_TOKEN=[redacted] ",
	}
	out := s
	for i := 0; i < len(replacements); i += 2 {
		out = strings.ReplaceAll(out, replacements[i], replacements[i+1])
	}
	return out
}

type bashLimiter struct {
	mu        sync.Mutex
	global    int
	perServer map[string]int
	perUser   map[string][]time.Time
}

var globalBashLimiter = &bashLimiter{
	perServer: make(map[string]int),
	perUser:   make(map[string][]time.Time),
}

func (l *bashLimiter) acquire(deps *BashDeps) (func(), string) {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if deps.PerUserRateLimit > 0 && deps.UserID != "" {
		key := deps.ServerID + ":" + deps.UserID
		cutoff := now.Add(-time.Minute)
		valid := l.perUser[key][:0]
		for _, ts := range l.perUser[key] {
			if ts.After(cutoff) {
				valid = append(valid, ts)
			}
		}
		if len(valid) >= deps.PerUserRateLimit {
			l.perUser[key] = valid
			return nil, "Bash rate limit reached for this user. Please wait before running another command."
		}
		l.perUser[key] = append(valid, now)
	}

	if deps.GlobalConcurrency > 0 && l.global >= deps.GlobalConcurrency {
		return nil, "Global bash concurrency limit reached. Please wait for another command to finish."
	}
	if deps.PerServerConcurrency > 0 && l.perServer[deps.ServerID] >= deps.PerServerConcurrency {
		return nil, "A bash command is already running for this server. Please wait for it to finish."
	}
	l.global++
	l.perServer[deps.ServerID]++
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.global--
		l.perServer[deps.ServerID]--
		if l.perServer[deps.ServerID] <= 0 {
			delete(l.perServer, deps.ServerID)
		}
	}, ""
}
