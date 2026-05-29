package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tomasmach/vespra/sandbox"
	"github.com/tomasmach/vespra/tools"
)

type fakeBashRunner struct {
	req    sandbox.RunRequest
	result sandbox.RunResult
	err    error
}

func (r *fakeBashRunner) Run(ctx context.Context, req sandbox.RunRequest) (sandbox.RunResult, error) {
	r.req = req
	return r.result, r.err
}

func TestBashExecToolRunsCommand(t *testing.T) {
	runner := &fakeBashRunner{result: sandbox.RunResult{
		ExitCode:   0,
		DurationMS: 12,
		Stdout:     "hello\n",
	}}
	reg := tools.NewRegistry()
	deps := &tools.BashDeps{
		Runner:               runner,
		ServerID:             "srv1",
		ChannelID:            "chan1",
		UserID:               "user1",
		MessageID:            "msg1",
		TimeoutSeconds:       3,
		MaxOutputBytes:       1000,
		MaxCommandBytes:      100,
		JobImage:             "vespra-bash-job:latest",
		VolumePrefix:         "vespra-test",
		EgressNetwork:        "vespra-bash-egress",
		GlobalConcurrency:    4,
		PerServerConcurrency: 1,
		PerUserRateLimit:     10,
	}
	reg = tools.NewDefaultRegistry(nil, "srv1", 0, 0, func(string) error { return nil }, func(string) error { return nil }, nil, deps, nil, 2)

	result, err := reg.Dispatch(context.Background(), tools.ToolNameBashExec, json.RawMessage(`{"command":"echo hello","reason":"test"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if !reg.BashCalled {
		t.Fatal("BashCalled should be true after bash_exec")
	}
	if runner.req.Command != "echo hello" {
		t.Fatalf("command = %q, want echo hello", runner.req.Command)
	}
	if !strings.Contains(result, "stdout") || !strings.Contains(result, "hello") {
		t.Fatalf("result should include stdout, got: %q", result)
	}
}

func TestBashExecToolRejectsEmptyCommand(t *testing.T) {
	reg := tools.NewDefaultRegistry(nil, "srv1", 0, 0, func(string) error { return nil }, func(string) error { return nil }, nil, &tools.BashDeps{Runner: &fakeBashRunner{}}, nil, 2)
	result, err := reg.Dispatch(context.Background(), tools.ToolNameBashExec, json.RawMessage(`{"command":"   "}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if !strings.Contains(result, "command is required") {
		t.Fatalf("expected command required error, got: %q", result)
	}
	if reg.BashCalled {
		t.Fatal("BashCalled should remain false for invalid command")
	}
}

func TestBashExecToolRejectsLongCommand(t *testing.T) {
	reg := tools.NewDefaultRegistry(nil, "srv1", 0, 0, func(string) error { return nil }, func(string) error { return nil }, nil, &tools.BashDeps{
		Runner:          &fakeBashRunner{},
		MaxCommandBytes: 5,
	}, nil, 2)
	result, err := reg.Dispatch(context.Background(), tools.ToolNameBashExec, json.RawMessage(`{"command":"echo too long"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if !strings.Contains(result, "max_command_bytes") {
		t.Fatalf("expected max_command_bytes error, got: %q", result)
	}
	if reg.BashCalled {
		t.Fatal("BashCalled should remain false for oversized command")
	}
}

func TestBashExecToolFormatsTimeoutAndTruncation(t *testing.T) {
	reg := tools.NewDefaultRegistry(nil, "srv1", 0, 0, func(string) error { return nil }, func(string) error { return nil }, nil, &tools.BashDeps{
		Runner: &fakeBashRunner{result: sandbox.RunResult{
			ExitCode:        -1,
			TimedOut:        true,
			DurationMS:      3000,
			Stderr:          "slow",
			StderrTruncated: true,
		}},
		MaxCommandBytes:      100,
		GlobalConcurrency:    4,
		PerServerConcurrency: 1,
		PerUserRateLimit:     10,
		ServerID:             "srv1",
	}, nil, 2)
	result, err := reg.Dispatch(context.Background(), tools.ToolNameBashExec, json.RawMessage(`{"command":"sleep 30"}`))
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	for _, want := range []string{"Timed out", "stderr", "truncated"} {
		if !strings.Contains(result, want) {
			t.Fatalf("result should contain %q, got: %q", want, result)
		}
	}
}
