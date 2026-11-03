package patrol

import (
	"context"
	"fmt"

	"factory/internal/prompts"
	"factory/internal/runtime"
)

// PromptFor is what a patrol agent is told. It names the exact shell command
// and the one tool to call after it.
func PromptFor(t *Task) string {
	return fmt.Sprintf(`You are patrol agent %s in pass %s. Your cell: feature %q, technique %q, commit %s.

1. Run exactly this in the shell, from the project folder:
   factory test check %s --feature %s --technique %s --commit %s --root %s --json
   It exits 0 when the check passes and 1 when it fails. Read the JSON it prints: run, pass, failure{step, expected, actual}, evidence.logs, replay_cmd.
2. Look at the logs it points to (under %s/.factory/runs/<run>/). Decide: ok (it passed and nothing looks wrong), finding (it failed, or you saw something wrong), or blocked (the check could not tell you anything, say why).
3. Call record_check with run (the run id from the JSON), verdict and a one-line note. A failed run is a finding, not ok.

Do not fix anything and do not edit files. Do not run the check twice unless the first run was blocked.`,
		t.Agent, t.Pass, t.Feature.ID, t.Technique, t.Commit,
		t.Repo, t.Feature.ID, t.Technique, t.Commit, t.Root, t.Root)
}

// RuntimeAgent runs a patrol agent on a runtime (Pi, Claude, Codex): it
// starts the agent, gives it PromptFor, and stops it when ctx ends. record_check
// reaches the pass through the engine's tool endpoint.
type RuntimeAgent struct {
	RT    runtime.Runtime
	Model string
	Cost  func(pass, agent string) float64 // USD spent so far by the agent; may be nil
}

func (r RuntimeAgent) Patrol(ctx context.Context, t *Task) (float64, error) {
	a, err := r.RT.Start(ctx, runtime.AgentSpec{Job: "pass-" + t.Pass, Agent: t.Agent, Cwd: t.Repo, Model: r.Model,
		System: prompts.For("patrol"), Role: "patrol", Tools: []string{"bash", "read"}})
	if err != nil {
		return 0, fmt.Errorf("start %s: %w", t.Agent, err)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			a.Stop() // the kill switch reaches the agent within a moment
		case <-done:
		}
	}()
	_, err = a.Prompt(ctx, PromptFor(t))
	defer a.Stop()
	var cost float64
	if r.Cost != nil {
		cost = r.Cost(t.Pass, t.Agent)
	}
	return cost, err
}

func headCommit(repo string) (string, error) {
	out, err := gitHead(repo)
	if err != nil {
		return "", fmt.Errorf("%s: not a git repo with a commit: %w", repo, err)
	}
	return out, nil
}
