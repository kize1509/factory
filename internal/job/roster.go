package job

import (
	"factory/internal/events"
	"factory/internal/handoff"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The job owns a roster: who is working on it, in what role and state, whose
// output each agent reads and who reads its own. Agents get it two ways: the
// engine puts a short version at the top of each task it sends (rosterBrief),
// and the roster tool returns the live one. Every change is logged as a
// roster.updated event with the full snapshot, which the map reads.

// Agent states in the roster.
const (
	StateWorking = "working"
	StateWaiting = "waiting"
	StateBlocked = "blocked"
	StateDone    = "done"
)

// Link is one side of a hand-off between two agents.
type Link struct {
	Agent string `json:"agent"`           // who (an agent, or you, oracle, engine)
	What  string `json:"what"`            // what is passed on, e.g. "plan, handoff note"
	Needs string `json:"needs,omitempty"` // what the reader needs from the writer
	Note  string `json:"note,omitempty"`  // path of the handoff note, once it exists
}

// Member is one agent of the job.
type Member struct {
	Agent     string `json:"agent"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	State     string `json:"state"`
	Doing     string `json:"doing,omitempty"`
	Because   string `json:"because,omitempty"` // why it was started
	ReadsFrom []Link `json:"reads_from"`
	WritesTo  []Link `json:"writes_to"`
}

// agentName is the agent's id as a name: aristotle becomes Aristotle.
func agentName(id string) string {
	if id == "" {
		return ""
	}
	return strings.ToUpper(id[:1]) + id[1:]
}

// roleOf says what an agent id does in a job.
func roleOf(id string) string {
	switch id {
	case planner:
		return "planner"
	case reviewer:
		return "reviewer"
	case grader:
		return "grader"
	case operations:
		return "operations"
	case builder:
		return "builder"
	}
	for _, b := range experimentBuilders {
		if b == id {
			return "builder"
		}
	}
	return id
}

// rank orders the roster along the pipeline, so it reads the same every time.
func rank(id string) int {
	switch roleOf(id) {
	case "operations":
		return 0
	case "planner":
		return 1
	case "builder":
		for i, b := range experimentBuilders {
			if b == id {
				return 10 + i
			}
		}
		return 20
	case "grader":
		return 30
	case "reviewer":
		return 40
	}
	return 50
}

// join adds an agent to the roster (or updates the one already there) and
// says why it was started.
func (j *Job) join(id, because string) {
	j.mu.Lock()
	if j.roster == nil {
		j.roster = map[string]*Member{}
	}
	m := j.roster[id]
	if m == nil {
		m = &Member{Agent: id, Name: agentName(id), Role: roleOf(id)}
		j.roster[id] = m
	}
	m.State, m.Because = StateWaiting, because
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.publish(snap)
}

// depart takes an agent off the roster when its session ends for good.
func (j *Job) depart(id string) {
	j.mu.Lock()
	delete(j.roster, id)
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.publish(snap)
}

// setMember changes one member and logs the new roster.
func (j *Job) setMember(id string, f func(*Member)) {
	j.mu.Lock()
	m := j.roster[id]
	if m == nil {
		j.mu.Unlock()
		return
	}
	f(m)
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.publish(snap)
}

// SetDoing records what an agent says it is doing now (set_status).
func (j *Job) SetDoing(id, step string) {
	step = strings.TrimSpace(step)
	j.setMember(id, func(m *Member) { m.Doing = step })
}

// publish logs a roster snapshot, unless nothing changed since the last one.
func (j *Job) publish(snap []Member) {
	data := mustJSON(snap)
	j.mu.Lock()
	same := string(data) == j.lastRoster
	j.lastRoster = string(data)
	j.mu.Unlock()
	if same {
		return
	}
	j.emit("", "roster.updated", map[string]any{"members": snap})
}

// snapshotLocked is the whole roster in pipeline order, with each member's
// links filled in. The caller holds j.mu.
func (j *Job) snapshotLocked() []Member {
	out := make([]Member, 0, len(j.roster))
	for id, m := range j.roster {
		c := *m
		c.ReadsFrom, c.WritesTo = j.flowLocked(id)
		out = append(out, c)
	}
	sort.Slice(out, func(a, b int) bool {
		if ra, rb := rank(out[a].Agent), rank(out[b].Agent); ra != rb {
			return ra < rb
		}
		return out[a].Agent < out[b].Agent
	})
	return out
}

// Roster returns the roster as it is now.
func (j *Job) Roster() []Member {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.snapshotLocked()
}

// buildersLocked lists the builders in the job, in order; the main builder
// when there is none yet.
func (j *Job) buildersLocked() []string {
	var out []string
	for id := range j.roster {
		if roleOf(id) == "builder" {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(a, b int) bool { return rank(out[a]) < rank(out[b]) })
	if len(out) == 0 {
		out = []string{builder}
	}
	return out
}

// noteLocked is the handoff note of an agent, when it has written one.
func (j *Job) noteLocked(id string) string {
	p := filepath.Join(j.Dir(), "handoff", id+".json")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// flowLocked is the fixed flow of a job: the person and the ticket feed the
// planner, the planner feeds the builders, the builders feed the checks and
// the reviewer (or, in a Symposium, the grader), and the reviewer feeds the
// builder and the commit. The caller holds j.mu.
func (j *Job) flowLocked(id string) (reads, writes []Link) {
	// after a restart the planner's session is gone, but its note is still there
	hasPlanner := j.roster[planner] != nil || j.noteLocked(planner) != ""
	switch roleOf(id) {
	case "planner":
		reads = []Link{{Agent: you, What: "the ticket and your notes"}}
		for _, b := range j.buildersLocked() {
			writes = append(writes, Link{Agent: b, What: "the plan, as a handoff note", Needs: "the approach, the files, the order, the risks and a first concrete step, so they don't have to guess", Note: j.noteLocked(id)})
		}
	case "builder":
		if hasPlanner {
			reads = []Link{{Agent: planner, What: "the plan, as a handoff note", Note: j.noteLocked(planner)}}
		} else {
			reads = []Link{{Agent: you, What: "the ticket and your notes"}}
		}
		writes = []Link{{Agent: "oracle", What: "your changes in the working folder", Needs: "work the engine's checks can run against"}}
		if j.inExperiment[id] {
			writes = append(writes, Link{Agent: grader, What: "your diff, without your name", Needs: "a diff that stands on its own: the grader doesn't know who built it"})
		} else {
			writes = append(writes, Link{Agent: reviewer, What: "your diff and your handoff note", Needs: "the reasoning behind each change, not only the diff"})
		}
	case "reviewer":
		lead := j.lead
		if lead == "" {
			lead = builder
		}
		reads = []Link{{Agent: lead, What: "the diff, the handoff note and answers to your questions", Note: j.noteLocked(lead)}}
		writes = []Link{
			{Agent: lead, What: "blocking findings", Needs: "a file, a line, why it matters and how to fix it"},
			{Agent: "engine", What: "your verdict", Needs: "approved, or blocking findings: the engine commits only on approval"},
		}
	case "grader":
		for _, b := range j.buildersLocked() {
			if j.inExperiment[b] {
				reads = append(reads, Link{Agent: b, What: "its diff, known to you only by a letter"})
			}
		}
		writes = []Link{{Agent: "engine", What: "a grade from 1 to 5 for each experiment", Needs: "a score and a short reason"}}
	}
	return reads, writes
}

// handoverTarget is who a handoff note goes to: the agent its "to" names, or
// else the next agent in the flow, or the engine.
func (j *Job) handoverTarget(agent string, n *handoff.Note) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if f := strings.Fields(n.To); len(f) > 0 {
		to := strings.ToLower(strings.Trim(f[0], ".,:;"))
		if j.roster[to] != nil && to != agent {
			return to
		}
	}
	_, writes := j.flowLocked(agent)
	for _, l := range writes {
		if isAgent(l.Agent) && l.Agent != agent {
			return l.Agent
		}
	}
	return "engine"
}

// who is the name an agent, the person or the engine goes by in text.
func who(id string) string {
	switch id {
	case you:
		return "The person running the factory"
	case "oracle":
		return "The engine's checks"
	case "engine":
		return "The engine"
	}
	return agentName(id)
}

// isAgent says whether an id in a Link is an agent of the job.
func isAgent(id string) bool {
	switch id {
	case you, "oracle", "engine":
		return false
	}
	return true
}

// RosterFor writes an agent's view of the job as short markdown. Live adds
// what each agent is doing now and its state; the version the engine puts in
// a prompt leaves those out where they would change from one run to the next.
func (j *Job) RosterFor(id string, live bool) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	me := j.roster[id]
	if me == nil {
		return ""
	}
	ticket := j.Issue
	if ticket == "" {
		ticket = j.ID
	}
	reads, writes := j.flowLocked(id)
	var b strings.Builder
	fmt.Fprintf(&b, "## Your job roster\n\nYou are %s, the %s, on job %s (ticket %s).\nThe job is at this step: %s.\n", me.Name, me.Role, j.ID, ticket, phaseName(j.State))
	if me.Because != "" {
		fmt.Fprintf(&b, "You were started because %s.\n", me.Because)
	}
	if live && me.Doing != "" {
		fmt.Fprintf(&b, "You said you are doing: %s.\n", me.Doing)
	}
	b.WriteString("\nWho is in this job:\n")
	for _, m := range j.snapshotLocked() {
		if m.Agent == id {
			continue
		}
		line := fmt.Sprintf("- %s (%s)", m.Name, m.Role)
		switch {
		case live:
			line += ", " + m.State
			if m.Doing != "" {
				line += ": " + m.Doing
			}
		case j.inExperiment[m.Agent] && j.inExperiment[id]:
			line += ", building another approach" // their state changes while you work
		case !j.inExperiment[m.Agent]:
			line += ", " + m.State
		}
		b.WriteString(line + "\n")
	}
	if len(j.roster) == 1 {
		b.WriteString("- Nobody else yet.\n")
	}
	b.WriteString("\nYou read from:\n")
	if len(reads) == 0 {
		b.WriteString("- Nobody yet.\n")
	}
	for _, l := range reads {
		fmt.Fprintf(&b, "- %s: %s", who(l.Agent), l.What)
		if l.Note != "" {
			fmt.Fprintf(&b, " (%s)", l.Note)
		}
		if isAgent(l.Agent) {
			fmt.Fprintf(&b, ". If it is unclear, ask them with ask_agent before you guess")
		}
		b.WriteString("\n")
	}
	if roleOf(id) == "builder" && len(reads) > 0 && reads[0].Agent == planner {
		if paths, more := j.plannerReads(); len(paths) > 0 {
			fmt.Fprintf(&b, "\n%s already read: %s", agentName(planner), strings.Join(paths, ", "))
			if more > 0 {
				fmt.Fprintf(&b, ", and %d more", more)
			}
			b.WriteString(". Don't explore those again; use the plan and read only what it doesn't cover.\n")
		}
	}
	b.WriteString("\nYou write to:\n")
	if len(writes) == 0 {
		b.WriteString("- Nobody yet.\n")
	}
	for _, l := range writes {
		fmt.Fprintf(&b, "- %s: %s", who(l.Agent), l.What)
		if l.Needs != "" {
			fmt.Fprintf(&b, ". They need: %s", l.Needs)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nWho to ask:\n" +
		"- About your input: the agent you read from, with ask_agent. It waits for the answer, then you carry on.\n" +
		"- Not sure who knows: ask_agent to \"operations\".\n" +
		"- A credential, access or a decision only the person can make: ask_human, then end your turn.\n" +
		"- Your finished work: write your handoff note; the engine delivers it to the next step.\n" +
		"- Who is working now: call the roster tool.\n")
	return b.String()
}

// rosterBrief is the roster text that goes at the top of a task prompt.
func (j *Job) rosterBrief(id string) string {
	if s := j.RosterFor(id, false); s != "" {
		return s + "\n"
	}
	return ""
}

// Planner is the planner's agent id, for code outside the package.
const Planner = planner

// IsBuilder says whether an agent id is one of the job's builders: the main
// builder or an experiment's.
func IsBuilder(id string) bool { return roleOf(id) == "builder" }

// maxReadsListed caps how many paths go into a builder's roster.
const maxReadsListed = 30

// ReadPath returns the path an agent.tool event read, searched or listed
// (the read, grep and find tools), cleaned. It is the one filter both the
// roster and the redo_reads metric use.
func ReadPath(e events.Event) (string, bool) {
	if e.Type != "agent.tool" {
		return "", false
	}
	tool, _ := e.Data["tool"].(string)
	args, _ := e.Data["args"].(map[string]any)
	path, _ := args["path"].(string)
	if path == "" || (tool != "read" && tool != "grep" && tool != "find") {
		return "", false
	}
	return filepath.Clean(path), true
}

// plannerReads lists, sorted and without repeats, the paths the planner
// read, grepped or found in this job, so a builder doesn't start by doing the
// same exploration. It reads the job's events, which a replay puts back.
func (j *Job) plannerReads() (paths []string, more int) {
	evs, _ := j.bus.Replay(j.ID)
	seen := map[string]bool{}
	for _, e := range evs {
		if p, ok := ReadPath(e); ok && e.Agent == planner && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	if len(paths) > maxReadsListed {
		more = len(paths) - maxReadsListed
		paths = paths[:maxReadsListed]
	}
	return paths, more
}

// Builders lists every builder id: the main builder and the experiment builders.
func Builders() []string { return append([]string(nil), experimentBuilders...) }
