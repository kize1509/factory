package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/runtime"
)

// funcAgent runs a function for each turn it is given.
type funcAgent struct {
	fn func(text string) (string, error)
}

func (a *funcAgent) Prompt(_ context.Context, text string) (string, error) { return a.fn(text) }
func (a *funcAgent) Steer(string) error                                    { return nil }
func (a *funcAgent) Stop() error                                           { return nil }

// teamJob is a job with agents added by hand, for tests of the roster and of
// ask_agent that don't run a whole pipeline.
func teamJob(t *testing.T) (*Job, *events.Bus) {
	t.Helper()
	bus := events.NewBus(t.TempDir())
	j := New(Spec{ID: "T9", Issue: "ABC-9", Title: "team", Repo: t.TempDir()}, bus, nil)
	bus.Place(j.ID, j.Dir())
	j.ctx = context.Background()
	return j, bus
}

func (j *Job) addForTest(id string, fn func(string) (string, error)) *funcAgent {
	a := &funcAgent{fn: fn}
	j.mu.Lock()
	j.agents[id] = a
	j.mu.Unlock()
	j.join(id, "a test started it")
	return a
}

func TestMessageNeedsSenderReceiverJobAndTicket(t *testing.T) {
	j, bus := teamJob(t)
	for _, m := range []Message{
		{Kind: KindQuestion, To: "aristotle", Text: "x"},
		{Kind: KindQuestion, From: "archimedes", Text: "x"},
		{Kind: KindQuestion, From: "archimedes", To: "archimedes", Text: "x"},
		{Kind: KindQuestion, From: "archimedes", To: you, Text: "x"},
		{Kind: KindReport, From: "archimedes", To: "aristotle", Text: "x"},
		{Kind: KindBlocker, From: "archimedes", To: "aristotle", Text: "x"},
		{Kind: "gossip", From: "archimedes", To: "aristotle", Text: "x"},
	} {
		if _, err := j.send(m); err == nil {
			t.Errorf("message %+v should have been refused", m)
		}
	}
	if log, _ := bus.Replay(j.ID); len(log) != 0 {
		t.Fatalf("refused messages must not be logged, got %d events", len(log))
	}
	m, err := j.send(Message{Kind: KindQuestion, From: "archimedes", To: "aristotle", Text: "which file?"})
	if err != nil {
		t.Fatal(err)
	}
	log, _ := bus.Replay(j.ID)
	if len(log) != 1 || log[0].Type != "agent.message" {
		t.Fatalf("events %v", log)
	}
	d := log[0].Data
	if d["from"] != "archimedes" || d["to"] != "aristotle" || d["job"] != "T9" || d["ticket"] != "ABC-9" || d["id"] != m.ID {
		t.Fatalf("envelope %v", d)
	}
}

func TestTicketFallsBackToJobID(t *testing.T) {
	j, _ := teamJob(t)
	j.Issue = ""
	m, err := j.send(Message{Kind: KindHandover, From: "aristotle", To: "archimedes", Text: "plan"})
	if err != nil || m.Ticket != "T9" {
		t.Fatalf("ticket %q, err %v", m.Ticket, err)
	}
}

