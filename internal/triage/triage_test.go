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
		{"start_job", `{"issue":"HF-3","why":"ready"}`},                         // a job is already on it
		{"start_job", `{"issue":"OTHER-1","why":"ready"}`},                      // another project
		{"start_job", `{"issue":"hf-5","why":"top priority","notes":"cart.go"}`}, // the one it may start
		{"start_job", `{"issue":"HF-6","why":"also ready"}`},                    // over the limit
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
