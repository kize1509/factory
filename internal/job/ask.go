package job

import (
	"context"
	"fmt"
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
	Need    string   `json:"need"`              // what it needs from you, in one sentence
	Why     string   `json:"why,omitempty"`     // what it tried, where it's stuck
	Options []string `json:"options,omitempty"` // choices, when it's a decision
}

// Markdown is the request as you read it.
func (q Ask) Markdown() string {
	s := "**" + strings.TrimSpace(q.Need) + "**"
	if w := strings.TrimSpace(q.Why); w != "" {
		s += "\n\n" + w
	}
	if len(q.Options) > 0 {
		s += "\n\n- " + strings.Join(q.Options, "\n- ")
	}
	return s
}

// AskHuman records an agent's request for help. The job waits for your
// answer as soon as the agent's turn ends.
func (j *Job) AskHuman(agent string, q Ask) error {
	q.Need = strings.TrimSpace(q.Need)
	if q.Need == "" {
		return fmt.Errorf("say what you need from the person in need, in one sentence")
	}
	j.mu.Lock()
	if j.asks == nil {
		j.asks = map[string]*Ask{}
	}
	j.asks[agent] = &q
	if m := j.roster[agent]; m != nil {
		m.State = StateBlocked
	}
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.publish(snap)
	j.send(Message{Kind: KindBlocker, From: agent, To: you, Text: strings.TrimSpace(q.Need + "\n" + q.Why)})
	j.emit(agent, "agent.asked", map[string]any{"need": q.Need, "why": q.Why, "options": q.Options, "question": q.Markdown()})
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
func (j *Job) plea(ctx context.Context, err error) string {
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
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	out, e := a.Prompt(ctx, "The engine can't get this job further on its own:\n\n"+why+"\n\n"+
		"The person running the factory reads your next reply first, so write it to them. At most three short sentences. "+
		"Start with what you need from them (\"I need …\") or what you're stuck on (\"I'm stuck on …: should I … or …?\"), then say why in one sentence. "+
		"No recap of the task, no list of what you did, no code blocks, no tools.")
	out = strings.TrimSpace(out)
	if e != nil || out == "" {
		return ""
	}
	if r := []rune(out); len(r) > 700 {
		out = string(r[:699]) + "…"
	}
	return out
}