func TestRosterTellsWhoReadsFromWhom(t *testing.T) {
	j, bus := teamJob(t)
	j.addForTest(planner, nil)
	j.addForTest(builder, nil)
	os.MkdirAll(filepath.Join(j.Dir(), "handoff"), 0o755)
	os.WriteFile(filepath.Join(j.Dir(), "handoff", planner+".json"), []byte("{}"), 0o644)

	text := j.RosterFor(builder, true)
	for _, want := range []string{"You are Archimedes, the builder, on job T9 (ticket ABC-9)", "Aristotle (planner)", "You read from:\n- Aristotle: the plan",
		"handoff/aristotle.json", "ask them with ask_agent", "You write to:\n- The engine's checks", "Socrates: your diff and your handoff note. They need: the reasoning behind each change, not only the diff"} {
		if !strings.Contains(text, want) {
			t.Errorf("roster for the builder lacks %q:\n%s", want, text)
		}
	}
	// the version in a prompt must read the same every time it is built
	if a, b := j.rosterBrief(builder), j.rosterBrief(builder); a != b || a == "" {
		t.Fatal("the roster in a prompt must be the same each time")
	}
	if strings.Contains(j.rosterBrief(builder), "said you are") {
		t.Fatal("what an agent says it is doing must stay out of the prompt version")
	}
	j.SetDoing(planner, "reading the code")
	if !strings.Contains(j.RosterFor(builder, true), "Aristotle (planner), waiting: reading the code") {
		t.Fatalf("the live roster should show what the planner is doing:\n%s", j.RosterFor(builder, true))
	}
	var members []any
	for _, e := range mustReplay(bus, j.ID) {
		if e.Type == "roster.updated" {
			members, _ = e.Data["members"].([]any)
		}
	}
	if len(members) != 2 {
		t.Fatalf("the last roster.updated should list both agents, got %v", members)
	}
}

func mustReplay(bus *events.Bus, job string) []events.Event {
	evs, _ := bus.Replay(job)
	return evs
}

func TestAskAgentSendsBothMessagesAndStates(t *testing.T) {
	j, bus := teamJob(t)
	var states []string
	j.addForTest(planner, func(text string) (string, error) {
		if !strings.Contains(text, "Archimedes, the builder, asks you a question about job T9 (ticket ABC-9)") || !strings.Contains(text, "plan.json") {
			t.Errorf("the planner's prompt: %s", text)
		}
		states = append(states, j.Roster()[0].State) // planner is first in the roster
		return "Edit app.txt only.", nil
	})
	b := j.addForTest(builder, nil)
	b.fn = func(string) (string, error) {
		return j.AskAgent(builder, planner, "Which file do I edit?", []string{"plan.json"})
	}
	answer, err := j.ask(context.Background(), b, "go")
	if err != nil || answer != "Edit app.txt only." {
		t.Fatalf("answer %q, err %v", answer, err)
	}
	if len(states) != 1 || states[0] != StateWorking {
		t.Fatalf("the planner should be working while it answers, got %v", states)
	}
	var q, a map[string]any
	for _, e := range mustReplay(bus, j.ID) {
		if e.Type != "agent.message" {
			continue
		}
		switch e.Data["kind"] {
		case KindQuestion:
			q = e.Data
		case KindAnswer:
			a = e.Data
		}
	}
	if q == nil || a == nil || q["from"] != "archimedes" || q["to"] != "aristotle" || a["from"] != "aristotle" || a["to"] != "archimedes" || a["reply_to"] != q["id"] ||
		a["job"] != "T9" || a["ticket"] != "ABC-9" || q["text"] != "Which file do I edit?" {
		t.Fatalf("question %v answer %v", q, a)
	}
}

func TestAskAgentRules(t *testing.T) {
	j, _ := teamJob(t)
	j.addForTest(planner, func(string) (string, error) { return "ok", nil })
	j.addForTest(grader, func(string) (string, error) { return "ok", nil })
	var errs []error
	b := j.addForTest(builder, nil)
	b.fn = func(string) (string, error) {
		for _, to := range []string{builder, grader, you, "nobody", "", planner, planner, planner, planner} {
			_, err := j.AskAgent(builder, to, "q", nil)
			errs = append(errs, err)
		}
		return "", nil
	}
	j.ask(context.Background(), b, "go")
	want := []string{"yourself", "blind", "ask_human", "not in this job", "say who", "", "", "", "3 questions"}
	for i, w := range want {
		switch {
		case w == "" && errs[i] != nil:
			t.Errorf("ask %d should work: %v", i, errs[i])
		case w != "" && (errs[i] == nil || !strings.Contains(errs[i].Error(), w)):
			t.Errorf("ask %d: want an error with %q, got %v", i, w, errs[i])
		}
	}
	if _, err := j.AskAgent(grader, planner, "q", nil); err == nil {
		t.Error("the grader must not ask anyone")
	}
}

