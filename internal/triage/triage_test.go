package triage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/ops"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/youtrack"
)

// scriptRT plays the product agent: each turn runs the next batch of tool
// calls against the run and records what came back.
type scriptRT struct {
	run   *Run
	turns [][]call
	got   []string
	spec  runtime.AgentSpec
}

type call struct {
	tool string
	args string
}

func (f *scriptRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	f.spec = s
	return &scriptAgent{rt: f}, nil
}

type scriptAgent struct {
	rt   *scriptRT
	turn int
}

func (a *scriptAgent) Prompt(_ context.Context, text string) (string, error) {
	if a.turn >= len(a.rt.turns) {
		return "ok", nil
	}
	for _, c := range a.rt.turns[a.turn] {
		out, err := a.rt.run.Tool(Pericles, c.tool, []byte(c.args))
		if err != nil {
			out = "error: " + err.Error()
		}
		a.rt.got = append(a.rt.got, c.tool+" → "+out)
	}
	a.turn++
	return "ok", nil
}
func (a *scriptAgent) Steer(string) error { return nil }
func (a *scriptAgent) Stop() error        { return nil }

type fakeYT struct {
	mu       sync.Mutex
	comments []string
	srv      *httptest.Server
}

func newYT(t *testing.T) *fakeYT {
	f := &fakeYT{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/comments") && r.Method == "POST" {
			var c struct{ Text string }
			json.NewDecoder(r.Body).Decode(&c)
			f.comments = append(f.comments, r.URL.Path+": "+c.Text)
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func setup(t *testing.T, level string, maxJobs int, turns [][]call) (*Run, *scriptRT, *fakeYT, *[]string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	yt := newYT(t)
	bus := events.NewBus(t.TempDir())
	svc := &ops.Service{Store: db, Bus: bus, Client: func() youtrack.Client { return youtrack.Client{URL: yt.srv.URL, Token: "perm:x"} }}
	rt := &scriptRT{turns: turns}
	var started []string
	run := &Run{ID: "T1", Repo: t.TempDir(), Project: "HF", Level: level, MaxJobs: maxJobs, RT: rt, Model: "m", Ops: svc, Bus: bus, Dir: t.TempDir(),
		Live: func() []LiveJob { return []LiveJob{{ID: "HF-3", Issue: "HF-3", State: "building"}} },
		Start: func(issue, notes string) (string, error) {
			started = append(started, issue+": "+notes)
			return issue, nil
		}}
	rt.run = run
	bus.Place(run.Scope(), run.Dir)
	svc.RegisterBoard(run.Scope(), run.Repo, "HF", level, Pericles)
	return run, rt, yt, &started
}

func TestTriageAsksStartsAndReports(t *testing.T) {
	run, rt, yt, started := setup(t, "fix", 1, [][]call{{
		{"ask_owner", `{"issue":"HF-4","question":"Should the export include archived rows? I'd leave them out."}`},
		{"start_job", `{"issue":"HF-3","why":"ready"}`},                          // a job is already on it
		{"start_job", `{"issue":"OTHER-1","why":"ready"}`},                       // another project
		{"start_job", `{"issue":"hf-5","why":"top priority","notes":"cart.go"}`}, // the one it may start
		{"start_job", `{"issue":"HF-6","why":"also ready"}`},                     // over the limit
	}, {
		{"finish_triage", `{"summary":"asked about HF-4, started HF-5","ready":["HF-6"],"blocked":["HF-4: archived rows"]}`},
	}})
	if err := run.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if run.State != Done || run.Report == nil || run.Report.Ready[0] != "HF-6" {
		t.Fatalf("state %s report %+v", run.State, run.Report)
	}
	if len(*started) != 1 || (*started)[0] != "HF-5: cart.go" {
		t.Fatalf("started %v", *started)
	}
	want := []string{"already working on HF-3", "outside this board's project", "Started job HF-5", "limit of 1 jobs"}
	for i, w := range want {
		if !strings.Contains(rt.got[i+1], w) {
			t.Errorf("call %d: %q, want %q", i+1, rt.got[i+1], w)
		}
	}
	if len(yt.comments) != 1 || !strings.Contains(yt.comments[0], "HF-4") || !strings.Contains(yt.comments[0], "[factory] Question for the owner: Should the export") {
		t.Fatalf("comments %v", yt.comments)
	}
	if rt.spec.Role != "product" || rt.spec.Job != "triage-T1" || strings.Contains(strings.Join(rt.spec.Tools, ","), "bash") {
		t.Fatalf("agent spec %+v", rt.spec)
	}
}

// With no jobs allowed, start_job only recommends, and at Observe the
// owner's question stays in the report instead of going on the ticket.
func TestTriageRecommendsAtObserve(t *testing.T) {
	run, rt, yt, started := setup(t, "observe", 0, [][]call{{
		{"ask_owner", `{"issue":"HF-4","question":"Which page size?"}`},
		{"start_job", `{"issue":"HF-5","why":"ready"}`},
		{"finish_triage", `{"summary":"looked"}`},
	}})
	if err := run.Execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*started) != 0 || len(run.Jobs) != 1 || run.Jobs[0].Job != "" {
		t.Fatalf("started %v jobs %+v", *started, run.Jobs)
	}
	if len(yt.comments) != 0 || len(run.Questions) != 1 || !strings.HasPrefix(run.Questions[0].Posted, "refused") {
		t.Fatalf("comments %v questions %+v", yt.comments, run.Questions)
	}
	if !strings.Contains(rt.got[1], "recommendation") {
		t.Fatalf("%v", rt.got)
	}
}

// An agent that never finishes is reminded twice, then the run fails.
func TestTriageNeedsAFinish(t *testing.T) {
	run, _, _, _ := setup(t, "fix", 1, nil)
	run.Time = 5 * time.Second
	if err := run.Execute(context.Background()); err == nil || run.State != Failed {
		t.Fatalf("err %v state %s", err, run.State)
	}
}

func TestDue(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	on := config.TriageAuto{On: true, MaxJobs: 2, OnBoard: true, OnAnswers: true, OnReady: true}
	ended := func(ago time.Duration) *Run {
		return &Run{State: Done, MaxJobs: 2, Started: now.Add(-ago - time.Minute), Ended: now.Add(-ago)}
	}
	changed := Signals{Changed: []string{"HF-9"}}
	cases := []struct {
		name  string
		a     config.TriageAuto
		last  *Run
		today int
		live  int
		sig   Signals
		want  string
	}{
		{"off", config.TriageAuto{}, nil, 0, 0, changed, ""},
		{"first run", on, nil, 0, 0, Signals{}, "the first run"},
		{"inside the gap", on, ended(10 * time.Minute), 0, 0, changed, ""},
		{"daily cap", on, ended(2 * time.Hour), 6, 0, changed, ""},
		{"nothing new", on, ended(2 * time.Hour), 0, 0, Signals{}, ""},
		{"board changed", on, ended(2 * time.Hour), 0, 0, changed, "someone changed HF-9"},
		{"answers first", on, ended(2 * time.Hour), 0, 0, Signals{Changed: []string{"HF-9"}, Answered: []string{"HF-4"}}, "the owner answered on HF-4"},
		{"trigger not ticked", config.TriageAuto{On: true, OnAnswers: true}, ended(2 * time.Hour), 0, 0, changed, ""},
	}
	for _, c := range cases {
		if got := Due(c.a, now, c.last, c.today, c.live, c.sig); !strings.HasPrefix(got, c.want) || (c.want == "") != (got == "") {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	// ready tickets left because the run hit its limit: run again once its jobs are done
	full := ended(2 * time.Hour)
	full.Jobs = []Started{{Issue: "HF-1", Job: "HF-1"}, {Issue: "HF-2", Job: "HF-2"}}
	full.Report = &Report{Summary: "s", Ready: []string{"HF-3"}}
	if got := Due(on, now, full, 0, 1, Signals{}); got != "" {
		t.Errorf("jobs still going: %q", got)
	}
	if got := Due(on, now, full, 0, 0, Signals{}); !strings.Contains(got, "left HF-3 ready") {
		t.Errorf("jobs done: %q", got)
	}
	full.Jobs = full.Jobs[:1] // it didn't hit the limit, so it chose not to start HF-3
	if got := Due(on, now, full, 0, 0, Signals{}); got != "" {
		t.Errorf("below the limit: %q", got)
	}
}

func TestLoadMarksAnInterruptedRunStopped(t *testing.T) {
	dir := t.TempDir()
	r := &Run{ID: "T9", Dir: dir, State: Running, Started: time.Now()}
	r.save()
	got, err := Load(dir)
	if err != nil || got.ID != "T9" || got.State != Stopped || got.Error == "" {
		t.Fatalf("%+v %v", got, err)
	}
}
