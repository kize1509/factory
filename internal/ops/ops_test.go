package ops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/youtrack"
)

// fakeYT is a YouTrack that records every write.
type fakeYT struct {
	mu       sync.Mutex
	commands []string // "HF-1: State In Progress"
	comments []string
	creates  int
	queries  []string // search queries received
	leak     bool     // answer searches with an issue of another project too
	srv      *httptest.Server
}

func newYT(t *testing.T) *fakeYT {
	f := &fakeYT{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/api/admin/projects":
			w.Write([]byte(`[{"id":"0-1","shortName":"HF"}]`))
		case r.URL.Path == "/api/issues" && r.Method == "POST":
			f.creates++
			w.Write([]byte(`{"idReadable":"HF-42"}`))
		case r.URL.Path == "/api/issues" && r.Method == "GET":
			f.queries = append(f.queries, r.URL.Query().Get("query"))
			extra := ""
			if f.leak {
				extra = `,{"idReadable":"OTHER-1","summary":"secret plans","customFields":[]}`
			}
			w.Write([]byte(`[{"idReadable":"HF-5","summary":"login times out","customFields":[{"name":"State","value":{"name":"Open"}}]}` + extra + `]`))
		case r.URL.Path == "/api/commands":
			var c struct {
				Query  string
				Issues []struct{ IdReadable string }
			}
			json.Unmarshal(body, &c)
			for _, is := range c.Issues {
				f.commands = append(f.commands, is.IdReadable+": "+c.Query)
			}
			w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/comments") && r.Method == "POST":
			var c struct{ Text string }
			json.Unmarshal(body, &c)
			f.comments = append(f.comments, r.URL.Path+": "+c.Text)
			w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeYT) cmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.commands...)
}

const tracker = `tracker:
  kind: youtrack
  project: HF
  level: %s
  states: {working: "In Progress", blocked: "Blocked", review: "In Review", done: "Done"}
  max_created_per_job: 2
  max_created_per_pass: 3
`

func setup(t *testing.T, level string) (*Service, *fakeYT, string, *events.Bus) {
	t.Helper()
	repo := t.TempDir()
	os.WriteFile(filepath.Join(repo, "factory.yml"), []byte(strings.Replace(tracker, "%s", level, 1)), 0o644)
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	yt := newYT(t)
	bus := events.NewBus(t.TempDir())
	bus.Persist = db.AddEvent
	s := &Service{Store: db, Bus: bus, Client: func() youtrack.Client { return youtrack.Client{URL: yt.srv.URL, Token: "perm:x"} }}
	return s, yt, repo, bus
}

func eventTypes(bus *events.Bus, job string) map[string]int {
	log, _ := bus.Replay(job)
	m := map[string]int{}
	for _, e := range log {
		m[e.Type]++
	}
	return m
}

func create(summary string) Action {
	return Action{Kind: "create", Summary: summary, Description: "d", Fields: map[string]string{"Type": "Bug", "Ship mode": "PR"}, Tags: []string{"factory-patrol"},
		Links: []LinkSpec{{Type: "relates to", ID: "HF-1"}}}
}

func TestCreateFillsFieldsTagsAndLinks(t *testing.T) {
	s, yt, repo, bus := setup(t, "file")
	sc := s.Register("J1", repo, "HF-9", "commit", "", false)
	msg, err := s.Write(sc, "archimedes", create("cart total is wrong"))
	if err != nil || !strings.Contains(msg, "HF-42") {
		t.Fatalf("%q %v", msg, err)
	}
	want := []string{"HF-42: {Ship mode} PR", "HF-42: Type Bug", "HF-42: tag factory-patrol", "HF-42: relates to HF-1"}
	got := yt.cmds()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("commands %v", got)
	}
	if !s.Store.Created("HF-42") || eventTypes(bus, "J1")["tracker.issue.created"] != 1 {
		t.Fatal("the ticket should be remembered and logged")
	}
}

