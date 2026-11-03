package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/harness/fixture"
	"factory/internal/harness/record"
	"factory/internal/harness/replay"
	"factory/internal/job"
	"factory/internal/runtime"
)

// step is what a scripted agent sees on one turn.
type step struct {
	job  *job.Job
	cwd  string
	turn int // 1-based, per agent
	text string
}

// scripted is a runtime whose agents follow a script: one function per
// agent name. It stands in for a model when a scenario is recorded.
type scripted struct {
	job    *job.Job
	bus    *events.Bus
	script map[string]func(s step) string
	turns  map[string]int
}

func (r *scripted) Start(_ context.Context, spec runtime.AgentSpec) (runtime.Agent, error) {
	r.bus.Emit(spec.Job, spec.Agent, "agent.started", map[string]any{"runtime": "scripted"})
	return &scriptedAgent{r: r, spec: spec}, nil
}

type scriptedAgent struct {
	r    *scripted
	spec runtime.AgentSpec
}

func (a *scriptedAgent) Prompt(_ context.Context, text string) (string, error) {
	a.r.turns[a.spec.Agent]++
	f := a.r.script[a.spec.Agent]
	if f == nil {
		return "", nil
	}
	return f(step{job: a.r.job, cwd: a.spec.Cwd, turn: a.r.turns[a.spec.Agent], text: text}), nil
}
func (a *scriptedAgent) Steer(string) error { return nil }
func (a *scriptedAgent) Stop() error        { return nil }

func write(s step, file, body string) { os.WriteFile(filepath.Join(s.cwd, file), []byte(body), 0o644) }

func planner(s step) string {
	s.job.Name("aristotle", "Say done in app.txt", "Hello App")
	return "plan: write done into app.txt"
}

func note(s step) {
	s.job.SaveHandoff("archimedes", &handoff.Note{From: "archimedes", To: "reviewers", Did: "app.txt now says done", Next: "review", Files: []string{"app.txt"}})
}

// healing: breaks the check first, fixes it when the checks fail, and only
// writes the handoff after the engine reminds it.
var healing = map[string]func(step) string{
	"aristotle": planner,
	"archimedes": func(s step) string {
		switch {
		case strings.HasPrefix(s.text, "Ticket"):
			write(s, "app.txt", "half done\n")
		case strings.HasPrefix(s.text, "The checks failed"):
			write(s, "app.txt", "done\n")
		case strings.HasPrefix(s.text, "Your handoff is missing"):
			note(s)
		}
		return "ok"
	},
}

// stubborn: never fixes the check, so the same failure repeats.
var stubborn = map[string]func(step) string{
	"aristotle": planner,
	"archimedes": func(s step) string {
		if strings.HasPrefix(s.text, "Ticket") {
			write(s, "app.txt", "half done\n")
		}
		if strings.HasPrefix(s.text, "The engine can't get this job further") {
			return "I'm stuck on the check: app.txt never says done."
		}
		return "tried"
	},
}

// silent: does the work but never writes a handoff.
var silent = map[string]func(step) string{
	"aristotle": planner,
	"archimedes": func(s step) string {
		if strings.HasPrefix(s.text, "Ticket") {
			write(s, "app.txt", "done\n")
		}
		return "done, I think"
	},
}

type result struct {
	job      *job.Job
	repo     string
	types    []string
	tree     string // the worktree's content when the run ended
	branch   string // the job branch's tree, "" when nothing was committed
	note     string
	dir      string
	duration time.Duration
}