// The reviewer asking the builder while the builder asks the reviewer would
// wait forever; the one asked mid-turn gets told to carry on.
func TestAskAgentBusyGuardPreventsDeadlock(t *testing.T) {
	j, _ := teamJob(t)
	var back error
	j.addForTest(reviewer, func(string) (string, error) {
		_, back = j.AskAgent(reviewer, builder, "why?", nil)
		return "I asked", nil
	})
	b := j.addForTest(builder, nil)
	b.fn = func(string) (string, error) {
		return j.AskAgent(builder, reviewer, "anything on your mind?", nil)
	}
	if _, err := j.ask(context.Background(), b, "go"); err != nil {
		t.Fatal(err)
	}
	if back == nil || !strings.Contains(back.Error(), "busy") || !strings.Contains(back.Error(), operations) {
		t.Fatalf("the reviewer's question to a busy builder should fail and say what to do, got %v", back)
	}
}

func TestAskOperationsGoesToTheSourceOfYourInput(t *testing.T) {
	j, _ := teamJob(t)
	j.addForTest(planner, func(text string) (string, error) {
		if !strings.Contains(text, "forwarded to you because operations isn't running") {
			t.Errorf("the planner should be told why it got this: %s", text)
		}
		return "Ask me anything.", nil
	})
	b := j.addForTest(builder, nil)
	var got string
	var err error
	b.fn = func(string) (string, error) {
		got, err = j.AskAgent(builder, operations, "who knows about the schema?", nil)
		return "", nil
	}
	j.ask(context.Background(), b, "go")
	if err != nil || got != "Ask me anything." {
		t.Fatalf("got %q, err %v", got, err)
	}
	// the planner has nobody above it but the person
	j.mu.Lock()
	_, e := j.routeLocked(planner)
	j.mu.Unlock()
	if e == nil || !strings.Contains(e.Error(), "ask_human") {
		t.Fatalf("with nobody to forward to the agent should be pointed at ask_human, got %v", e)
	}
}

func TestAskGoneAgentPointsAtItsSavedNote(t *testing.T) {
	j, _ := teamJob(t)
	os.MkdirAll(filepath.Join(j.Dir(), "handoff"), 0o755)
	note := filepath.Join(j.Dir(), "handoff", planner+".json")
	os.WriteFile(note, []byte("{}"), 0o644)
	b := j.addForTest(builder, nil)
	var err error
	b.fn = func(string) (string, error) {
		_, err = j.AskAgent(builder, planner, "q", nil)
		return "", nil
	}
	j.ask(context.Background(), b, "go")
	if err == nil || !strings.Contains(err.Error(), note) || !strings.Contains(err.Error(), "ask_human") {
		t.Fatalf("want the saved note's path and a next step, got %v", err)
	}
}

func TestAskHumanBlocksAndSendsABlocker(t *testing.T) {
	j, bus := teamJob(t)
	j.addForTest(builder, nil)
	if err := j.AskHuman(builder, Ask{Need: "I need the API key", Why: "no key in .env"}); err != nil {
		t.Fatal(err)
	}
	if got := j.Roster()[0].State; got != StateBlocked {
		t.Fatalf("state %s, want blocked", got)
	}
	var found bool
	for _, e := range mustReplay(bus, j.ID) {
		if e.Type == "agent.message" && e.Data["kind"] == KindBlocker && e.Data["from"] == builder && e.Data["to"] == you {
			found = true
		}
	}
	if !found {
		t.Fatal("ask_human should send a blocker to the person")
	}
}

// scriptRT plays the planner and the builder of a small job: the builder
// doesn't know which file to write, so it asks the planner.
type scriptRT struct {
	job     *Job
	builder string // the prompt the builder got
	answer  string
	err     error
}

