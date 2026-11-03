package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/handoff"
	"factory/internal/prompts"
	"factory/internal/runtime"
	"factory/internal/store"
	"factory/internal/youtrack"
)

// attachable resolves a file path the agent wants to attach. Only files
// inside the repo or the factory's own folder, up to 25 MB.
func (sc *Scope) attachable(p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(sc.Repo, p)
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("no file %s", p)
	}
	for _, root := range []string{sc.Repo, config.Home()} {
		if r, err := filepath.EvalSymlinks(root); err == nil && root != "" {
			if rel, err := filepath.Rel(r, real); err == nil && !strings.HasPrefix(rel, "..") {
				if fi, err := os.Stat(real); err != nil || !fi.Mode().IsRegular() || fi.Size() > 25<<20 {
					return "", fmt.Errorf("%s is not a regular file under 25 MB", p)
				}
				return real, nil
			}
		}
	}
	return "", fmt.Errorf("%s is outside the repo and the factory's folder", p)
}

// ---- reads ----

var projectRe = regexp.MustCompile(`(?i)project:\s*\{?([A-Za-z0-9_-]+)`)

// Search runs a YouTrack query inside the mapped project.
func (s *Service) Search(sc *Scope, agent, query string) (string, error) {
	if !sc.Enabled() {
		return "", fmt.Errorf("no tracker is mapped to this repo: add a tracker block to factory.yml")
	}
	for _, m := range projectRe.FindAllStringSubmatch(query, -1) {
		if !strings.EqualFold(m[1], sc.Tracker.Project) {
			return "", fmt.Errorf("project %s is not mapped to this repo; search %s", m[1], sc.Tracker.Project)
		}
	}
	// "or" binds looser than the implicit AND, so the project term has to
	// wrap the whole query, never sit next to it
	// an empty query lists the whole project
	if q := strings.TrimSpace(query); q == "" {
		query = "project: " + sc.Tracker.Project
	} else {
		query = "project: " + sc.Tracker.Project + " and (" + q + ")"
	}
	c, err := s.client()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	all, err := c.Search(ctx, query, 20)
	if err != nil {
		s.emit(sc, Hermes, "tracker.search", map[string]any{"from": agent, "query": query, "error": err.Error()})
		return "", err
	}
	// whatever the query did, only issues of the mapped project go on
	var list []youtrack.Brief
	for _, is := range all {
		if sc.inProject(is.ID) == nil {
			list = append(list, is)
		}
	}
	s.emit(sc, Hermes, "tracker.search", map[string]any{"from": agent, "query": query, "found": len(list), "dropped": len(all) - len(list)})
	if len(list) == 0 {
		return "No issues match: " + query, nil
	}
	var b strings.Builder
	for _, is := range list {
		fmt.Fprintf(&b, "%s [%s] %s\n", is.ID, is.State, is.Summary)
	}
	return strings.TrimSpace(b.String()), nil
}

// Read gives one issue with its comments, links, attachments and history.
func (s *Service) Read(sc *Scope, agent, id string) (string, error) {
	if !sc.Enabled() {
		return "", fmt.Errorf("no tracker is mapped to this repo: add a tracker block to factory.yml")
	}
	if err := sc.inProject(id); err != nil {
		return "", err
	}
	c, err := s.client()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	d, err := c.Read(ctx, id)
	s.emit(sc, Hermes, "tracker.read", map[string]any{"from": agent, "issue": id})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", d.ID, d.Summary)
	for _, k := range sortStrings(keys(d.Fields)) {
		fmt.Fprintf(&b, "%s: %s\n", k, d.Fields[k])
	}
	if len(d.Tags) > 0 {
		fmt.Fprintf(&b, "Tags: %s\n", strings.Join(d.Tags, ", "))
	}
	fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(d.Description))
	for _, l := range d.Links {
		fmt.Fprintf(&b, "\nLink %s (%s): %s", l.Type, l.Dir, strings.Join(l.Issues, ", "))
	}
	if len(d.Attachments) > 0 {
		fmt.Fprintf(&b, "\nAttachments: %s", strings.Join(d.Attachments, ", "))
	}
	for _, cm := range d.Comments {
		fmt.Fprintf(&b, "\n\nComment by %s: %s", cm.Author.Login, strings.TrimSpace(cm.Text))
	}
	if len(d.Activity) > 0 {
		fmt.Fprintf(&b, "\n\nRecent changes:\n%s", strings.Join(d.Activity, "\n"))
	}
	return strings.TrimSpace(b.String()), nil
}

