// Package ops is the operations agent's side of the engine (Hermes, role
// "ops"). The agent decides; this package executes (D2). It gives the agent
// a typed set of tracker tools and runs each call through the YouTrack
// client, keeps the token, logs every call as an event and enforces the
// limits: the mapped project, the cap on issues created, the control level.
// It also keeps project knowledge in the store, builds handovers between
// agents, keeps the board in step with the jobs and pushes new ticket
// comments to the agent that works on the ticket.
package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/youtrack"
)

// Hermes is the operations agent's id on the map and in events.
const Hermes = "hermes"

// Marker starts every comment the engine posts, so the comment poller can
// tell the factory's own comments from a person's.
const Marker = "[factory] "

type Service struct {
	Store    *store.Store
	Bus      *events.Bus
	Runtimes map[string]runtime.Runtime
	Settings func() config.Engine
	// Client builds the YouTrack client; the default reads Admin → Connections.
	Client func() youtrack.Client
	// Steer gives a running job's active agent a message; false means nobody got it.
	Steer func(scope, text string) bool
	// PollEvery is how often a job's ticket is checked for new comments.
	PollEvery time.Duration

	mu     sync.Mutex
	scopes map[string]*Scope
}

// Scope is one job or pass the operations agent works for.
type Scope struct {
	ID      string // the job id, or "pass-<id>"
	Repo    string
	Issue   string // the job's own ticket, if any
	Level   string // observe, propose, file or fix
	Ship    string // the job's ship mode, for the board's done state
	Tracker config.Tracker

	svc      *Service
	cap      int
	mu       sync.Mutex
	created  int // issues created or drafted so far
	out      output
	turnMu   sync.Mutex
	serving  string // the agent Hermes is answering in this turn; guarded by mu
	agent    runtime.Agent
	queue    chan func()
	closed   bool
	workerUp bool
	stopPoll context.CancelFunc
	lastMove string
	lastSeen int64
	commit   string
	branch   string
	pr       string
}

type output struct {
	answer   string
	sources  []string
	handover *Handover
}

// Handover is what Hermes puts together when work moves between agents.
type Handover struct {
	Context    string   `json:"context"`
	Decisions  []string `json:"decisions"`
	OpenPoints []string `json:"open_points"`
	Links      []string `json:"links"`
}

// Markdown is the handover as the next agent reads it.
func (h Handover) Markdown(from, to string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Handover from Hermes (%s → %s)\n\n%s\n", from, to, strings.TrimSpace(h.Context))
	section := func(title string, xs []string) {
		if len(xs) > 0 {
			fmt.Fprintf(&b, "\n**%s**\n- %s\n", title, strings.Join(xs, "\n- "))
		}
	}
	section("Decisions", h.Decisions)
	section("Open points", h.OpenPoints)
	section("Links", h.Links)
	return strings.TrimSpace(b.String())
}

// Enabled says whether the repo maps to a tracker project.
func (sc *Scope) Enabled() bool {
	return strings.EqualFold(sc.Tracker.Kind, "youtrack") && sc.Tracker.Project != ""
}

func (s *Service) settings() config.Engine {
	if s.Settings != nil {
		return s.Settings()
	}
	return config.LoadEngine()
}

func (s *Service) client() (youtrack.Client, error) {
	var c youtrack.Client
	if s.Client != nil {
		c = s.Client()
	} else {
		yt := s.settings().YouTrack
		c = youtrack.Client{URL: yt.URL, Token: yt.Token}
	}
	if !c.Configured() {
		return c, fmt.Errorf("YouTrack is not set up yet (Admin → Connections)")
	}
	return c, nil
}

func (s *Service) emit(sc *Scope, agent, typ string, data map[string]any) {
	if s.Bus != nil {
		s.Bus.Emit(sc.ID, agent, typ, data)
	}
}

