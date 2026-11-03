package job

import (
	"fmt"
	"strings"
)

// Every message between agents has the same envelope: who sent it, who it is
// for, and which job and ticket it belongs to. Nothing goes out anonymous or
// without a receiver. The engine logs each one as an agent.message event,
// which is what the map and the Who-talks-to-whom graph read.

// The kinds of message agents send each other. docs/AGENTS.md says who may
// send which kind to whom.
const (
	KindQuestion = "question" // ask the agent that produced your input
	KindAnswer   = "answer"   // the reply to a question
	KindHandover = "handover" // your finished work, for the next step
	KindReport   = "report"   // what you start and finish, to operations
	KindBlocker  = "blocker"  // you can't go on without the person
)

// Message is one thing one agent says to another.
type Message struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Job     string   `json:"job"`
	Ticket  string   `json:"ticket"`
	Text    string   `json:"text"`
	Sources []string `json:"sources,omitempty"`  // links to what the text is about: notes, files, diffs
	ReplyTo string   `json:"reply_to,omitempty"` // the question an answer belongs to
}

// operations is the default contact for an agent that doesn't know who to
// ask. you is the person running the factory.
const (
	operations = "operations"
	you        = "you"
)

// send checks a message and logs it. It fills in the job and ticket, and
// returns the message with its ID. A message without a sender or a receiver,
// or one that breaks the rules for its kind, is refused and not logged.
func (j *Job) send(m Message) (Message, error) {
	m.Job = j.ID
	m.Ticket = j.Issue
	if m.Ticket == "" {
		m.Ticket = j.ID
	}
	m.From, m.To, m.Text = strings.TrimSpace(m.From), strings.TrimSpace(m.To), strings.TrimSpace(m.Text)
	switch {
	case m.From == "":
		return m, fmt.Errorf("a message needs a sender")
	case m.To == "":
		return m, fmt.Errorf("a message needs a receiver")
	case m.Job == "" || m.Ticket == "":
		return m, fmt.Errorf("a message needs a job and a ticket")
	case m.From == m.To:
		return m, fmt.Errorf("%s can't send a message to itself", m.From)
	}
	switch m.Kind {
	case KindQuestion, KindAnswer:
		if m.To == you {
			return m, fmt.Errorf("%s goes to an agent; ask the person with ask_human", m.Kind)
		}
	case KindHandover:
		if m.To == you {
			return m, fmt.Errorf("a handover goes to the next step, not to the person")
		}
	case KindReport:
		if m.To != operations {
			return m, fmt.Errorf("a report goes to %s", operations)
		}
	case KindBlocker:
		if m.To != you {
			return m, fmt.Errorf("a blocker goes to the person running the factory")
		}
	default:
		return m, fmt.Errorf("unknown message kind %q", m.Kind)
	}
	j.mu.Lock()
	j.msgSeq++
	m.ID = fmt.Sprintf("M%d", j.msgSeq)
	j.mu.Unlock()
	j.emit(m.From, "agent.message", map[string]any{"id": m.ID, "kind": m.Kind, "from": m.From, "to": m.To, "job": m.Job,
		"ticket": m.Ticket, "text": m.Text, "sources": m.Sources, "reply_to": m.ReplyTo})
	return m, nil
}
