package job

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"factory/internal/runtime"
)

// ask_agent: an agent asks another agent of the same job a question and gets
// the answer in the same tool call. The engine puts the question into the
// other agent's session as a turn, the way AskBuilder does for the reviewer.

const maxAgentAsks = 3 // questions one agent may ask per turn

// errBusy is what an agent gets when it asks someone who is in the middle of
// a turn of their own. Two agents waiting on each other would never finish.
func errBusy(id string) error {
	return fmt.Errorf("%s is busy with a turn of their own and can't answer now. Carry on with what you know, or ask %s", agentName(id), operations)
}

// AskAgent passes a question to another agent and returns its answer. to may
// be "operations", the default contact when you don't know who to ask.
func (j *Job) AskAgent(from, to, question string, sources []string) (string, error) {
	j.tap(from, "ask_agent", map[string]any{"to": to, "question": question, "sources": sources})
	to, question = strings.ToLower(strings.TrimSpace(to)), strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("ask a question")
	}
	if to == "" {
		return "", fmt.Errorf("say who you ask: an agent's name, or %q when you don't know who", operations)
	}
	if from == grader {
		return "", fmt.Errorf("the grader judges alone and blind; record your reasoning in the grade")
	}
	if to == you {
		return "", fmt.Errorf("the person running the factory is asked with ask_human, which ends your turn")
	}
	if to == grader {
		return "", fmt.Errorf("the grader grades blind and takes no questions")
	}

	j.mu.Lock()
	ctx := j.ctx
	via := ""
	if to == operations {
		target, err := j.routeLocked(from)
		if err != nil {
			j.mu.Unlock()
			return "", err
		}
		if target != operations {
			via = ", forwarded to you because " + operations + " isn't running"
		}
		to = target
	}
	switch {
	case to == from:
		j.mu.Unlock()
		return "", fmt.Errorf("you can't ask yourself")
	case j.inExperiment[from] && j.inExperiment[to]:
		j.mu.Unlock()
		return "", fmt.Errorf("experiments don't talk to each other: each builder tries its own approach")
	case j.askTurn[from] >= maxAgentAsks:
		j.mu.Unlock()
		return "", fmt.Errorf("you have asked %d questions this turn; decide with what you know, or ask_human", maxAgentAsks)
	}
	target := j.agents[to]
	if target == nil {
		j.mu.Unlock()
		return "", j.gone(to)
	}
	if j.busy[to] > 0 {
		j.mu.Unlock()
		return "", errBusy(to)
	}
	j.askTurn[from]++
	j.mu.Unlock()

	me := j.roleName(from)
	prompt := fmt.Sprintf("%s, the %s, asks you a question about job %s (ticket %s)%s:\n\n%s\n", agentName(from), me, j.ID, j.ticket(), via, question)
	if len(sources) > 0 {
		prompt += "\nThey point at: " + strings.Join(sources, ", ") + "\n"
	}
	prompt += "\nAnswer plainly and briefly, with file paths where it helps, and give the reason behind your decisions. Don't change any code now, and don't ask them back: answer from what you know."
	return j.relay(ctx, from, to, question, sources, prompt)
}

// relay sends a question to an agent's session and returns the answer. Both
// directions go out as messages.
func (j *Job) relay(ctx context.Context, from, to, question string, sources []string, prompt string) (string, error) {
	j.mu.Lock()
	target := j.agents[to]
	j.mu.Unlock()
	if target == nil {
		return "", j.gone(to)
	}
	q, err := j.send(Message{Kind: KindQuestion, From: from, To: to, Text: question, Sources: sources})
	if err != nil {
		return "", err
	}
	answer, err := j.ask(ctx, target, prompt)
	if err != nil {
		return "", err
	}
	if _, err := j.send(Message{Kind: KindAnswer, From: to, To: from, Text: answer, ReplyTo: q.ID}); err != nil {
		return "", err
	}
	return answer, nil
}

// routeLocked picks who gets a question for operations when nobody with that
// role is in the job: the producer of the caller's input that is alive and
// idle. Operations (HF-9) only has to join the roster to take this over.
func (j *Job) routeLocked(from string) (string, error) {
	if j.roster[operations] != nil && j.agents[operations] != nil {
		return operations, nil
	}
	reads, _ := j.flowLocked(from)
	for _, l := range reads {
		if isAgent(l.Agent) && l.Agent != from && j.agents[l.Agent] != nil && j.busy[l.Agent] == 0 && !(j.inExperiment[from] && j.inExperiment[l.Agent]) {
			return l.Agent, nil
		}
	}
	return "", fmt.Errorf("nobody is available to forward this to: ask_human if you need the person, otherwise carry on")
}

// gone is the error for an agent that isn't running. When it left a handoff
// note (after a restart its session is gone), the note is still there to read.
func (j *Job) gone(id string) error {
	if note := filepath.Join(j.Dir(), "handoff", id+".json"); fileExists(note) {
		return fmt.Errorf("%s is not running any more. Their saved handoff note is at %s. Read it, then ask %s or ask_human", agentName(id), note, operations)
	}
	return fmt.Errorf("%s is not in this job right now. Call the roster tool to see who is, or ask %s", agentName(id), operations)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (j *Job) ticket() string {
	if j.Issue != "" {
		return j.Issue
	}
	return j.ID
}

func (j *Job) roleName(id string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if m := j.roster[id]; m != nil {
		return m.Role
	}
	return roleOf(id)
}

// enter marks an agent busy for one turn. It fails when the agent already is.
func (j *Job) enter(a runtime.Agent) (string, error) {
	j.mu.Lock()
	id := j.idOfLocked(a)
	if id == "" {
		j.mu.Unlock()
		return "", nil
	}
	if j.busy[id] > 0 {
		j.mu.Unlock()
		return id, errBusy(id)
	}
	j.busy[id]++
	j.askTurn[id] = 0
	member := j.roster[id] != nil
	if member {
		j.roster[id].State = StateWorking
	}
	step := phaseName(j.State)
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.publish(snap)
	if member {
		j.send(Message{Kind: KindReport, From: id, To: operations, Text: "Started: " + step})
	}
	return id, nil
}

// exit ends the turn: blocked while a request to the person is open, done
// once the agent has a handoff note, waiting otherwise.
func (j *Job) exit(id string) {
	if id == "" {
		return
	}
	j.mu.Lock()
	if j.busy[id] > 0 {
		j.busy[id]--
	}
	doing, member := phaseName(j.State), false
	if m := j.roster[id]; m != nil {
		member = true
		if m.Doing != "" {
			doing = m.Doing
		}
		m.Doing = ""
		switch {
		case j.asks[id] != nil:
			m.State = StateBlocked
		case j.handoffs[id] != nil:
			m.State = StateDone
		default:
			m.State = StateWaiting
		}
	}
	snap := j.snapshotLocked()
	j.mu.Unlock()
	j.publish(snap)
	if member {
		j.send(Message{Kind: KindReport, From: id, To: operations, Text: "Finished: " + doing})
	}
}

// idOfLocked finds which agent of the job a session belongs to.
func (j *Job) idOfLocked(a runtime.Agent) string {
	for id, b := range j.agents {
		if b == a {
			return id
		}
	}
	return ""
}
