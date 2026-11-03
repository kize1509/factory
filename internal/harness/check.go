package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Result kinds: what produced the run folder.
const (
	KindJob      = "job"
	KindScenario = "scenario"
	KindStatic   = "static"
	KindTests    = "tests"
)

// CheckOptions says how one patrol check runs. Everything is optional.
type CheckOptions struct {
	Commit   string        // the commit to check; default is the repo's HEAD
	Commands []string      // replaces the technique's default commands
	Timeout  time.Duration // per command; default CheckTimeout
	Cost     float64       // USD spent by the agent that asked, recorded as cost_usd
}

// CheckTimeout is how long one check command may run.
const CheckTimeout = 5 * time.Minute

// Techniques are the ones Check can run itself. explore and review need an
// agent and a browser, so they come later through the same Result.
var Techniques = []string{KindStatic, KindTests}

// DefaultCommands picks a technique's commands from what the repo contains.
func DefaultCommands(dir, technique string) ([]string, error) {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(dir, f)); return err == nil }
	switch technique {
	case KindStatic:
		switch {
		case has("go.mod"):
			return []string{"go vet ./..."}, nil
		case has("package.json"):
			return []string{"npm run lint --if-present"}, nil
		}
	case KindTests:
		switch {
		case has("go.mod"):
			return []string{"go test ./..."}, nil
		case has("package.json"):
			return []string{"npm test --if-present"}, nil
		}
	default:
		return nil, fmt.Errorf("unknown technique %q (want static or tests)", technique)
	}
	return nil, fmt.Errorf("no %s command known for this repo; pass --cmd", technique)
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// CheckID names a check's run folder: feature, technique, short commit and a
// timestamp, so two checks of the same cell never collide.
func CheckID(feature, technique, commit string, at time.Time) string {
	if len(commit) > 8 {
		commit = commit[:8]
	}
	return unsafeID.ReplaceAllString(fmt.Sprintf("chk-%s-%s-%s-%d", feature, technique, commit, at.UnixMilli()), "_")
}

// Check runs one technique on one feature in a clean worktree of repo at a
// fixed commit, and leaves a run folder <root>/.factory/runs/<id>/ with
// result.json and one log per command. The Result's pass is "every command
// exited 0"; the first failing command becomes failure. replay_cmd runs the
// same check on the same commit again. An error means the check could not
// run at all (bad repo, unknown technique), not that it failed.
func Check(ctx context.Context, root, repo, feature, technique string, o CheckOptions) (*Result, error) {
	if feature == "" {
		return nil, fmt.Errorf("check needs a feature")
	}
	repo, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	rev := o.Commit
	if rev == "" {
		rev = "HEAD"
	}
	commit := gitOut(repo, "rev-parse", "--verify", rev+"^{commit}")
	if commit == "" {
		return nil, fmt.Errorf("%s: no commit %q", repo, rev)
	}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = CheckTimeout
	}

	tmp, err := os.MkdirTemp("", "factory-check-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	wt := filepath.Join(tmp, "wt")
	if out, err := exec.CommandContext(ctx, "git", "-C", repo, "worktree", "add", "-q", "--detach", wt, commit).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("worktree at %s: %v: %s", commit, err, out)
	}
	defer func() {
		exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt).Run()
	}()

	cmds := o.Commands
	if len(cmds) == 0 {
		if cmds, err = DefaultCommands(wt, technique); err != nil {
			return nil, err
		}
	}
	id := CheckID(feature, technique, commit, time.Now())
	dir := RunDir(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		return nil, err
	}
	r := &Result{Run: id, Kind: technique, Feature: feature, Technique: technique, Commit: commit, Pass: true, Metrics: map[string]float64{}}
	r.ReplayCmd = fmt.Sprintf("factory test check %s --feature %s --technique %s --commit %s --root %s --json", shq(repo), shq(feature), technique, commit, shq(root))
	start := time.Now()
	for i, c := range cmds {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		cmd := exec.CommandContext(cctx, "sh", "-c", c)
		cmd.Dir = wt
		t0 := time.Now()
		out, runErr := cmd.CombinedOutput()
		took := time.Since(t0)
		timedOut := cctx.Err() == context.DeadlineExceeded
		cancelled := ctx.Err() != nil
		cancel()
		log := fmt.Sprintf("logs/%02d-%s.log", i+1, technique)
		body := fmt.Sprintf("$ %s\n%s", c, out)
		if err := os.WriteFile(filepath.Join(dir, log), []byte(body), 0o644); err != nil {
			return nil, err
		}
		r.Evidence.Logs = append(r.Evidence.Logs, log)
		r.Metrics[fmt.Sprintf("cmd%d_ms", i+1)] = float64(took.Milliseconds())
		ok := runErr == nil
		r.Steps = append(r.Steps, Step{Name: c, Pass: ok})
		if !ok && r.Failure == nil {
			r.Pass = false
			r.Failure = &Failure{Step: c, Expected: "exit 0", Actual: actualOf(runErr, timedOut, cancelled, timeout, string(out))}
		}
		if cancelled {
			break // the pass was stopped; no point running the rest
		}
	}
	r.Metrics["wall_ms"] = float64(time.Since(start).Milliseconds())
	r.Metrics["cost_usd"] = o.Cost
	return r, writeJSON(filepath.Join(dir, "result.json"), r)
}

func actualOf(err error, timedOut, cancelled bool, timeout time.Duration, out string) string {
	var head string
	switch {
	case timedOut:
		head = "timed out after " + timeout.String()
	case cancelled:
		head = "cancelled"
	default:
		if ee, ok := err.(*exec.ExitError); ok {
			head = fmt.Sprintf("exit %d", ee.ExitCode())
		} else {
			head = err.Error()
		}
	}
	return head + ":\n" + tail(out, 40)
}

// tail is the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// shq quotes a word for the shell when it needs it.
func shq(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`&|;<>()*?[]{}!#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