func (f *scriptRT) Start(_ context.Context, s runtime.AgentSpec) (runtime.Agent, error) {
	return &funcAgent{fn: func(text string) (string, error) {
		j := f.job
		switch {
		case s.Agent == planner && strings.Contains(text, "asks you a question"):
			return "Write it into notes.txt, not app.txt.", nil
		case s.Agent == planner:
			j.SaveHandoff(planner, &handoff.Note{From: planner, To: builder, Did: "planned", Next: "write the answer", Decisions: []string{"one file"}})
		case s.Agent == builder:
			f.builder = text
			f.answer, f.err = j.AskAgent(builder, planner, "The plan doesn't say which file. Which one?", nil)
			os.WriteFile(filepath.Join(s.Cwd, "notes.txt"), []byte("done\n"), 0o644)
			j.SaveHandoff(builder, &handoff.Note{From: builder, To: "reviewers", Did: "wrote notes.txt", Next: "review", Files: []string{"notes.txt"}})
		}
		return "ok", nil
	}}, nil
}

func TestBuilderAsksThePlannerInsteadOfGuessing(t *testing.T) {
	bus := events.NewBus(t.TempDir())
	rt := &scriptRT{}
	j := New(Spec{ID: "A1", Issue: "ABC-1", Title: "write notes", Repo: gitRepo(t), Prompt: "write done into the right file"}, bus, rt)
	rt.job = j
	bus.Place(j.ID, j.Dir())
	j.Run(context.Background())
	if j.State != "done" {
		t.Fatalf("state %s, error %s", j.State, j.Error)
	}
	if rt.err != nil || !strings.Contains(rt.answer, "notes.txt") {
		t.Fatalf("the builder's answer %q, err %v", rt.answer, rt.err)
	}
	if !strings.HasPrefix(rt.builder, "## Your job roster") || !strings.Contains(rt.builder, "Aristotle: the plan") || !strings.Contains(rt.builder, "Socrates") {
		t.Fatalf("the builder's task should start with its roster:\n%s", rt.builder)
	}
	kinds := map[string]int{}
	for _, e := range mustReplay(bus, j.ID) {
		if e.Type != "agent.message" {
			continue
		}
		for _, k := range []string{"from", "to", "job", "ticket"} {
			if s, _ := e.Data[k].(string); s == "" {
				t.Errorf("message without %s: %v", k, e.Data)
			}
		}
		if e.Data["ticket"] != "ABC-1" || e.Data["job"] != "A1" {
			t.Errorf("wrong job or ticket: %v", e.Data)
		}
		kinds[e.Data["kind"].(string)+" "+e.Data["from"].(string)+">"+e.Data["to"].(string)]++
	}
	for _, want := range []string{"question archimedes>aristotle", "answer aristotle>archimedes", "handover aristotle>archimedes", "handover archimedes>socrates", "report archimedes>operations"} {
		if kinds[want] == 0 {
			t.Errorf("missing message %q in %v", want, kinds)
		}
	}
}

// The builder's roster lists what the planner already read, sorted and capped.
func TestBuilderRosterListsWhatThePlannerRead(t *testing.T) {
	j, bus := teamJob(t)
	j.addForTest(planner, nil)
	j.addForTest(builder, nil)
	tool := func(agent, name, path string) {
		bus.Emit(j.ID, agent, "agent.tool", map[string]any{"tool": name, "args": map[string]any{"path": path}})
	}
	tool(planner, "read", "z.go")
	tool(planner, "grep", "internal/./a.go")
	tool(planner, "read", "z.go")     // repeats once
	tool(planner, "edit", "never.go") // not a read
	tool(builder, "read", "mine.go")  // not the planner's
	got := j.rosterBrief(builder)
	if !strings.Contains(got, "Aristotle already read: internal/a.go, z.go. Don't explore those again") || strings.Contains(got, "mine.go") || strings.Contains(got, "never.go") {
		t.Fatalf("roster:\n%s", got)
	}
	if j.rosterBrief(builder) != got {
		t.Fatal("the brief must be the same each time")
	}
	for i := 0; i < maxReadsListed+5; i++ {
		tool(planner, "read", fmt.Sprintf("f%03d.go", i))
	}
	if got := j.rosterBrief(builder); !strings.Contains(got, ", and 7 more.") {
		t.Fatalf("the list should be capped with a count of the rest:\n%s", got)
	}
	if strings.Contains(j.rosterBrief(planner), "already read") {
		t.Fatal("only builders get the list")
	}
}