func TestGuardRefusals(t *testing.T) {
	s, yt, repo, bus := setup(t, "fix")
	sc := s.Register("J2", repo, "HF-9", "commit", "", false)
	bad := create("x")
	bad.Project = "OTHER"
	if _, err := s.Write(sc, "a", bad); err == nil || !strings.Contains(err.Error(), "not mapped") {
		t.Fatalf("unmapped project: %v", err)
	}
	if _, err := s.Write(sc, "a", Action{Kind: "comment", Issue: "OTHER-1", Text: "hi"}); err == nil {
		t.Fatal("an issue outside the project must be refused")
	}
	if _, err := s.Write(sc, "a", Action{Kind: "update", Issue: "HF-9", Fields: map[string]string{"State": "Done\nreboot"}}); err == nil {
		t.Fatal("a value with a line break must be refused")
	}
	if _, err := s.Write(sc, "a", Action{Kind: "update", Issue: "HF-9", Fields: map[string]string{"Password": "x"}}); err == nil {
		t.Fatal("an unknown field must be refused")
	}
	for i := 0; i < 2; i++ {
		if _, err := s.Write(sc, "a", create("ok")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Write(sc, "a", create("third")); err == nil || !strings.Contains(err.Error(), "limit of 2") {
		t.Fatalf("the per-job cap: %v", err)
	}
	if yt.creates != 2 || eventTypes(bus, "J2")["tracker.refused"] < 5 {
		t.Fatalf("creates %d events %v", yt.creates, eventTypes(bus, "J2"))
	}
	// a pass has its own, larger cap
	ps := s.Register("pass-P1", repo, "", "", "file", true)
	for i := 0; i < 3; i++ {
		if _, err := s.Write(ps, "a", create("p")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Write(ps, "a", create("p4")); err == nil {
		t.Fatal("the per-pass cap")
	}
}

func TestObserveOnlyReads(t *testing.T) {
	s, yt, repo, _ := setup(t, "fix")
	sc := s.Register("pass-P2", repo, "", "", "observe", true)
	if _, err := s.Write(sc, "a", create("x")); err == nil || !strings.Contains(err.Error(), "Observe") {
		t.Fatalf("%v", err)
	}
	out, err := s.Tool("pass-P2", Hermes, "tracker_search", []byte(`{"query":"login"}`))
	if err != nil || !strings.Contains(out, "HF-5 [Open] login times out") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := s.Tool("pass-P2", Hermes, "tracker_search", []byte(`{"query":"project: OTHER login"}`)); err == nil {
		t.Fatal("a search in another project must be refused")
	}
	if yt.creates != 0 {
		t.Fatal("nothing may be written")
	}
}

func TestProposeDraftsThenApproveRunsOnce(t *testing.T) {
	s, yt, repo, bus := setup(t, "propose")
	sc := s.Register("J3", repo, "HF-9", "commit", "", false)
	msg, err := s.Write(sc, "archimedes", create("flaky test"))
	if err != nil || !strings.Contains(msg, "draft") {
		t.Fatalf("%q %v", msg, err)
	}
	if yt.creates != 0 {
		t.Fatal("nothing may reach YouTrack before approval")
	}
	drafts, _ := s.Store.Drafts("pending")
	if len(drafts) != 1 || drafts[0].Summary != "Create bug in HF: flaky test" {
		t.Fatalf("%+v", drafts)
	}
	issue, err := s.Approve(drafts[0].ID)
	if err != nil || issue != "HF-42" {
		t.Fatalf("%q %v", issue, err)
	}
	if _, err := s.Approve(drafts[0].ID); err == nil {
		t.Fatal("a second approval must not run it again")
	}
	if yt.creates != 1 {
		t.Fatalf("created %d times", yt.creates)
	}
	d, _ := s.Store.Draft(drafts[0].ID)
	if d.State != "approved" || d.Result != "HF-42" {
		t.Fatalf("%+v", d)
	}
	ev := eventTypes(bus, "J3")
	if ev["tracker.draft"] != 1 || ev["tracker.draft.approved"] != 1 || ev["tracker.issue.created"] != 1 {
		t.Fatalf("%v", ev)
	}
}

func TestRejectAndFailedApprovalStayPending(t *testing.T) {
	s, yt, repo, _ := setup(t, "propose")
	sc := s.Register("J4", repo, "", "", "", false)
	s.Write(sc, "a", create("one"))
	s.Write(sc, "a", create("two"))
	drafts, _ := s.Store.Drafts("pending")
	if err := s.Reject(drafts[0].ID); err != nil {
		t.Fatal(err)
	}
	yt.srv.Close() // YouTrack goes away
	if _, err := s.Approve(drafts[1].ID); err == nil {
		t.Fatal("want YouTrack's error")
	}
	d, _ := s.Store.Draft(drafts[1].ID)
	if d.State != "pending" || !strings.HasPrefix(d.Result, "error:") {
		t.Fatalf("a failed approval stays pending with the error: %+v", d)
	}
}

func TestForeignTicketChangesAreDrafts(t *testing.T) {
	s, yt, repo, _ := setup(t, "fix")
	sc := s.Register("J5", repo, "HF-9", "commit", "", false)
	if _, err := s.Write(sc, "a", Action{Kind: "move", Issue: "HF-7", Fields: map[string]string{"State": "Done"}}); err != nil {
		t.Fatal(err)
	}
	if len(yt.cmds()) != 0 {
		t.Fatal("closing a ticket the factory didn't create must wait for approval")
	}
	if _, err := s.Write(sc, "a", Action{Kind: "comment", Issue: "HF-7", Text: "dup of HF-3"}); err != nil {
		t.Fatal(err)
	}
	if len(yt.comments) != 1 || !strings.Contains(yt.comments[0], Marker) {
		t.Fatalf("comments are allowed on any ticket, marked as the factory's: %v", yt.comments)
	}
}

func TestBoardSyncFollowsJobState(t *testing.T) {
	s, yt, repo, bus := setup(t, "fix")
	sc := s.Register("J6", repo, "HF-9", "commit", "", false)
	for _, st := range []string{"planning", "building", "checking", "reviewing", "building", "reviewing", "done"} {
		s.Observe(events.Event{Job: "J6", Type: "job.state", Data: map[string]any{"state": st}})
	}
	s.Observe(events.Event{Job: "J6", Type: "job.committed", Data: map[string]any{"sha": "abc1234", "branch": "factory/HF-9"}})
	s.Wait("J6")
	var moves []string
	for _, c := range yt.cmds() {
		moves = append(moves, strings.TrimPrefix(c, "HF-9: State "))
	}
	want := "{In Progress}|{In Review}|{In Progress}|{In Review}|Done"
	if strings.Join(moves, "|") != want {
		t.Fatalf("moves %v, want %s", moves, want)
	}
	if len(yt.comments) != 1 || !strings.Contains(yt.comments[0], "Job J6 finished") {
		t.Fatalf("%v", yt.comments)
	}
	if eventTypes(bus, "J6")["tracker.issue.moved"] != 5 {
		t.Fatalf("%v", eventTypes(bus, "J6"))
	}
	_ = sc
}

func TestBoardSyncNeverFailsTheJob(t *testing.T) {
	s, yt, repo, bus := setup(t, "fix")
	s.Register("J7", repo, "HF-9", "commit", "", false)
	yt.srv.Close()
	s.Observe(events.Event{Job: "J7", Type: "job.state", Data: map[string]any{"state": "building"}})
	s.Wait("J7")
	if eventTypes(bus, "J7")["tracker.skipped"] != 1 {
		t.Fatalf("%v", eventTypes(bus, "J7"))
	}
	// no tracker block: nothing happens
	bare := t.TempDir()
	s.Register("J8", bare, "HF-9", "commit", "", false)
	s.Observe(events.Event{Job: "J8", Type: "job.state", Data: map[string]any{"state": "building"}})
	s.Wait("J8")
	if len(eventTypes(bus, "J8")) != 0 {
		t.Fatal("without a tracker block board sync is off")
	}
}

func TestNotesFromHandoffsAndUser(t *testing.T) {
	s, _, repo, _ := setup(t, "fix")
	s.Register("J9", repo, "HF-9", "commit", "", false)
	s.Observe(events.Event{Seq: 7, Job: "J9", Agent: "aristotle", Type: "handoff.written", Data: map[string]any{"note": &handoff.Note{Did: "read the code", Decisions: []string{"use a drafts table"}}}})
	s.Observe(events.Event{Seq: 8, Job: "J9", Type: "human.message", Data: map[string]any{"text": "keep it small"}})
	notes, _ := s.Store.Notes(repo, "", "", words("what did aristotle decide about drafts"), 10)
	if len(notes) != 1 || notes[0].Kind != "decision" || notes[0].Seq != 7 || notes[0].Issue != "HF-9" {
		t.Fatalf("%+v", notes)
	}
}

// scriptRT plays Hermes: it answers from the notes it is handed.
type scriptRT struct {
	s       *Service
	scope   string
	prompts []string
	starts  int
}

func (f *scriptRT) Start(_ context.Context, spec runtime.AgentSpec) (runtime.Agent, error) {
	f.starts++
	return &scriptAgent{f}, nil
}

type scriptAgent struct{ f *scriptRT }

func (a *scriptAgent) Prompt(_ context.Context, text string) (string, error) {
	a.f.prompts = append(a.f.prompts, text)
	switch {
	case strings.Contains(text, "asks you"):
		a.f.s.Tool(a.f.scope, Hermes, "answer", []byte(`{"text":"The planner chose a drafts table.","sources":["HF-9","job J10","aristotle handoff #7"]}`))
	case strings.Contains(text, "Work moves from"):
		a.f.s.Tool(a.f.scope, Hermes, "handover", []byte(`{"context":"Plan is a drafts table.","decisions":["drafts table"],"open_points":["UI copy"],"links":["HF-9"]}`))
	}
	return "done", nil
}
func (a *scriptAgent) Steer(string) error { return nil }
func (a *scriptAgent) Stop() error        { return nil }

func TestAskOpsRoundTripWithSources(t *testing.T) {
	s, _, repo, bus := setup(t, "fix")
	rt := &scriptRT{s: s, scope: "J10"}
	s.Runtimes = map[string]runtime.Runtime{"pi": rt}
	s.Settings = func() config.Engine { return config.Engine{} }
	s.Register("J10", repo, "HF-9", "commit", "", false)
	s.Observe(events.Event{Seq: 7, Job: "J10", Agent: "aristotle", Type: "handoff.written", Data: map[string]any{"note": &handoff.Note{Did: "x", Decisions: []string{"use a drafts table"}}}})

	out, err := s.Tool("J10", "archimedes", "ask_ops", []byte(`{"question":"What did the planner decide about drafts?"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "drafts table") || !strings.Contains(out, "Sources: HF-9, job J10, aristotle handoff #7") {
		t.Fatalf("%q", out)
	}
	if !strings.Contains(rt.prompts[0], "use a drafts table") {
		t.Fatal("the notes that match go into Hermes's prompt")
	}
	// the second request reuses the session; a handover works too
	h, err := s.Handover("J10", "aristotle", "archimedes", &handoff.Note{Did: "planned"})
	if err != nil || !strings.Contains(h, "Handover from Hermes (aristotle → archimedes)") || !strings.Contains(h, "UI copy") {
		t.Fatalf("%q %v", h, err)
	}
	if rt.starts != 1 {
		t.Fatalf("one session per job, started %d", rt.starts)
	}
	ev := eventTypes(bus, "J10")
	if ev["ops.request"] != 1 || ev["ops.answer"] != 1 || ev["ops.handover"] != 1 {
		t.Fatalf("%v", ev)
	}
	// only Hermes has the tracker tools
	if _, err := s.Tool("J10", "archimedes", "tracker_create", []byte(`{}`)); err == nil {
		t.Fatal("other agents must go through ask_ops")
	}
	s.Close("J10")
}

func TestReportKeepsNotes(t *testing.T) {
	s, _, repo, bus := setup(t, "fix")
	s.Register("J11", repo, "HF-9", "commit", "", false)
	if _, err := s.Tool("J11", "archimedes", "report", []byte(`{"kind":"blocker","text":"no API key for staging","issues":["HF-12"]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Tool("J11", "archimedes", "report", []byte(`{"kind":"gossip","text":"x"}`)); err == nil {
		t.Fatal("unknown kind")
	}
	notes, _ := s.Store.Notes(repo, "", "HF-12", nil, 5)
	if len(notes) != 1 || notes[0].Kind != "blocker" || eventTypes(bus, "J11")["ops.report"] != 1 {
		t.Fatalf("%+v", notes)
	}
}

func TestPollPushesNewCommentsOnly(t *testing.T) {
	s, _, repo, bus := setup(t, "fix")
	var got []string
	s.Steer = func(scope, text string) bool { got = append(got, text); return true }
	comments := `[{"id":"1","text":"[factory] moved","created":9999999999999,"author":{"login":"bot"}},{"id":"2","text":"please keep it small","created":9999999999998,"author":{"login":"zoran","fullName":"Zoran"}}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(comments)) }))
	defer srv.Close()
	s.Client = func() youtrack.Client { return youtrack.Client{URL: srv.URL, Token: "perm:x"} }
	sc := s.Register("J12", repo, "HF-9", "commit", "", false)
	s.pollOnce(sc)
	s.pollOnce(sc)
	if len(got) != 1 || !strings.Contains(got[0], "New comment on HF-9 from Zoran: please keep it small") {
		t.Fatalf("%v", got)
	}
	if eventTypes(bus, "J12")["ops.push"] != 1 {
		t.Fatal("a push is an event")
	}
}

func TestSearchStaysInTheMappedProject(t *testing.T) {
	s, yt, repo, bus := setup(t, "fix")
	yt.leak = true
	s.Register("J13", repo, "HF-9", "commit", "", false)
	for _, q := range []string{"login or in: OTHER", "login or #OTHER-1"} {
		out, err := s.Tool("J13", Hermes, "tracker_search", []byte(`{"query":"`+q+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out, "OTHER-1") || !strings.Contains(out, "HF-5") {
			t.Fatalf("results from another project must be dropped: %q", out)
		}
	}
	for i, q := range []string{"login or in: OTHER", "login or #OTHER-1"} {
		if want := "project: HF and (" + q + ")"; yt.queries[i] != want {
			t.Fatalf("query sent %q, want %q", yt.queries[i], want)
		}
	}
	log, _ := bus.Replay("J13")
	dropped := 0.0
	for _, e := range log {
		if e.Type == "tracker.search" {
			d, _ := e.Data["dropped"].(float64)
			dropped += d
		}
	}
	if dropped != 2 {
		t.Fatalf("each search drops the stray issue and says so in the event, got %v", dropped)
	}
}

// fileRT plays Hermes filing a bug while it serves a request.
type fileRT struct{ s *Service }

func (f *fileRT) Start(_ context.Context, _ runtime.AgentSpec) (runtime.Agent, error) {
	return &fileAgent{f}, nil
}

type fileAgent struct{ f *fileRT }

func (a *fileAgent) Prompt(_ context.Context, text string) (string, error) {
	a.f.s.Tool("J14", Hermes, "tracker_create", []byte(`{"summary":"cart total is wrong","description":"d","type":"Bug"}`))
	a.f.s.Tool("J14", Hermes, "answer", []byte(`{"text":"Filed.","sources":["HF-42"]}`))
	return "", nil
}
func (a *fileAgent) Steer(string) error { return nil }
func (a *fileAgent) Stop() error        { return nil }

func TestWritesRecordWhoAsked(t *testing.T) {
	for _, level := range []string{"file", "propose"} {
		s, _, repo, bus := setup(t, level)
		s.Runtimes = map[string]runtime.Runtime{"pi": &fileRT{s}}
		s.Settings = func() config.Engine { return config.Engine{} }
		s.Register("J14", repo, "HF-9", "commit", "", false)
		if _, err := s.Tool("J14", "archimedes", "ask_ops", []byte(`{"question":"please file a bug for the cart total"}`)); err != nil {
			t.Fatal(err)
		}
		typ := "tracker.issue.created"
		if level == "propose" {
			typ = "tracker.draft"
		}
		log, _ := bus.Replay("J14")
		found := false
		for _, e := range log {
			if e.Type == typ {
				found = true
				if e.Data["from"] != "archimedes" || e.Agent != Hermes {
					t.Fatalf("%s: %+v", level, e)
				}
			}
		}
		if !found {
			t.Fatalf("%s: no %s event", level, typ)
		}
		if level == "propose" {
			if d, _ := s.Store.Drafts("pending"); len(d) != 1 || d[0].Agent != "archimedes" {
				t.Fatalf("the draft should name who asked: %+v", d)
			}
		}
		s.Close("J14")
	}
	// work the engine starts itself stays Hermes's
	s, _, repo, _ := setup(t, "fix")
	sc := s.Register("J15", repo, "HF-9", "commit", "", false)
	if requester(sc) != Hermes {
		t.Fatal("board sync is Hermes's own")
	}
}

func TestSearchWithoutAQueryListsTheProject(t *testing.T) {
	s, yt, repo, _ := setup(t, "fix")
	s.Register("J16", repo, "HF-9", "commit", "", false)
	for _, q := range []string{"", "  "} {
		if _, err := s.Tool("J16", Hermes, "tracker_search", []byte(`{"query":"`+q+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	for i := range []string{"", "  "} {
		if yt.queries[i] != "project: HF" {
			t.Fatalf("an empty query should list the project, sent %q", yt.queries[i])
		}
	}
}
