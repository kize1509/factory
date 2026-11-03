package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"factory/internal/events"
	"factory/internal/harness/fixture"
	"factory/internal/harness/record"
	"factory/internal/harness/replay"
	"factory/internal/job"
	"factory/internal/runtime"
)

// Result is what every run, replay and (later) eval gives back. A failing
// one is already a bug report: failure says what broke, evidence says where
// to look, replay_cmd says how to reproduce it. Evidence paths are relative
// to the run folder.
type Result struct {
	Run       string             `json:"run"`
	Pass      bool               `json:"pass"`
	Steps     []Step             `json:"steps"`
	Failure   *Failure           `json:"failure,omitempty"`
	Evidence  Evidence           `json:"evidence"`
	Metrics   map[string]float64 `json:"metrics"`
	ReplayCmd string             `json:"replay_cmd"`
}

// Step is one thing the run was checked for.
type Step struct {
	Name string `json:"name"`
	Pass bool   `json:"pass"`
}

// Failure is the first step that failed.
type Failure struct {
	Step     string `json:"step"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// Evidence links to what the run left behind.
type Evidence struct {
	Events      string   `json:"events,omitempty"`
	Cassette    string   `json:"cassette,omitempty"`
	Handoff     string   `json:"handoff,omitempty"`
	Logs        []string `json:"logs,omitempty"`
	Screenshots []string `json:"screenshots,omitempty"`
	Video       string   `json:"video,omitempty"`
	Console     string   `json:"console,omitempty"`
	Network     string   `json:"network,omitempty"`
}

// Meta is what a replay needs to rebuild the job, and what the recording
// ended with. It lives in the run folder as meta.json.
type Meta struct {
	Spec     job.Spec `json:"spec"` // Repo is left empty: replay makes its own
	Fixture  string   `json:"fixture"`
	Base     string   `json:"base"` // the repo's commit when recorded
	Recorded Final    `json:"recorded"`
}

// Final is where a job ended up: what a replay must reproduce.
type Final struct {
	State  string   `json:"state"`
	Title  string   `json:"title"`
	Types  []string `json:"event_types"`
	Tree   string   `json:"worktree_tree"`
	Branch string   `json:"branch_tree"` // "" when nothing was committed
	Note   string   `json:"handoff_note"`
}

// Outcome is a finished job with what was measured on it.
type Outcome struct {
	Job      *job.Job
	Spec     job.Spec // as the job started, before the planner renamed it
	Repo     string
	Events   []events.Event
	Final    Final
	Duration time.Duration
	TimedOut bool          // the deadline, not the job, ended the run
	Timeout  time.Duration // the limit that was hit
}

// RunDir is where a run's evidence lives: <root>/.factory/runs/<id>.
func RunDir(root, id string) string { return filepath.Join(root, ".factory", "runs", id) }

// RunOptions is how a job is run. Timeout is how long it may take; zero means
// no limit beyond ctx. A live run takes minutes, so the caller says how many.
type RunOptions struct {
	Timeout time.Duration
}

// ScriptedTimeout is the limit for scripted and replayed jobs, which finish
// in about a second: a hang guard, not a budget.
const ScriptedTimeout = 30 * time.Second

// Execute puts sp (with sp.Repo set) through one whole job on the runtime mk
// builds. A job that gets stuck waits for guidance, so the run is cancelled
// as soon as it says so.
func Execute(ctx context.Context, sp job.Spec, ro RunOptions, mk func(j *job.Job, bus *events.Bus) runtime.Runtime) (*Outcome, error) {
	tmp, err := os.MkdirTemp("", "factory-harness-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	bus := events.NewBus(tmp)
	defer bus.Close()
	rt := &lateRT{}
	j := job.New(sp, bus, rt)
	started := j.Spec
	bus.Place(j.ID, j.Dir())
	rt.Runtime = mk(j, bus)

	var cancel context.CancelFunc
	if ro.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, ro.Timeout)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	ch, unsub := bus.Subscribe(j.ID)
	defer unsub()
	go func() {
		for e := range ch {
			if e.Type == "job.stuck" {
				cancel()
				return
			}
		}
	}()
	start := time.Now()
	j.Run(ctx)
	o := &Outcome{Job: j, Spec: started, Repo: sp.Repo, Duration: time.Since(start)}
	if ro.Timeout > 0 && ctx.Err() == context.DeadlineExceeded {
		o.TimedOut, o.Timeout = true, ro.Timeout
	}
	o.Events, _ = bus.Replay(j.ID)
	for _, e := range o.Events {
		o.Final.Types = append(o.Final.Types, e.Type)
	}
	o.Final.State, o.Final.Title = j.State, j.Title
	o.Final.Tree, _ = record.Snapshot(j.Worktree)
	if j.Commit != "" {
		o.Final.Branch = gitOut(sp.Repo, "rev-parse", j.Branch+"^{tree}")
	}
	b, _ := os.ReadFile(filepath.Join(j.Dir(), "handoff", "archimedes.json"))
	o.Final.Note = string(b)
	return o, nil
}

// lateRT lets the job be built before the runtime that needs the job.
type lateRT struct{ runtime.Runtime }

// Recording wraps the runtime inner builds so every turn goes into the
// job's cassette.
func Recording(inner func(j *job.Job, bus *events.Bus) runtime.Runtime) func(*job.Job, *events.Bus) runtime.Runtime {
	return func(j *job.Job, bus *events.Bus) runtime.Runtime {
		rec := record.New(inner(j, bus), bus, j.Dir())
		rec.Attach(j)
		return rec
	}
}

// Replaying plays the cassette under root back. out, if set, receives the
// runtime so the caller can ask it about drift and unplayed turns.
func Replaying(root string, out **replay.Runtime) func(*job.Job, *events.Bus) runtime.Runtime {
	return func(j *job.Job, bus *events.Bus) runtime.Runtime {
		rp := replay.New(root, bus)
		rp.Bind(j)
		if out != nil {
			*out = rp
		}
		return rp
	}
}

// Record runs sp (with sp.Repo set, a copy of the named fixture) on the
// runtime inner builds, recording a cassette, and leaves a run folder
// <root>/.factory/runs/<id>/ with events.jsonl, the cassette, the handoff
// note, meta.json and result.json. The Result's pass is "the job finished".
func Record(ctx context.Context, root, id string, sp job.Spec, fixtureName string, ro RunOptions, inner func(j *job.Job, bus *events.Bus) runtime.Runtime) (*Outcome, *Result, error) {
	o, err := Execute(ctx, sp, ro, Recording(inner))
	if err != nil {
		return nil, nil, err
	}
	dir := RunDir(root, id)
	if err := os.RemoveAll(dir); err != nil {
		return o, nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return o, nil, err
	}
	jd := o.Job.Dir()
	r := &Result{Run: id, Metrics: Metrics(o.Events, o.Duration), ReplayCmd: "factory test replay " + id + " --json"}
	if err := copyEvidence(&r.Evidence, jd, dir, o.Events); err != nil {
		return o, nil, err
	}
	spec := o.Spec
	spec.Repo = ""
	meta := Meta{Spec: spec, Fixture: fixtureName, Base: gitOut(sp.Repo, "rev-parse", "HEAD"), Recorded: o.Final}
	if err := writeJSON(filepath.Join(dir, "meta.json"), meta); err != nil {
		return o, nil, err
	}
	pass := o.Job.State == "done" && !o.TimedOut
	r.Steps = []Step{{Name: "job finishes in time", Pass: !o.TimedOut}, {Name: "job reaches done", Pass: o.Job.State == "done"}}
	r.Pass = pass
	if o.TimedOut {
		// the cassette stops where the deadline hit, so it can't replay to the end
		r.Failure = &Failure{Step: "job finishes in time", Expected: "finished within " + o.Timeout.String(), Actual: fmt.Sprintf("timed out after %s in state %s; the cassette is cut off there", o.Timeout, o.Job.State)}
	} else if !pass {
		actual := "state " + o.Job.State
		if o.Job.Error != "" {
			actual += ": " + o.Job.Error
		} else if o.Job.Ask != "" {
			actual += ": " + o.Job.Ask
		}
		r.Failure = &Failure{Step: "job reaches done", Expected: "state done", Actual: actual}
	}
	return o, r, writeJSON(filepath.Join(dir, "result.json"), r)
}

// copyEvidence puts a job's events, cassette, handoff notes and resume note
// into the run folder and links them from ev.
func copyEvidence(ev *Evidence, jobDir, dir string, evs []events.Event) error {
	if err := writeEvents(dir, evs); err != nil {
		return err
	}
	if len(evs) > 0 {
		ev.Events = "events.jsonl"
	}
	for _, c := range []struct {
		name string
		dst  *string
	}{{"cassette", &ev.Cassette}, {"handoff", &ev.Handoff}} {
		if _, err := os.Stat(filepath.Join(jobDir, c.name)); err == nil {
			if err := copyTree(filepath.Join(jobDir, c.name), filepath.Join(dir, c.name)); err != nil {
				return err
			}
			*c.dst = c.name
		}
	}
	if _, err := os.Stat(filepath.Join(jobDir, "resume.md")); err == nil {
		if err := copyTree(filepath.Join(jobDir, "resume.md"), filepath.Join(dir, "logs", "resume.md")); err != nil {
			return err
		}
		ev.Logs = append(ev.Logs, "logs/resume.md")
	}
	return nil
}

func writeEvents(dir string, evs []events.Event) error {
	if len(evs) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range evs {
		enc.Encode(e)
	}
	return os.WriteFile(filepath.Join(dir, "events.jsonl"), buf.Bytes(), 0o644)
}

// Results reads the result.json of a recorded run.
func Results(root, id string) (*Result, error) {
	return readResult(filepath.Join(RunDir(root, id), "result.json"))
}

func readResult(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no result for this run: %w", err)
	}
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, nil
}

// ReplayOptions says what to replay a run against. With neither field set
// the run's own fixture is rebuilt. Repo is a git repo to clone, At the
// commit to check out in the clone: replay the same cassette on a repo at
// any commit.
type ReplayOptions struct {
	Repo    string
	At      string
	Timeout time.Duration // zero means ScriptedTimeout: replays are fast
}

// Replay plays the run's cassette against the current engine and a repo, and
// says whether the job ended where the recording did. The Result is also
// written to <run>/replay/result.json, with the replayed events beside it.
// A prompt that no longer matches fails the result with the drift diff.
func Replay(ctx context.Context, root, id string, o ReplayOptions) (*Result, error) {
	dir := RunDir(root, id)
	var meta Meta
	b, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return nil, fmt.Errorf("run %q has no meta.json: %w", id, err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "factory-replay-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	repo, err := replayRepo(tmp, meta, o)
	if err != nil {
		return nil, err
	}
	sp := meta.Spec
	sp.Repo = repo

	timeout := o.Timeout
	if timeout == 0 {
		timeout = ScriptedTimeout
	}
	var rp *replay.Runtime
	out, err := Execute(ctx, sp, RunOptions{Timeout: timeout}, Replaying(dir, &rp))
	if err != nil {
		return nil, err
	}
	r := &Result{Run: id, Metrics: Metrics(out.Events, out.Duration), ReplayCmd: replayCmd(id, o)}
	rec, got := meta.Recorded, out.Final
	var steps []Step
	var fails []Failure
	check := func(name string, ok bool, expected, actual string) {
		steps = append(steps, Step{Name: name, Pass: ok})
		if !ok {
			fails = append(fails, Failure{Step: name, Expected: expected, Actual: actual})
		}
	}
	// the repo first: a changed fixture shows up as drift or a tree mismatch
	// below, and this says why. With --repo/--at the commit is chosen, so it
	// is only reported.
	if now := gitOut(repo, "rev-parse", "HEAD"); o.Repo == "" && meta.Base != "" {
		check("repo commit matches the recording", now == meta.Base, meta.Base, now+" (the fixture "+meta.Fixture+" changed since this run was recorded)")
	} else {
		steps = append(steps, Step{Name: "repo commit (information: " + now + ", recorded " + meta.Base + ")", Pass: true})
	}
	if out.TimedOut {
		check("job finishes in time", false, "finished within "+out.Timeout.String(), "timed out after "+out.Timeout.String())
	}
	if d := rp.Drift(); d != nil {
		check(fmt.Sprintf("prompt matches %s turn %d", d.Agent, d.N), false, "the prompt as recorded", "the prompt differs (- recorded, + now):\n"+d.Diff)
	} else {
		check("prompts match the recording", true, "", "")
	}
	check("job state", rec.State == got.State, rec.State, got.State+errorSuffix(out.Job))
	check("job title", rec.Title == got.Title, rec.Title, got.Title)
	check("event types", reflect.DeepEqual(rec.Types, got.Types), strings.Join(rec.Types, " "), strings.Join(got.Types, " "))
	check("worktree tree", rec.Tree == got.Tree, rec.Tree, got.Tree)
	check("job branch tree", rec.Branch == got.Branch, rec.Branch, got.Branch)
	check("handoff note", rec.Note == got.Note, rec.Note, got.Note)
	left := rp.Unplayed()
	check("all recorded turns played", len(left) == 0, "none left", strings.Join(left, " "))
	r.Steps = steps
	r.Pass = len(fails) == 0
	if !r.Pass {
		r.Failure = &fails[0]
	}

	rdir := filepath.Join(dir, "replay")
	if err := os.RemoveAll(rdir); err != nil {
		return r, err
	}
	if err := os.MkdirAll(rdir, 0o755); err != nil {
		return r, err
	}
	if err := writeEvents(rdir, out.Events); err != nil {
		return r, err
	}
	r.Evidence = Evidence{Events: "replay/events.jsonl", Cassette: "cassette"}
	return r, writeJSON(filepath.Join(rdir, "result.json"), r)
}

func errorSuffix(j *job.Job) string {
	if j.Error != "" {
		return ": " + j.Error
	}
	return ""
}

func replayCmd(id string, o ReplayOptions) string {
	c := "factory test replay " + id
	if o.Repo != "" {
		c += " --repo " + o.Repo
	}
	if o.At != "" {
		c += " --at " + o.At
	}
	return c + " --json"
}

// replayRepo makes the repo the replay runs in.
func replayRepo(tmp string, meta Meta, o ReplayOptions) (string, error) {
	if o.Repo == "" {
		if o.At != "" {
			return "", fmt.Errorf("--at needs --repo: the repo to check the commit out of")
		}
		if meta.Fixture == "" {
			return "", fmt.Errorf("the run has no fixture; pass --repo")
		}
		return fixture.Make(tmp, meta.Fixture)
	}
	dst := filepath.Join(tmp, "repo")
	if out, err := exec.Command("git", "clone", "-q", o.Repo, dst).CombinedOutput(); err != nil {
		return "", fmt.Errorf("clone %s: %v: %s", o.Repo, err, out)
	}
	if o.At != "" {
		if out, err := exec.Command("git", "-C", dst, "checkout", "-q", "-B", "main", o.At).CombinedOutput(); err != nil {
			return "", fmt.Errorf("check out %s: %v: %s", o.At, err, out)
		}
	}
	return dst, nil
}

// Metrics are the factory numbers that can be read straight from a job's
// events: heal loops (checks.failed), wall time (first to last event, or
// elapsed when there are no events), and cost, tokens and model calls when
// usage events exist. Cost is only set when usage was recorded; a replay has
// none, so it reports what the recording had.
func Metrics(evs []events.Event, elapsed time.Duration) map[string]float64 {
	m := map[string]float64{"events": float64(len(evs)), "heal_loops": 0, "wall_ms": float64(elapsed.Milliseconds())}
	if len(evs) > 1 {
		m["wall_ms"] = float64(evs[len(evs)-1].Time.Sub(evs[0].Time).Milliseconds())
	}
	var usd, in, out, calls float64
	for _, e := range evs {
		switch e.Type {
		case "checks.failed":
			m["heal_loops"]++
		case "usage":
			calls++
			usd += num(e.Data["usd"])
			in += num(e.Data["input"])
			out += num(e.Data["output"])
		}
	}
	if calls > 0 {
		m["cost_usd"], m["tokens_in"], m["tokens_out"], m["model_calls"] = usd, in, out, calls
	}
	return m
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func gitOut(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// copyTree copies a file or a folder.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}
