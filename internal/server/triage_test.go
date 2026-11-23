package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/triage"
)

// waitRT is a product agent that waits until the test lets it go.
type waitRT struct{ release chan struct{} }

func (f *waitRT) Start(context.Context, runtime.AgentSpec) (runtime.Agent, error) {
	return waitAgent{f}, nil
}

type waitAgent struct{ rt *waitRT }

func (a waitAgent) Prompt(ctx context.Context, _ string) (string, error) {
	select {
	case <-a.rt.release:
	case <-ctx.Done():
	}
	return "ok", nil
}
func (waitAgent) Steer(string) error { return nil }
func (waitAgent) Stop() error        { return nil }

func TestTriageRunOverHTTPAndMCP(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home) // default roles: the product role runs on pi
	yt := httptest.NewServer(http.NotFoundHandler())
	defer yt.Close()
	os.MkdirAll(filepath.Join(home, ".factory"), 0o755)
	os.WriteFile(filepath.Join(home, ".factory", "settings.json"), []byte(`{"youtrack":{"url":"`+yt.URL+`","token":"perm:x"}}`), 0o644)
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	repo, _ = filepath.EvalSymlinks(repo) // the engine keeps repos by their real path
	rt := &waitRT{release: make(chan struct{})}
	s := &Server{Store: db, Bus: events.NewBus(t.TempDir()), Runtimes: map[string]runtime.Runtime{"pi": rt}}
	h := s.Handler()
	post := func(path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, strings.NewReader(body)))
		return w
	}

	// no tracker block and no project: it has to ask which board
	if w := post("/api/triage", `{"repo":"`+repo+`"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "which YouTrack project") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	w := post("/api/triage", `{"repo":"`+repo+`","project":"hf","level":"propose","max_jobs":0}`)
	var started struct {
		ID, Project, Level string
		MaxJobs            int `json:"max_jobs"`
	}
	if json.Unmarshal(w.Body.Bytes(), &started); w.Code != 200 || started.Project != "HF" || started.Level != "propose" || started.MaxJobs != 0 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post("/api/triage", `{"repo":"`+repo+`","project":"HF"}`); w.Code != 409 {
		t.Fatalf("a second run on the same board: %d %s", w.Code, w.Body)
	}

	mcp := func(agent, tool, args string) string {
		w := post("/mcp/triage-"+started.ID+"/"+agent, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+args+`}}`)
		return w.Body.String()
	}
	if out := mcp("pericles", "handoff", `{}`); !strings.Contains(out, "no tool") {
		t.Fatalf("job tools aren't the product agent's: %s", out)
	}
	if out := mcp("pericles", "finish_triage", `{"summary":"nothing open"}`); !strings.Contains(out, "Run finished") {
		t.Fatal(out)
	}
	close(rt.release)
	for i := 0; i < 200 && s.triageOf("triage-"+started.ID).StateNow() == triage.Running; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	g := httptest.NewRecorder()
	h.ServeHTTP(g, httptest.NewRequest("GET", "/api/triage/"+started.ID, nil))
	var got triage.Run
	if json.Unmarshal(g.Body.Bytes(), &got); got.State != triage.Done || got.Report == nil || got.Report.Summary != "nothing open" || got.Why != "you started it" {
		t.Fatalf("%s", g.Body)
	}

	// the Board panel lists the repo with the run's project
	g = httptest.NewRecorder()
	h.ServeHTTP(g, httptest.NewRequest("GET", "/api/triage/boards", nil))
	var boards struct {
		Boards   []Board
		YouTrack struct{ Configured bool }
	}
	if json.Unmarshal(g.Body.Bytes(), &boards); len(boards.Boards) != 1 || boards.Boards[0].Repo != repo || boards.Boards[0].Project != "HF" || !boards.YouTrack.Configured {
		t.Fatalf("%s", g.Body)
	}

	// the schedule: refused without a trigger, saved with one, and a restart keeps the runs
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("PUT", "/api/triage/auto", strings.NewReader(body)))
		return w
	}
	if w := put(`{"repo":"` + repo + `","on":true,"project":"HF"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "at least one") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := put(`{"repo":"` + repo + `","on":true,"project":"HF","on_board":true,"max_jobs":1}`); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if a := config.LoadEngine().Triage[repo]; !a.On || !a.OnBoard || a.Project != "HF" {
		t.Fatalf("saved %+v", a)
	}
	// the last run ended a moment ago, so the gap holds the schedule back
	s.autoTick(time.Now())
	if note := s.autoNote[repo]; !strings.Contains(note, "waiting") {
		t.Fatalf("note %q", note)
	}
	s2 := &Server{Store: db, Bus: events.NewBus(t.TempDir()), Runtimes: s.Runtimes}
	s2.Handler()
	if runs := s2.runsFor(repo); len(runs) != 1 || runs[0].ID != started.ID || runs[0].StateNow() != triage.Done {
		t.Fatalf("after a restart: %v", runs)
	}
}
