package job

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/runtime"
)

// promptRT plays a builder and a reviewer that approves at once, and keeps
// the first prompt the reviewer got.
type promptRT struct {
	job      *Job
	reviewed string
}

func (f *promptRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &promptAgent{rt: f, role: s.Role, cwd: s.Cwd}, nil
}

type promptAgent struct {
	rt   *promptRT
	role string
	cwd  string
}

func (a *promptAgent) Prompt(_ context.Context, text string) (string, error) {
	j := a.rt.job
	if a.role == "reviewer" {
		if a.rt.reviewed == "" {
			a.rt.reviewed = text
		}
		return "", j.SetVerdict(reviewer, Verdict{Approved: true, Summary: "fine"})
	}
	os.WriteFile(filepath.Join(a.cwd, "app.txt"), []byte("done\n"), 0o644)
	j.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewer", Did: "done", Next: "review", Files: []string{"app.txt"}})
	return "ok", nil
}
func (a *promptAgent) Steer(string) error { return nil }
func (a *promptAgent) Stop() error        { return nil }

type fakeOps struct {
	calls  []string
	closed bool
	fail   bool
}

func (f *fakeOps) Handover(job, from, to string, n *handoff.Note) (string, error) {
	f.calls = append(f.calls, from+">"+to)
	if f.fail {
		return "", os.ErrNotExist
	}
	return "## Handover from Hermes\n\nthe builder chose app.txt", nil
}
func (f *fakeOps) Close(string) { f.closed = true }

func runReview(t *testing.T, o Ops) (*promptRT, *Job, *events.Bus) {
	t.Helper()
	bus := events.NewBus(t.TempDir())
	rt := &promptRT{}
	j := New(Spec{ID: "O1", Title: "ops", Repo: gitRepo(t), Prompt: "x", Checks: []string{"test -f app.txt"}}, bus, rt)
	j.SetReviewer(rt)
	if o != nil {
		j.SetOps(o)
	}
	rt.job = j
	j.Run(context.Background())
	if j.State != "done" {
		t.Fatalf("state %s, error %s", j.State, j.Error)
	}
	return rt, j, bus
}

func TestHandoverReachesReviewerOnlyWithOps(t *testing.T) {
	rt, _, _ := runReview(t, nil)
	if strings.Contains(rt.reviewed, "Handover from Hermes") {
		t.Fatal("without an operations agent the review prompt must stay as it was")
	}
	f := &fakeOps{}
	rt, _, _ = runReview(t, f)
	if !strings.Contains(rt.reviewed, "Handover from Hermes") || len(f.calls) == 0 || f.calls[0] != "archimedes>socrates" || !f.closed {
		t.Fatalf("prompt %q calls %v closed %v", rt.reviewed, f.calls, f.closed)
	}
}

func TestHandoverFailureFallsBackToPlainNote(t *testing.T) {
	f := &fakeOps{fail: true}
	rt, _, bus := runReview(t, f)
	if strings.Contains(rt.reviewed, "Handover from Hermes") || !strings.Contains(rt.reviewed, "The builder's handoff note") {
		t.Fatalf("fallback prompt wrong: %q", rt.reviewed)
	}
	log, _ := bus.Replay("O1")
	found := false
	for _, e := range log {
		found = found || e.Type == "ops.unavailable"
	}
	if !found {
		t.Fatal("a failed handover should be an event")
	}
}
