package harness

import (
	"testing"
	"time"

	"factory/internal/events"
	"factory/internal/job"
)

// asks counts the questions agents sent each other; redo_reads the builder
// reads of paths the planner had already read.
func TestMetricsCountAsksAndRedoneReads(t *testing.T) {
	tool := func(agent, name, path string) events.Event {
		return events.Event{Type: "agent.tool", Agent: agent, Data: map[string]any{"tool": name, "args": map[string]any{"path": path}}}
	}
	evs := []events.Event{
		tool("aristotle", "read", "internal/a.go"),
		tool("aristotle", "grep", "internal"),
		tool("archimedes", "read", "internal/a.go"),   // redone
		tool("archimedes", "read", "./internal/a.go"), // redone, same path spelled differently
		tool("archimedes", "grep", "internal"),        // redone
		tool("archimedes", "read", "internal/b.go"),   // new
		tool("archimedes", "edit", "internal/a.go"),   // not a read
		tool("socrates", "read", "internal/a.go"),     // not a builder
		{Type: "agent.message", Data: map[string]any{"kind": "question"}},
		{Type: "agent.message", Data: map[string]any{"kind": "answer"}},
	}
	m := Metrics(evs, time.Second)
	if m["asks"] != 1 || m["redo_reads"] != 3 {
		t.Fatalf("asks %v redo_reads %v", m["asks"], m["redo_reads"])
	}
	// a builder reading before the planner did isn't redoing anything
	m = Metrics([]events.Event{tool("archimedes", "read", "x.go"), tool("aristotle", "read", "x.go")}, time.Second)
	if m["redo_reads"] != 0 {
		t.Fatalf("redo_reads %v, want 0", m["redo_reads"])
	}
}

// The metric takes its names from the job package, so every builder the job
// can start counts, and a renamed planner doesn't drop the number to zero.
func TestMetricsUseTheJobsOwnNames(t *testing.T) {
	read := func(agent string) events.Event {
		return events.Event{Type: "agent.tool", Agent: agent, Data: map[string]any{"tool": "read", "args": map[string]any{"path": "a.go"}}}
	}
	for _, b := range job.Builders() {
		m := Metrics([]events.Event{read(job.Planner), read(b)}, time.Second)
		if m["redo_reads"] != 1 {
			t.Errorf("builder %s: redo_reads %v, want 1", b, m["redo_reads"])
		}
	}
	if len(job.Builders()) == 0 || !job.IsBuilder(job.Builders()[0]) || job.IsBuilder(job.Planner) {
		t.Fatal("IsBuilder and Builders disagree")
	}
}
