// Package checks runs the project's own check commands (tests, e2e, lint)
// in a job's worktree and reports what failed.
package checks

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"factory/internal/events"
)

type Result struct {
	Command  string        `json:"command"`
	Passed   bool          `json:"passed"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration"`
	Output   string        `json:"output"` // last lines only
}

// Run runs every command in order and returns all results. Each command
// gets its own process group, so stopping it stops its children too.
func Run(ctx context.Context, bus *events.Bus, job, agent, dir string, commands []string, timeout time.Duration) []Result {
	var out []Result
	for _, c := range commands {
		out = append(out, runOne(ctx, bus, job, agent, dir, c, timeout))
	}
	return out
}

func runOne(ctx context.Context, bus *events.Bus, job, agent, dir, command string, timeout time.Duration) Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	start := time.Now()
	bus.Emit(job, agent, "check.started", map[string]any{"command": command})
	err := cmd.Start()
	if err == nil {
		bus.Emit(job, agent, "proc.started", map[string]any{"pid": cmd.Process.Pid, "command": command, "kind": "proc"})
		err = cmd.Wait()
	}
	r := Result{Command: command, Duration: time.Since(start), Output: tail(buf.String(), 120)}
	if cmd.ProcessState != nil {
		r.ExitCode = cmd.ProcessState.ExitCode()
	} else {
		r.ExitCode = -1
	}
	r.Passed = err == nil
	data := map[string]any{"command": command, "exit_code": r.ExitCode, "ms": r.Duration.Milliseconds(), "output": r.Output}
	if cmd.Process != nil {
		bus.Emit(job, agent, "proc.exited", map[string]any{"pid": cmd.Process.Pid, "exit_code": r.ExitCode})
	}
	if r.Passed {
		bus.Emit(job, agent, "check.passed", data)
	} else {
		bus.Emit(job, agent, "check.failed", data)
	}
	return r
}

// Failures formats failed results as input for the builder's next turn.
func Failures(results []Result) string {
	var b strings.Builder
	for _, r := range results {
		if r.Passed {
			continue
		}
		fmt.Fprintf(&b, "Check failed: `%s` (exit %d)\n```\n%s\n```\n", r.Command, r.ExitCode, r.Output)
	}
	return b.String()
}

func AllPassed(results []Result) bool {
	for _, r := range results {
		if !r.Passed {
			return false
		}
	}
	return true
}

func tail(s string, lines int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "\n")
}