func spec(prompt string) job.Spec {
	return job.Spec{ID: "E2E-1", Title: "make app", Repo: "", Prompt: prompt, MaxLoops: 3,
		Checks: []string{"grep -q '^done$' app.txt"}}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// run puts the fixture repo through one whole job with the runtime mk builds.
func run(t *testing.T, sp job.Spec, mk func(j *job.Job, bus *events.Bus) runtime.Runtime) result {
	t.Helper()
	sp.Repo = fixture.New(t, "hello")
	return fromOutcome(t, sp.Repo, mk, sp)
}

func fromOutcome(t *testing.T, repo string, mk func(j *job.Job, bus *events.Bus) runtime.Runtime, sp job.Spec) result {
	o, err := Execute(context.Background(), sp, mk)
	if err != nil {
		t.Fatal(err)
	}
	return result{job: o.Job, repo: repo, types: o.Final.Types, tree: o.Final.Tree, branch: o.Final.Branch, note: o.Final.Note, dir: o.Job.Dir(), duration: o.Duration}
}

func recording(script map[string]func(step) string) func(*job.Job, *events.Bus) runtime.Runtime {
	return Recording(func(j *job.Job, bus *events.Bus) runtime.Runtime {
		return &scripted{job: j, bus: bus, script: script, turns: map[string]int{}}
	})
}

func replaying(root string, out **replay.Runtime) func(*job.Job, *events.Bus) runtime.Runtime {
	return Replaying(root, out)
}

// same checks that a replay reproduced a recording.
func same(t *testing.T, rec, rep result) {
	t.Helper()
	if !reflect.DeepEqual(rec.types, rep.types) {
		t.Errorf("event types differ\nrecorded: %v\nreplayed: %v", rec.types, rep.types)
	}
	if rec.tree != rep.tree {
		t.Errorf("worktree tree differs: %s vs %s", rec.tree, rep.tree)
	}
	if rec.branch != rep.branch {
		t.Errorf("job branch tree differs: %q vs %q", rec.branch, rep.branch)
	}
	if rec.note != rep.note {
		t.Errorf("handoff note differs\nrecorded: %s\nreplayed: %s", rec.note, rep.note)
	}
	if rec.job.State != rep.job.State || rec.job.Title != rep.job.Title {
		t.Errorf("state/title differ: %s %q vs %s %q", rec.job.State, rec.job.Title, rep.job.State, rep.job.Title)
	}
}

func count(types []string, want string) int {
	n := 0
	for _, ty := range types {
		if ty == want {
			n++
		}
	}
	return n
}

func TestRecordAndReplayHealingJob(t *testing.T) {
	rec := run(t, spec("write done into app.txt"), recording(healing))
	j := rec.job
	if j.State != "done" || j.Commit == "" {
		t.Fatalf("recorded job: state %s commit %q error %q", j.State, j.Commit, j.Error)
	}
	for _, want := range []string{"agent.started", "job.named", "checks.failed", "checks.passed", "handoff.missing", "handoff.written", "job.committed", "job.done"} {
		if count(rec.types, want) == 0 {
			t.Errorf("recorded run has no %s event: %v", want, rec.types)
		}
	}
	if got := git(t, rec.repo, "show", j.Branch+":app.txt"); got != "done" {
		t.Errorf("app.txt on %s = %q", j.Branch, got)
	}
	if got := git(t, rec.repo, "log", "-1", "--format=%s", j.Branch); got != "E2E-1: Say done in app.txt" {
		t.Errorf("commit message %q", got)
	}
	if !strings.Contains(rec.note, "app.txt now says done") {
		t.Errorf("handoff note: %s", rec.note)
	}
	for _, f := range []string{"aristotle/1.json", "archimedes/1.json", "archimedes/2.json", "archimedes/3.json"} {
		if _, err := os.Stat(filepath.Join(rec.dir, "cassette", f)); err != nil {
			t.Errorf("no cassette file %s", f)
		}
	}

	var rp *replay.Runtime
	rep := run(t, spec("write done into app.txt"), replaying(rec.dir, &rp))
	same(t, rec, rep)
	if rep.job.State != "done" {
		t.Errorf("replayed job: state %s error %q", rep.job.State, rep.job.Error)
	}
	if left := rp.Unplayed(); len(left) > 0 {
		t.Errorf("replay left turns unplayed: %v", left)
	}
	if rep.duration > 10*time.Second { // only a hang; the speed is logged below, not asserted
		t.Errorf("replay took %s", rep.duration)
	}
	t.Logf("recorded in %s, replayed in %s", rec.duration, rep.duration)
}

func TestStuckOnChecksThatKeepFailing(t *testing.T) {
	rec := run(t, spec("write done into app.txt"), recording(stubborn))
	if rec.job.State != "stuck" || rec.job.Commit != "" {
		t.Fatalf("state %s commit %q", rec.job.State, rec.job.Commit)
	}
	if count(rec.types, "checks.same_failure") == 0 || count(rec.types, "job.stuck") != 1 || count(rec.types, "job.committed") != 0 {
		t.Fatalf("events: %v", rec.types)
	}
	if !strings.Contains(rec.job.Ask, "I'm stuck on the check") {
		t.Errorf("the agent's plea is missing: %q", rec.job.Ask)
	}
	if _, err := os.Stat(filepath.Join(rec.dir, "resume.md")); err != nil {
		t.Error("a stuck job should leave resume.md")
	}
	rep := run(t, spec("write done into app.txt"), replaying(rec.dir, nil))
	same(t, rec, rep)
}

func TestStuckOnAgentThatNeverWritesHandoff(t *testing.T) {
	rec := run(t, spec("write done into app.txt"), recording(silent))
	if rec.job.State != "stuck" || rec.job.Commit != "" {
		t.Fatalf("state %s commit %q", rec.job.State, rec.job.Commit)
	}
	if n := count(rec.types, "handoff.missing"); n != 2 {
		t.Fatalf("want 2 handoff reminders, got %d: %v", n, rec.types)
	}
	if rec.note != "" {
		t.Errorf("no handoff should exist, got %s", rec.note)
	}
	rep := run(t, spec("write done into app.txt"), replaying(rec.dir, nil))
	same(t, rec, rep)
}

// A prompt that changed since the recording makes the replay fail, and says how.
func TestReplayFailsWhenPromptDrifts(t *testing.T) {
	rec := run(t, spec("write done into app.txt"), recording(healing))
	rep := run(t, spec("write finished into app.txt"), replaying(rec.dir, nil))
	if rep.job.State == "done" {
		t.Fatal("replay should not finish a job whose prompt changed")
	}
	msg := rep.job.Error
	for _, want := range []string{"replay drift", "aristotle turn 1", "- write done into app.txt", "+ write finished into app.txt"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	// the same through the Agent seam, without a job
	rp := replay.New(rec.dir, nil)
	a, _ := rp.Start(context.Background(), runtime.AgentSpec{Agent: "aristotle", Cwd: t.TempDir()})
	_, err := a.Prompt(context.Background(), "something else")
	var d *replay.Drift
	if !errors.As(err, &d) || d.Agent != "aristotle" || d.N != 1 {
		t.Fatalf("want a Drift for aristotle turn 1, got %v", err)
	}
}

// A cassette keeps the tool calls, the diff and the events of each turn.
func TestCassetteHoldsTheTurn(t *testing.T) {
	rec := run(t, spec("write done into app.txt"), recording(healing))
	turns, err := record.Load(rec.dir, "archimedes")
	if err != nil || len(turns) != 3 {
		t.Fatalf("turns %d, err %v", len(turns), err)
	}
	if !strings.Contains(turns[0].Diff, "+half done") || turns[0].TreeBefore == turns[0].TreeAfter {
		t.Errorf("turn 1 diff: %q", turns[0].Diff)
	}
	if len(turns[2].Calls) != 1 || turns[2].Calls[0].Tool != "handoff" {
		t.Errorf("turn 3 calls: %+v", turns[2].Calls)
	}
	var n handoff.Note
	if json.Unmarshal(turns[2].Calls[0].Args, &n) != nil || n.Did == "" {
		t.Errorf("handoff args: %s", turns[2].Calls[0].Args)
	}
	if len(turns[0].StartEvents) != 1 || turns[0].StartEvents[0].Type != "agent.started" {
		t.Errorf("turn 1 should hold the Start's agent.started: %+v", turns[0].StartEvents)
	}
	if len(turns[1].StartEvents) != 0 {
		t.Errorf("turn 2 has no Start of its own: %+v", turns[1].StartEvents)
	}
}

// A recorded run leaves a run folder whose evidence exists; results reads it;
// replay passes on it, and fails with the drift when a prompt has changed.
func TestRunFolderResultsAndReplay(t *testing.T) {
	root := t.TempDir()
	if r := os.Getenv("FACTORY_RUNS_ROOT"); r != "" { // keep the run folder for the CLI to read
		root = r
	}
	sp := spec("write done into app.txt")
	sp.Repo = fixture.New(t, "hello")
	_, res, err := Record(context.Background(), root, "run-1", sp, "hello", func(j *job.Job, bus *events.Bus) runtime.Runtime {
		return &scripted{job: j, bus: bus, script: healing, turns: map[string]int{}}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Pass || res.Metrics["heal_loops"] != 1 {
		t.Fatalf("recorded result: %+v", res)
	}
	got, err := Results(root, "run-1")
	if err != nil || !got.Pass || got.Run != "run-1" {
		t.Fatalf("results: %+v, %v", got, err)
	}
	dir := RunDir(root, "run-1")
	for _, p := range []string{got.Evidence.Events, got.Evidence.Cassette, got.Evidence.Handoff, "meta.json"} {
		if p == "" || filepath.IsAbs(p) {
			t.Errorf("evidence path %q should be set and relative", p)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Errorf("evidence %s does not exist", p)
		}
	}

	rep, err := Replay(context.Background(), root, "run-1", ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Pass || rep.Failure != nil {
		t.Fatalf("replay should pass: %+v", rep.Failure)
	}
	if _, err := os.Stat(filepath.Join(dir, rep.Evidence.Events)); err != nil {
		t.Errorf("replay events missing: %v", err)
	}

	// change a prompt the engine sends (the ticket prompt in the recorded spec)
	var meta Meta
	b, _ := os.ReadFile(filepath.Join(dir, "meta.json"))
	defer os.WriteFile(filepath.Join(dir, "meta.json"), b, 0o644) // leave the run as recorded
	json.Unmarshal(b, &meta)
	meta.Spec.Prompt = "write finished into app.txt"
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
	rep, err = Replay(context.Background(), root, "run-1", ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pass || rep.Failure == nil {
		t.Fatalf("replay should fail on drift: %+v", rep)
	}
	for _, want := range []string{"aristotle", "- write done into app.txt", "+ write finished into app.txt"} {
		if !strings.Contains(rep.Failure.Step+rep.Failure.Actual, want) {
			t.Errorf("failure lacks %q: %+v", want, rep.Failure)
		}
	}
}