// Register tells the service about a job or pass. level overrides the
// tracker's own level when set (passes choose one when they start).
func (s *Service) Register(id, repo, issue, ship, level string, pass bool) *Scope {
	tr := config.Tracker{}
	if p, err := config.LoadProject(repo); err == nil {
		tr = p.Tracker
	}
	tr = tr.Normalised()
	if l := config.ValidLevel(level); l != "" {
		tr.Level = l
	}
	sc := &Scope{ID: id, Repo: repo, Issue: issue, Ship: ship, Level: tr.Level, Tracker: tr, svc: s, cap: tr.MaxCreatedPerJob,
		queue: make(chan func(), 32), workerUp: true, lastSeen: time.Now().UnixMilli()}
	if pass {
		sc.cap = tr.MaxCreatedPerPass
	}
	s.mu.Lock()
	if s.scopes == nil {
		s.scopes = map[string]*Scope{}
	}
	s.scopes[id] = sc
	s.mu.Unlock()
	go sc.worker()
	if issue != "" && sc.Enabled() && s.PollEvery > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		sc.stopPoll = cancel
		go s.poll(ctx, sc)
	}
	return sc
}

func (s *Service) scope(id string) *Scope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scopes[id]
}

// worker runs a scope's board moves one after another. It outlives Close
// for a short while, so the move to Done after the job's last event still
// goes through.
func (sc *Scope) worker() {
	for {
		select {
		case f := <-sc.queue:
			f()
		case <-time.After(20 * time.Second):
			sc.mu.Lock()
			if sc.closed && len(sc.queue) == 0 {
				sc.workerUp = false
				sc.mu.Unlock()
				return
			}
			sc.mu.Unlock()
		}
	}
}

func (sc *Scope) enqueue(f func()) {
	sc.mu.Lock()
	up := sc.workerUp
	sc.mu.Unlock()
	if !up {
		return
	}
	select {
	case sc.queue <- f:
	default: // a full queue drops board moves; they are a convenience, never a blocker
	}
}

