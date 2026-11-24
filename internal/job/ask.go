package job

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"factory/internal/runtime"
)

// Agents ask you for help the moment they need something only you can give:
// a credential, access, a decision the ticket doesn't make. The ask_human
// tool records the request and the agent ends its turn; the job waits for
// your answer, which becomes the agent's next message. Nothing blocks a tool
// call, so no runtime's tool timeout gets in the way.

// Ask is an agent's request to you.
type Ask struct {
	Need      string   `json:"need"`                // the question or request, in one plain sentence
	Why       string   `json:"why,omitempty"`       // the context: what it found, why it matters
	Recommend string   `json:"recommend,omitempty"` // what it would do, so you can just say yes
	Options   []string `json:"options,omitempty"`   // choices, when it's a decision
	Context   string   `json:"context,omitempty"`   // who asks and on which ticket; set by the engine
}

// Markdown is the request as you read it.
func (q Ask) Markdown() string {
	var s string
	if c := strings.TrimSpace(q.Context); c != "" {
		s = c + "\n\n"
	}
	s += "**" + strings.TrimSpace(q.Need) + "**"
	if w := strings.TrimSpace(q.Why); w != "" {
		s += "\n\n" + w
	}
	if r := strings.TrimSpace(q.Recommend); r != "" {
		s += "\n\nWhat I'd do: " + r
	}
	if len(q.Options) > 0 {
		s += "\n\n- " + strings.Join(q.Options, "\n- ")
	}
	return s
}

// A request for permission to change existing tests, or to rebase or
// merge, is one the engine answers itself. It takes a permission word, an
// edit and tests together, so "I need the Stripe test key" still goes
// through.
var (
	askPermission = regexp.MustCompile(`(?i)(permission|\bok\b|okay|approv|allow|sign.?off|may i|can i)`)
	askEdit       = regexp.MustCompile(`(?i)\b(edit|chang|modif|updat|touch|rewrit|fix)\w*`)
	askTests      = regexp.MustCompile(`(?i)(\btests?/|_test\.|\.test\.|\.spec\.|test_\w+\.py|\b(existing|the|these|those|three|two|my|its) (\w+ )?tests?\b|\btest files?\b)`)
	askGit        = regexp.MustCompile(`(?i)(permission|\bok\b|okay|approv|allow|may i|can i).{0,60}\b(rebase|force.?push|cherry.?pick|merge (main|origin))`)
)

func asksAboutTests(s string) bool {
	return askPermission.MatchString(s) && askEdit.MatchString(s) && askTests.MatchString(s)
}

// AskHuman records an agent's request for help. The job waits for your
// answer as soon as the agent's turn ends.
func (j *Job) AskHuman(agent string, q Ask) error {
	q.Need = strings.TrimSpace(q.Need)
	if q.Need == "" {
		return fmt.Errorf("say what you need from the person, in one sentence")
	}
	if len([]rune(q.Need)) > 300 {
		return fmt.Errorf("need is one plain sentence, under 300 characters, that they can answer in one reply; put the context in why")
	}
	all := q.Need + " " + q.Why
	if !j.TestsAllowed && j.hasReviewer() && asksAboutTests(all) {
		return fmt.Errorf("you don't need permission to change existing tests in this job: change the ones this ticket makes out of date, say why in your handoff, and the reviewer checks each edit. Carry on without asking")
	}
	if j.TestsAllowed && asksAboutTests(all) {
		return fmt.Errorf("this job already allows changes to existing tests. Carry on without asking")
	}
	if askGit.MatchString(all) {
		return fmt.Errorf("don't ask about git: the engine brings your work up to date with origin's default branch, commits and ships it. Carry on with the code")
	}
	name := agent
	if name != "" {
		name = strings.ToUpper(name[:1]) + name[1:]
	}
	q.Context = fmt.Sprintf("%s, working on %s (%s), asks:", name, j.ID, j.Title)
	j.mu.Lock()
	if j.asks == nil {
		j.asks = map[string]*Ask{}
	}
	j.asks[agent] = &q
	j.mu.Unlock()
	j.emit(agent, "agent.asked", map[string]any{"need": q.Need, "why": q.Why, "recommend": q.Recommend, "options": q.Options, "question": q.Markdown()})
	return nil
}

// takeAsk returns and clears the request the agent behind a made, if any.
func (j *Job) takeAsk(a runtime.Agent) (string, *Ask) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for id, b := range j.agents {
		if b == a {
			if q := j.asks[id]; q != nil {
				delete(j.asks, id)
				return id, q
			}
		}
	}
	return "", nil
}

// waitForYou holds the job until you answer the agent's request.
func (j *Job) waitForYou(ctx context.Context, agent string, q *Ask) (string, error) {
	j.mu.Lock()
	prev := j.State
	j.Question = q.Markdown()
	j.mu.Unlock()
	j.set("waiting")
	var answer string
	select {
	case answer = <-j.answers:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	j.mu.Lock()
	j.Question = ""
	j.mu.Unlock()
	j.emit(agent, "human.answered", map[string]any{"answer": answer})
	j.set(prev)
	return answer, nil
}

// plea asks the lead agent, while it's still there, to tell you in a few
// words what it needs from you to get the stuck job going again.
func (j *Job) plea(ctx context.Context, err error, phase string, repeats int) string {
	j.mu.Lock()
	lead := j.lead
	if lead == "" {
		lead = builder
	}
	a := j.agents[lead]
	j.mu.Unlock()
	if a == nil {
		return ""
	}
	why := err.Error()
	if len(why) > 1500 {
		why = why[:1500] + "…"
	}
	var extra string
	if repeats > 0 {
		last := ""
		j.mu.Lock()
		if n := len(j.guidance); n > 0 {
			last = j.guidance[n-1]
		}
		j.mu.Unlock()
		extra += fmt.Sprintf("This is the same failure %d times in a row, so the last attempt (%q) didn't help. Say so plainly and suggest something different, not the same thing again. ", repeats+1, last)
	}
	if acts := j.actions(phase, repeats); len(acts) > 0 {
		extra += "Besides writing to them, they can pick one of these ways out: " + optionsLine(acts) + ". If one fits, name it. "
	}
	if shipPhase(phase) {
		extra += "Don't ask for permission to run git commands: whatever they write goes to an agent with git in the job's worktree, so ask for a decision instead. "
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, e := a.Prompt(ctx, "The engine can't get this job further on its own:\n\n"+why+"\n\n"+
		"The person running the factory reads your next reply first. They haven't been watching this job, so write it the way you'd message a teammate, in at most three short sentences. "+
		"First say what happened in plain words (the feature or file, not engine terms like worktree, handoff, oracle or phase, and no commit hashes). "+
		"Then the one thing you need from them, as a question they can answer in one reply. Then what you'd do and why, so they can just say yes. "+
		extra+"No recap of the task, no list of what you did, no code blocks, no tools.")
	out = strings.TrimSpace(out)
	if e != nil || out == "" {
		return ""
	}
	if r := []rune(out); len(r) > 700 {
		out = string(r[:699]) + "…"
	}
	return out
}
