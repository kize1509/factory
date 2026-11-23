package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/internal/events"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/triage"
)

// waitRT is a product agent that waits until the test lets it go.
type waitRT struct{ release chan struct{} }

func (f *waitRT) Start(context.Context, runtime.AgentSpec) (runtime.Agent, error) { return waitAgent{f}, nil }

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
	t.Setenv("HOME", t.TempDir()) // default settings: the product role runs on pi
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
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
	if json.Unmarshal(g.Body.Bytes(), &got); got.State != triage.Done || got.Report == nil || got.Report.Summary != "nothing open" {
		t.Fatalf("%s", g.Body)
	}
}