// Wait returns once everything queued for the scope so far has run.
func (s *Service) Wait(id string) {
	sc := s.scope(id)
	if sc == nil {
		return
	}
	done := make(chan struct{})
	sc.enqueue(func() { close(done) })
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// Close ends the scope's Hermes session and comment polling. Knowledge
// stays in the store.
func (s *Service) Close(id string) {
	sc := s.scope(id)
	if sc == nil {
		return
	}
	sc.mu.Lock()
	sc.closed = true
	a, stop := sc.agent, sc.stopPoll
	sc.agent = nil
	sc.mu.Unlock()
	if a != nil {
		a.Stop()
	}
	if stop != nil {
		stop()
	}
}

// ---- the guard ----

var issueRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*-[0-9]+$`)

// Action is one tracker write, as typed fields. Nothing the agent writes is
// ever passed on as a raw YouTrack command.
type Action struct {
	Kind        string            `json:"kind"` // create, update, move, comment, link, attach
	Project     string            `json:"project,omitempty"`
	Issue       string            `json:"issue,omitempty"`
	Summary     string            `json:"summary,omitempty"`
	Description string            `json:"description,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"` // Type, Priority, Subsystem, Complexity, Ship mode, Assignee, State
	Tags        []string          `json:"tags,omitempty"`
	Links       []LinkSpec        `json:"links,omitempty"`
	Text        string            `json:"text,omitempty"`
	Path        string            `json:"path,omitempty"`
}

type LinkSpec struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

var allowedFields = map[string]string{"type": "Type", "priority": "Priority", "subsystem": "Subsystem", "complexity": "Complexity",
	"ship mode": "Ship mode", "ship_mode": "Ship mode", "assignee": "Assignee", "state": "State"}

var linkPhrases = map[string]string{"subtask of": "subtask of", "parent for": "parent for", "relates to": "relates to", "relates": "relates to",
	"duplicates": "duplicates", "is duplicated by": "is duplicated by", "depends on": "depends on", "is required for": "is required for"}

// Describe says the action in plain words, for the review queue.
func (a Action) Describe() string {
	switch a.Kind {
	case "create":
		t := a.Fields["Type"]
		if t == "" {
			t = "issue"
		}
		return fmt.Sprintf("Create %s in %s: %s", strings.ToLower(t), a.Project, a.Summary)
	case "move":
		return fmt.Sprintf("Move %s to %s", a.Issue, a.Fields["State"])
	case "update":
		var parts []string
		for k, v := range a.Fields {
			parts = append(parts, k+" "+v)
		}
		return fmt.Sprintf("Update %s: %s", a.Issue, strings.Join(sortStrings(parts), ", "))
	case "comment":
		return fmt.Sprintf("Comment on %s: %s", a.Issue, firstLine(a.Text, 80))
	case "link":
		if len(a.Links) > 0 {
			return fmt.Sprintf("Link %s %s %s", a.Issue, a.Links[0].Type, a.Links[0].ID)
		}
	case "attach":
		return fmt.Sprintf("Attach %s to %s", a.Path, a.Issue)
	}
	return a.Kind + " " + a.Issue
}

func sortStrings(x []string) []string {
	for i := range x {
		for j := i + 1; j < len(x); j++ {
			if x[j] < x[i] {
				x[i], x[j] = x[j], x[i]
			}
		}
	}
	return x
}

func firstLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(s); len(r) > n {
		s = string(r[:n-1]) + "…"
	}
	return s
}

// inProject checks an issue ID against the mapped project.
func (sc *Scope) inProject(id string) error {
	if !issueRe.MatchString(id) {
		return fmt.Errorf("%q is not an issue ID like %s-12", id, sc.Tracker.Project)
	}
	if p, _, _ := strings.Cut(id, "-"); !strings.EqualFold(p, sc.Tracker.Project) {
		return fmt.Errorf("%s is outside this repo's tracker project %s", id, sc.Tracker.Project)
	}
	return nil
}

// validate checks an action's typed fields and normalises them.
func (sc *Scope) validate(a *Action) error {
	if !sc.Enabled() {
		return fmt.Errorf("no tracker is mapped to this repo: add a tracker block to factory.yml")
	}
	if a.Kind == "create" {
		if a.Project == "" {
			a.Project = sc.Tracker.Project
		}
		if !strings.EqualFold(a.Project, sc.Tracker.Project) {
			return fmt.Errorf("project %s is not mapped to this repo; use %s", a.Project, sc.Tracker.Project)
		}
		a.Project = sc.Tracker.Project
		if strings.TrimSpace(a.Summary) == "" {
			return fmt.Errorf("an issue needs a summary")
		}
	} else if err := sc.inProject(a.Issue); err != nil {
		return err
	}
	norm := map[string]string{}
	for k, v := range a.Fields {
		name, ok := allowedFields[strings.ToLower(strings.TrimSpace(k))]
		if !ok {
			return fmt.Errorf("field %q can't be set; use Type, Priority, Subsystem, Complexity, Ship mode, Assignee or State", k)
		}
		if _, err := youtrack.Quote(v); err != nil {
			return fmt.Errorf("field %s: %w", name, err)
		}
		norm[name] = strings.TrimSpace(v)
	}
	a.Fields = norm
	for _, t := range a.Tags {
		if strings.ContainsAny(t, "{}\n\r") || strings.TrimSpace(t) == "" {
			return fmt.Errorf("tag %q is not valid", t)
		}
	}
	for i, l := range a.Links {
		phrase, ok := linkPhrases[strings.ToLower(strings.TrimSpace(l.Type))]
		if !ok {
			return fmt.Errorf("link type %q is not one of: subtask of, parent for, relates to, duplicates, is duplicated by, depends on, is required for", l.Type)
		}
		if err := sc.inProject(l.ID); err != nil {
			return err
		}
		a.Links[i].Type = phrase
	}
	switch a.Kind {
	case "comment":
		if strings.TrimSpace(a.Text) == "" {
			return fmt.Errorf("a comment needs text")
		}
	case "move":
		if a.Fields["State"] == "" {
			return fmt.Errorf("a move needs a state")
		}
	case "update":
		if len(a.Fields) == 0 {
			return fmt.Errorf("an update needs fields")
		}
	case "link":
		if len(a.Links) == 0 {
			return fmt.Errorf("a link needs a type and a target")
		}
	case "attach":
		if a.Path == "" {
			return fmt.Errorf("an attachment needs a path")
		}
	case "create":
	default:
		return fmt.Errorf("unknown action %q", a.Kind)
	}
	return nil
}

// ownsTicket says whether the factory made the ticket: Hermes created it,
// or it is the job's own.
func (s *Service) ownsTicket(sc *Scope, issue string) bool {
	return strings.EqualFold(issue, sc.Issue) && issue != "" || s.Store != nil && s.Store.Created(issue)
}

// Write is the guarded way to change the tracker. It returns what to tell
// the agent: the result, or that a draft was queued.
func (s *Service) Write(sc *Scope, agent string, a Action) (string, error) {
	if err := sc.validate(&a); err != nil {
		s.emit(sc, Hermes, "tracker.refused", map[string]any{"from": agent, "action": a.Kind, "issue": a.Issue, "reason": err.Error()})
		return "", err
	}
	refuse := func(why string) (string, error) {
		s.emit(sc, Hermes, "tracker.refused", map[string]any{"from": agent, "action": a.Kind, "issue": a.Issue, "reason": why})
		return "", fmt.Errorf("%s", why)
	}
	if sc.Level == "observe" {
		return refuse("this pass is at the Observe level: the operations agent only reads. Report what you would change with report, or ask for a higher level")
	}
	if a.Kind == "create" {
		sc.mu.Lock()
		if sc.created >= sc.cap {
			sc.mu.Unlock()
			return refuse(fmt.Sprintf("the limit of %d new issues for this run is reached", sc.cap))
		}
		sc.created++
		sc.mu.Unlock()
	}
	foreign := (a.Kind == "update" || a.Kind == "move") && !s.ownsTicket(sc, a.Issue)
	stateChange := a.Kind == "update" || a.Kind == "move"
	if sc.Level == "propose" || foreign || (sc.Level == "file" && stateChange) {
		return s.draft(sc, agent, a, foreign)
	}
	id, msg, err := s.execute(sc, agent, a)
	if err != nil {
		if a.Kind == "create" {
			sc.mu.Lock()
			sc.created--
			sc.mu.Unlock()
		}
		return "", err
	}
	_ = id
	return msg, nil
}

func (s *Service) draft(sc *Scope, agent string, a Action, foreign bool) (string, error) {
	args, _ := json.Marshal(a)
	sum := a.Describe()
	if pending, _ := s.Store.Drafts("pending"); pending != nil {
		for _, d := range pending { // the same draft twice is one draft
			if d.Job == sc.ID && d.Summary == sum {
				return "Already waiting in the review queue: " + sum, nil
			}
		}
	}
	id, err := s.Store.AddDraft(store.Draft{Repo: sc.Repo, Job: sc.ID, Agent: agent, Action: a.Kind, Args: args, Summary: sum})
	if err != nil {
		return "", err
	}
	why := "level " + sc.Level
	if foreign {
		why = "the factory didn't create this ticket"
	}
	s.emit(sc, Hermes, "tracker.draft", map[string]any{"from": agent, "draft": id, "action": a.Kind, "issue": a.Issue, "summary": sum, "why": why})
	return fmt.Sprintf("Queued as draft %d for the person running the factory to approve (%s): %s. Nothing reached YouTrack.", id, why, sum), nil
}

// execute runs an action against YouTrack and logs it. It returns the
// issue it touched.
func (s *Service) execute(sc *Scope, agent string, a Action) (issue, msg string, err error) {
	c, err := s.client()
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	data := func(typ string, extra map[string]any) {
		d := map[string]any{"from": agent, "issue": issue}
		for k, v := range extra {
			d[k] = v
		}
		s.emit(sc, Hermes, typ, d)
	}
	switch a.Kind {
	case "create":
		issue, err = c.Create(ctx, youtrack.NewIssue{Project: a.Project, Summary: strings.TrimSpace(a.Summary), Description: a.Description})
		if err != nil {
			return "", "", err
		}
		s.Store.MarkCreated(issue, sc.ID)
		var warn []string
		for _, name := range sortStrings(keys(a.Fields)) {
			warn = append(warn, s.command(ctx, c, issue, name, a.Fields[name])...)
		}
		for _, t := range a.Tags {
			q, _ := youtrack.Quote(t)
			if e := c.Command(ctx, []string{issue}, "tag "+q); e != nil {
				warn = append(warn, "tag "+t+": "+e.Error())
			}
		}
		for _, l := range a.Links {
			if e := c.Command(ctx, []string{issue}, l.Type+" "+l.ID); e != nil {
				warn = append(warn, "link "+l.Type+" "+l.ID+": "+e.Error())
			}
		}
		data("tracker.issue.created", map[string]any{"summary": a.Summary, "project": a.Project, "fields": a.Fields, "tags": a.Tags, "links": a.Links, "warnings": warn})
		msg = "Created " + issue
		if len(warn) > 0 {
			msg += " (some fields did not apply: " + strings.Join(warn, "; ") + ")"
		}
		return issue, msg, nil
	case "move", "update":
		issue = a.Issue
		for _, name := range sortStrings(keys(a.Fields)) {
			if w := s.command(ctx, c, issue, name, a.Fields[name]); len(w) > 0 {
				return issue, "", fmt.Errorf("%s", w[0])
			}
		}
		if a.Kind == "move" {
			data("tracker.issue.moved", map[string]any{"state": a.Fields["State"]})
			return issue, issue + " moved to " + a.Fields["State"], nil
		}
		data("tracker.issue.updated", map[string]any{"fields": a.Fields})
		return issue, issue + " updated", nil
	case "comment":
		issue = a.Issue
		if err := c.AddComment(ctx, issue, Marker+strings.TrimSpace(a.Text)); err != nil {
			return issue, "", err
		}
		data("tracker.comment.added", map[string]any{"text": firstLine(a.Text, 200)})
		return issue, "Comment added to " + issue, nil
	case "link":
		issue = a.Issue
		for _, l := range a.Links {
			if err := c.Command(ctx, []string{issue}, l.Type+" "+l.ID); err != nil {
				return issue, "", err
			}
			data("tracker.link.added", map[string]any{"type": l.Type, "target": l.ID})
		}
		for _, t := range a.Tags {
			q, _ := youtrack.Quote(t)
			if err := c.Command(ctx, []string{issue}, "tag "+q); err != nil {
				return issue, "", err
			}
		}
		return issue, "Linked " + issue, nil
	case "attach":
		issue = a.Issue
		path, err := sc.attachable(a.Path)
		if err != nil {
			return issue, "", err
		}
		if err := c.Attach(ctx, issue, path); err != nil {
			return issue, "", err
		}
		data("tracker.attachment.added", map[string]any{"path": a.Path})
		return issue, "Attached " + a.Path + " to " + issue, nil
	}
	return "", "", fmt.Errorf("unknown action %q", a.Kind)
}

func (s *Service) command(ctx context.Context, c youtrack.Client, issue, field, value string) []string {
	n, _ := youtrack.Quote(field)
	v, _ := youtrack.Quote(value)
	if err := c.Command(ctx, []string{issue}, n+" "+v); err != nil {
		return []string{field + " " + value + ": " + err.Error()}
	}
	return nil
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---- drafts ----

// Approve runs a pending draft, once. A failure leaves the draft pending
// with YouTrack's error.
func (s *Service) Approve(id int64) (string, error) {
	d, err := s.Store.Draft(id)
	if err != nil {
		return "", err
	}
	if !s.Store.ClaimDraft(id, "approved") {
		return "", fmt.Errorf("draft %d was already handled", id)
	}
	var a Action
	if err := json.Unmarshal(d.Args, &a); err != nil {
		s.Store.FinishDraft(id, "pending", "error: "+err.Error())
		return "", err
	}
	sc := s.scope(d.Job)
	if sc == nil || sc.Repo != d.Repo {
		sc = &Scope{ID: d.Job, Repo: d.Repo, svc: s}
		if p, e := config.LoadProject(d.Repo); e == nil {
			sc.Tracker = p.Tracker.Normalised()
		}
	}
	if err := sc.validate(&a); err != nil {
		s.Store.FinishDraft(id, "pending", "error: "+err.Error())
		return "", err
	}
	issue, _, err := s.execute(sc, d.Agent, a)
	if err != nil {
		s.Store.FinishDraft(id, "pending", "error: "+err.Error())
		return "", err
	}
	s.Store.FinishDraft(id, "approved", issue)
	s.emit(sc, Hermes, "tracker.draft.approved", map[string]any{"draft": id, "issue": issue, "from": d.Agent})
	return issue, nil
}

// Reject drops a pending draft.
func (s *Service) Reject(id int64) error {
	d, err := s.Store.Draft(id)
	if err != nil {
		return err
	}
	if !s.Store.ClaimDraft(id, "rejected") {
		return fmt.Errorf("draft %d was already handled", id)
	}
	if s.Bus != nil {
		s.Bus.Emit(d.Job, Hermes, "tracker.draft.rejected", map[string]any{"draft": id, "from": d.Agent})
	}
	return nil
}
