package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

type DockerExecutor struct {
	DockerPath string
}

func (e *DockerExecutor) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if strings.TrimSpace(req.ServerID) == "" {
		return RunResult{}, fmt.Errorf("server_id is required")
	}
	if strings.HasPrefix(req.ServerID, "DM:") {
		return RunResult{}, fmt.Errorf("bash is disabled for DMs")
	}
	if strings.TrimSpace(req.Command) == "" {
		return RunResult{}, fmt.Errorf("command is required")
	}
	if req.MaxCommandBytes > 0 && len(req.Command) > req.MaxCommandBytes {
		return RunResult{}, fmt.Errorf("command exceeds max_command_bytes")
	}
	if req.JobImage == "" {
		return RunResult{}, fmt.Errorf("job_image is required")
	}
	if req.VolumePrefix == "" {
		return RunResult{}, fmt.Errorf("volume_prefix is required")
	}
	if req.EgressNetwork == "" {
		return RunResult{}, fmt.Errorf("egress_network is required")
	}
	if req.EgressNetwork == "host" {
		return RunResult{}, fmt.Errorf("host network is not allowed")
	}

	docker := e.DockerPath
	if docker == "" {
		docker = "docker"
	}
	workspaceVolume := volumeName(req.VolumePrefix, req.ServerID)
	if err := exec.CommandContext(ctx, docker, "volume", "create", workspaceVolume).Run(); err != nil {
		return RunResult{}, fmt.Errorf("create workspace volume: %w", err)
	}
	if err := ensureNetwork(ctx, docker, req.EgressNetwork); err != nil {
		return RunResult{}, err
	}

	containerName := "vespra-bash-job-" + randomSuffix()
	timeout := TimeoutDuration(req.TimeoutSeconds)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	defer exec.CommandContext(context.Background(), docker, "rm", "-f", containerName).Run()

	args := []string{
		"run",
		"--name", containerName,
		"--workdir", "/workspace",
		"--user", "1000:1000",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges:true",
		"--network", req.EgressNetwork,
		"--mount", "type=volume,src=" + workspaceVolume + ",dst=/workspace",
		"--tmpfs", "/tmp:rw,nosuid,nodev,size=64m",
		"--rm",
	}
	if req.Memory != "" {
		args = append(args, "--memory", req.Memory)
	}
	if req.CPUs != "" {
		args = append(args, "--cpus", req.CPUs)
	}
	if req.PidsLimit > 0 {
		args = append(args, "--pids-limit", fmt.Sprintf("%d", req.PidsLimit))
	}
	args = append(args, req.JobImage, "bash", "-lc", req.Command)

	stdout := newCappedBuffer(req.MaxOutputBytes)
	stderr := newCappedBuffer(req.MaxOutputBytes)
	cmd := exec.CommandContext(runCtx, docker, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	duration := time.Since(start)

	result := RunResult{
		ExitCode:        exitCode(err),
		TimedOut:        errors.Is(runCtx.Err(), context.DeadlineExceeded),
		DurationMS:      duration.Milliseconds(),
		Stdout:          stdout.String(),
		Stderr:          stderr.String(),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
		ContainerID:     containerName,
	}
	if err != nil && !isExitError(err) && !result.TimedOut {
		return result, fmt.Errorf("run docker job: %w", err)
	}
	return result, nil
}

func ensureNetwork(ctx context.Context, docker, name string) error {
	if err := exec.CommandContext(ctx, docker, "network", "inspect", name).Run(); err == nil {
		return nil
	}
	if err := exec.CommandContext(ctx, docker, "network", "create", name).Run(); err != nil {
		return fmt.Errorf("create egress network: %w", err)
	}
	return nil
}

func volumeName(prefix, serverID string) string {
	prefix = safeName(prefix)
	if prefix == "" {
		prefix = "vespra-bash-workspace"
	}
	return prefix + "-" + safeName(serverID)
}

func safeName(s string) string {
	s = strings.Trim(safeNameRe.ReplaceAllString(s, "-"), "-.")
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

func randomSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}

func isExitError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr)
}

type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer {
	if limit <= 0 {
		limit = 32 * 1024
	}
	return &cappedBuffer{limit: limit}
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *cappedBuffer) String() string {
	return b.buf.String()
}

func (b *cappedBuffer) Truncated() bool {
	return b.truncated
}