// ---- knowledge ----

// Report stores a note an agent brought in.
func (s *Service) Report(scopeID, agent, kind, text string, issues []string) error {
	sc := s.scope(scopeID)
	if sc == nil {
		return fmt.Errorf("no job or pass %s", scopeID)
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "did", "decision", "finding", "blocker", "user_note":
	default:
		return fmt.Errorf("kind is one of did, decision, finding, blocker, user_note")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("say what you want to report")
	}
	s.note(sc, agent, kind, text, issues, 0)
	s.emit(sc, agent, "ops.report", map[string]any{"to": Hermes, "kind": kind, "text": firstLine(text, 300), "issues": issues})
	return nil
}

func (s *Service) note(sc *Scope, agent, kind, text string, issues []string, seq int64) {
	if s.Store == nil || strings.TrimSpace(text) == "" {
		return
	}
	all := issues
	if sc.Issue != "" {
		all = append([]string{sc.Issue}, issues...)
	}
	s.Store.AddNote(store.Note{Repo: sc.Repo, Job: sc.ID, Issue: strings.Join(all, ","), Agent: agent, Kind: kind, Text: text, Seq: seq})
}

var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]{3,}|[A-Za-z]+-[0-9]+`)

func words(q string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range wordRe.FindAllString(q, -1) {
		if lw := strings.ToLower(w); !seen[lw] && !stop[lw] {
			seen[lw] = true
			out = append(out, w)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

var stop = map[string]bool{"what": true, "that": true, "this": true, "with": true, "there": true, "about": true, "ticket": true, "which": true, "have": true, "does": true, "decide": true, "decided": true}

// Knowledge searches the notes of the scope's repo.
func (s *Service) Knowledge(sc *Scope, query string) (string, error) {
	notes, err := s.Store.Notes(sc.Repo, "", "", words(query), 15)
	if err != nil {
		return "", err
	}
	if len(notes) == 0 {
		return "No notes match.", nil
	}
	return formatNotes(notes), nil
}

func formatNotes(notes []store.Note) string {
	var b strings.Builder
	for _, n := range notes {
		src := "job " + n.Job
		if n.Issue != "" {
			src = n.Issue + ", " + src
		}
		fmt.Fprintf(&b, "- [%s by %s; %s; event #%d] %s\n", n.Kind, n.Agent, src, n.Seq, firstLine(n.Text, 400))
	}
	return strings.TrimSpace(b.String())
}

// ---- the Hermes session ----

func (s *Service) session(ctx context.Context, sc *Scope) (runtime.Agent, error) {
	sc.mu.Lock()
	a := sc.agent
	sc.mu.Unlock()
	if a != nil {
		return a, nil
	}
	role := s.settings().RoleForProject(sc.Repo, "ops")
	rt := s.Runtimes[role.Runtime]
	if rt == nil {
		return nil, fmt.Errorf("the operations agent has no runtime %q", role.Runtime)
	}
	a, err := rt.Start(ctx, runtime.AgentSpec{Job: sc.ID, Agent: Hermes, Cwd: sc.Repo, Model: role.Model, System: prompts.For("ops"),
		Tools: []string{"read", "grep", "find", "ls"}, Role: "ops"})
	if err != nil {
		return nil, fmt.Errorf("start the operations agent: %w", err)
	}
	sc.mu.Lock()
	if sc.closed {
		sc.mu.Unlock()
		a.Stop()
		return nil, fmt.Errorf("the job is over")
	}
	sc.agent = a
	sc.mu.Unlock()
	return a, nil
}

// turn gives Hermes one request and returns what it answered with the
// answer or handover tool, plus its plain reply.
func (s *Service) turn(sc *Scope, serving, prompt string) (output, string, error) {
	sc.turnMu.Lock()
	defer sc.turnMu.Unlock()
	sc.mu.Lock()
	sc.serving = serving
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		sc.serving = ""
		sc.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a, err := s.session(ctx, sc)
	if err != nil {
		return output{}, "", err
	}
	sc.mu.Lock()
	sc.out = output{}
	sc.mu.Unlock()
	text, err := a.Prompt(ctx, prompt)
	sc.mu.Lock()
	out := sc.out
	sc.mu.Unlock()
	return out, strings.TrimSpace(text), err
}

// Ask answers one agent's question, with sources.
func (s *Service) Ask(scopeID, from, question string) (string, error) {
	sc := s.scope(scopeID)
	if sc == nil {
		return "", fmt.Errorf("no job or pass %s", scopeID)
	}
	if strings.TrimSpace(question) == "" {
		return "", fmt.Errorf("ask a question")
	}
	s.emit(sc, from, "ops.request", map[string]any{"to": Hermes, "question": question})
	var known string
	if notes, _ := s.Store.Notes(sc.Repo, "", "", words(question), 12); len(notes) > 0 {
		known = "\n\nNotes that may help:\n" + formatNotes(notes)
	}
	out, text, err := s.turn(sc, from, fmt.Sprintf("%s asks you (job %s, ticket %s):\n\n%s%s\n\nLook it up with your tools if the notes don't settle it, then call answer with a short reply and its sources (issue IDs, job, agent, event #).",
		from, sc.ID, orNone(sc.Issue), question, known))
	if err != nil {
		return "", err
	}
	answer, sources := out.answer, out.sources
	if answer == "" {
		answer = text
	}
	if answer == "" {
		return "", fmt.Errorf("the operations agent gave no answer")
	}
	src := "none found"
	if len(sources) > 0 {
		src = strings.Join(sources, ", ")
	}
	s.emit(sc, Hermes, "ops.answer", map[string]any{"to": from, "question": firstLine(question, 200), "answer": answer, "sources": sources})
	s.note(sc, Hermes, "answer", "Asked by "+from+": "+firstLine(question, 200)+" → "+answer, nil, 0)
	return answer + "\n\nSources: " + src, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Handover builds the context the next agent starts from.
func (s *Service) Handover(scopeID, from, to string, n *handoff.Note) (string, error) {
	sc := s.scope(scopeID)
	if sc == nil {
		return "", fmt.Errorf("no job or pass %s", scopeID)
	}
	var raw []byte
	if n != nil {
		raw, _ = json.Marshal(n)
	}
	var known string
	if notes, _ := s.Store.Notes(sc.Repo, "", sc.Issue, nil, 12); len(notes) > 0 {
		known = "\n\nNotes on this ticket:\n" + formatNotes(notes)
	}
	out, _, err := s.turn(sc, from, fmt.Sprintf("Work moves from %s to %s (job %s, ticket %s). %s's handoff note:\n%s%s\n\nCall handover with what %s needs to start without asking again: short context, the decisions already made, open points, and links (issue IDs, files). Keep it compact.",
		from, to, sc.ID, orNone(sc.Issue), from, string(raw), known, to))
	if err != nil {
		return "", err
	}
	if out.handover == nil {
		return "", fmt.Errorf("the operations agent wrote no handover")
	}
	md := out.handover.Markdown(from, to)
	s.emit(sc, Hermes, "ops.handover", map[string]any{"from": from, "to": to, "text": md})
	return md, nil
}

// ---- tools ----

// Tool runs one operations tool for an agent. ask_ops and report work for
// every agent; the rest belong to Hermes.
func (s *Service) Tool(scopeID, agent, tool string, args []byte) (string, error) {
	switch tool {
	case "ask_ops":
		var b struct {
			Question string `json:"question"`
		}
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		return s.Ask(scopeID, agent, b.Question)
	case "report":
		var b struct {
			Kind   string   `json:"kind"`
			Text   string   `json:"text"`
			Issues []string `json:"issues"`
		}
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		if err := s.Report(scopeID, agent, b.Kind, b.Text, b.Issues); err != nil {
			return "", err
		}
		return "Noted. The operations agent keeps it with this job and its tickets.", nil
	}
	if agent != Hermes {
		return "", fmt.Errorf("only the operations agent has the tool %q; use ask_ops to ask it", tool)
	}
	sc := s.scope(scopeID)
	if sc == nil {
		return "", fmt.Errorf("no job or pass %s", scopeID)
	}
	switch tool {
	case "tracker_search":
		var b struct{ Query string }
		json.Unmarshal(args, &b)
		return s.Search(sc, requester(sc), b.Query)
	case "tracker_read":
		var b struct{ ID string }
		json.Unmarshal(args, &b)
		return s.Read(sc, requester(sc), b.ID)
	case "knowledge_search":
		var b struct{ Query string }
		json.Unmarshal(args, &b)
		return s.Knowledge(sc, b.Query)
	case "tracker_create":
		var b struct {
			Project, Summary, Description, Type, Priority, Subsystem, Complexity string
			ShipMode                                                             string     `json:"ship_mode"`
			Tags                                                                 []string   `json:"tags"`
			Links                                                                []LinkSpec `json:"links"`
		}
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		f := map[string]string{}
		for k, v := range map[string]string{"Type": b.Type, "Priority": b.Priority, "Subsystem": b.Subsystem, "Complexity": b.Complexity, "Ship mode": b.ShipMode} {
			if strings.TrimSpace(v) != "" {
				f[k] = v
			}
		}
		return s.Write(sc, requester(sc), Action{Kind: "create", Project: b.Project, Summary: b.Summary, Description: b.Description, Fields: f, Tags: b.Tags, Links: b.Links})
	case "tracker_update":
		var b struct {
			ID     string            `json:"id"`
			Fields map[string]string `json:"fields"`
		}
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		return s.Write(sc, requester(sc), Action{Kind: "update", Issue: b.ID, Fields: b.Fields})
	case "tracker_move":
		var b struct{ ID, State string }
		json.Unmarshal(args, &b)
		return s.Write(sc, requester(sc), Action{Kind: "move", Issue: b.ID, Fields: map[string]string{"State": b.State}})
	case "tracker_comment":
		var b struct{ ID, Text string }
		json.Unmarshal(args, &b)
		return s.Write(sc, requester(sc), Action{Kind: "comment", Issue: b.ID, Text: b.Text})
	case "tracker_link":
		var b struct{ ID, Type, Target, Tag string }
		json.Unmarshal(args, &b)
		a := Action{Kind: "link", Issue: b.ID, Links: []LinkSpec{{Type: b.Type, ID: b.Target}}}
		if b.Tag != "" {
			a.Tags = []string{b.Tag}
		}
		return s.Write(sc, requester(sc), a)
	case "tracker_attach":
		var b struct{ ID, Path string }
		json.Unmarshal(args, &b)
		return s.Write(sc, requester(sc), Action{Kind: "attach", Issue: b.ID, Path: b.Path})
	case "answer":
		var b struct {
			Text    string   `json:"text"`
			Sources []string `json:"sources"`
		}
		if err := json.Unmarshal(args, &b); err != nil {
			return "", err
		}
		sc.mu.Lock()
		sc.out.answer, sc.out.sources = b.Text, b.Sources
		sc.mu.Unlock()
		return "Answer recorded. End your turn.", nil
	case "handover":
		var h Handover
		if err := json.Unmarshal(args, &h); err != nil {
			return "", err
		}
		if strings.TrimSpace(h.Context) == "" {
			return "", fmt.Errorf("a handover needs context")
		}
		sc.mu.Lock()
		sc.out.handover = &h
		sc.mu.Unlock()
		return "Handover recorded. End your turn.", nil
	}
	return "", fmt.Errorf("unknown tool %s", tool)
}

// requester is who a write is on behalf of in the event log: the agent
// whose request Hermes is serving in this turn (turns are serialised), or
// Hermes himself for work the engine starts, like board sync.
func requester(sc *Scope) string {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.serving != "" {
		return sc.serving
	}
	return Hermes
}

// ---- watching the bus: knowledge and the board ----

// Run feeds every event to Observe until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ch, stop := s.Bus.Subscribe("")
	defer stop()
	for {
		select {
		case e := <-ch:
			s.Observe(e)
		case <-ctx.Done():
			return
		}
	}
}

func str(d map[string]any, k string) string { v, _ := d[k].(string); return v }

func strs(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		var out []string
		for _, i := range x {
			if s, ok := i.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Observe turns events into project knowledge and board moves. It never
// blocks the engine and never fails a job.
func (s *Service) Observe(e events.Event) {
	sc := s.scope(e.Job)
	if sc == nil {
		return
	}
	switch e.Type {
	case "handoff.written":
		var n *handoff.Note
		switch v := e.Data["note"].(type) {
		case *handoff.Note:
			n = v
		case handoff.Note:
			n = &v
		}
		if n == nil {
			return
		}
		s.note(sc, e.Agent, "did", n.Did, nil, e.Seq)
		for _, d := range n.Decisions {
			s.note(sc, e.Agent, "decision", d, nil, e.Seq)
		}
		for _, f := range n.Findings {
			s.note(sc, e.Agent, "finding", f, nil, e.Seq)
		}
	case "review.verdict":
		if sum := str(e.Data, "summary"); sum != "" {
			s.note(sc, e.Agent, "decision", "Review verdict: "+sum, nil, e.Seq)
		}
		for _, f := range strs(e.Data["findings"]) {
			s.note(sc, e.Agent, "finding", f, nil, e.Seq)
		}
	case "human.message", "human.guidance":
		s.note(sc, "user", "user_note", str(e.Data, "text"), nil, e.Seq)
	case "human.answered":
		s.note(sc, "user", "user_note", "Answer to "+e.Agent+": "+str(e.Data, "answer"), nil, e.Seq)
	case "job.committed":
		sc.mu.Lock()
		sc.commit, sc.branch = str(e.Data, "sha"), str(e.Data, "branch")
		sc.mu.Unlock()
	case "job.pr_opened":
		sc.mu.Lock()
		sc.pr = str(e.Data, "url")
		sc.mu.Unlock()
	case "job.state":
		s.boardSync(sc, str(e.Data, "state"))
	}
}

// boardSync moves the job's ticket to match its state. State names come
// from factory.yml; an empty name means don't move.
func (s *Service) boardSync(sc *Scope, state string) {
	if sc.Issue == "" || !sc.Enabled() {
		return
	}
	var to string
	switch state {
	case "planning", "building":
		to = sc.Tracker.States.Working
	case "stuck", "waiting":
		to = sc.Tracker.States.Blocked
	case "reviewing":
		to = sc.Tracker.States.Review
	case "done":
		to = sc.Tracker.States.Done
		if sc.Ship == "pr" {
			to = sc.Tracker.States.Review
		}
	default:
		return
	}
	if to == "" {
		return
	}
	sc.enqueue(func() {
		sc.mu.Lock()
		same := sc.lastMove == to && state != "done"
		sc.lastMove = to
		commit, branch, pr := sc.commit, sc.branch, sc.pr
		sc.mu.Unlock()
		if same {
			return
		}
		skip := func(err error) {
			s.emit(sc, Hermes, "tracker.skipped", map[string]any{"issue": sc.Issue, "state": to, "reason": err.Error()})
		}
		if _, err := s.Write(sc, Hermes, Action{Kind: "move", Issue: sc.Issue, Fields: map[string]string{"State": to}}); err != nil {
			skip(err)
			return
		}
		if state == "done" {
			text := fmt.Sprintf("Job %s finished. Commit %s on branch %s.", sc.ID, commit, branch)
			if pr != "" {
				text = fmt.Sprintf("Job %s finished. Pull request: %s", sc.ID, pr)
			}
			if _, err := s.Write(sc, Hermes, Action{Kind: "comment", Issue: sc.Issue, Text: text}); err != nil {
				skip(err)
			}
		}
	})
}

// poll pushes new ticket comments from people to the job's active agent.
func (s *Service) poll(ctx context.Context, sc *Scope) {
	t := time.NewTicker(s.PollEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollOnce(sc)
		}
	}
}

func (s *Service) pollOnce(sc *Scope) {
	c, err := s.client()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sc.mu.Lock()
	since := sc.lastSeen
	sc.mu.Unlock()
	list, err := c.Comments(ctx, sc.Issue, since)
	if err != nil {
		return
	}
	for _, cm := range list {
		sc.mu.Lock()
		if cm.Created > sc.lastSeen {
			sc.lastSeen = cm.Created
		}
		sc.mu.Unlock()
		if strings.HasPrefix(cm.Text, Marker) {
			continue
		}
		who := cm.Author.Name
		if who == "" {
			who = cm.Author.Login
		}
		text := fmt.Sprintf("New comment on %s from %s: %s", sc.Issue, who, strings.TrimSpace(cm.Text))
		s.note(sc, "user", "user_note", text, nil, 0)
		delivered := s.Steer != nil && s.Steer(sc.ID, text)
		s.emit(sc, Hermes, "ops.push", map[string]any{"issue": sc.Issue, "author": who, "text": firstLine(cm.Text, 300), "delivered": delivered})
	}
}
