package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"factory/internal/events"
	"factory/internal/job"
)

func metricsOf(t *testing.T, s *Server) metrics {
	t.Helper()
	w := httptest.NewRecorder()
	s.adminMetrics(w, httptest.NewRequest("GET", "/api/admin/metrics", nil))
	var m metrics
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err, w.Body)
	}
	return m
}

func metricsServer(t *testing.T, ids ...string) *Server {
	bus := events.NewBus(t.TempDir())
	s := &Server{Bus: bus, jobs: map[string]*job.Job{}}
	for _, id := range ids {
		j := job.New(job.Spec{ID: id, Repo: t.TempDir()}, bus, nil)
		bus.Place(id, j.Dir())
		s.jobs[id] = j
	}
	return s
}

// The graph draws from agent.message: its kinds, with reports to operations
// left out, and a handoff in the same job counted once.
func TestMetricsEdgesCountAgentMessages(t *testing.T) {
	s := metricsServer(t, "N1")
	msg := func(kind, from, to string) {
		s.Bus.Emit("N1", from, "agent.message", map[string]any{"kind": kind, "from": from, "to": to, "job": "N1", "ticket": "N1", "text": kind})
	}
	msg("question", "archimedes", "aristotle")
	msg("question", "archimedes", "aristotle")
	msg("answer", "aristotle", "archimedes")
	msg("handover", "aristotle", "archimedes")
	msg("blocker", "archimedes", "you")
	msg("report", "archimedes", "operations")
	s.Bus.Emit("N1", "aristotle", "handoff.written", map[string]any{"note": map[string]any{"to": "archimedes", "did": "planned", "next": "build"}})
	m := metricsOf(t, s)
	want := map[string]int{"archimedes>aristotle>question": 2, "aristotle>archimedes>answer": 1, "aristotle>archimedes>handover": 1, "archimedes>you>blocker": 1}
	if len(m.Edges) != len(want) {
		t.Fatalf("edges %v, want %v", m.Edges, want)
	}
	for k, n := range want {
		if m.Edges[k] != n {
			t.Errorf("edge %s = %d, want %d (all: %v)", k, m.Edges[k], n, m.Edges)
		}
	}
	if len(m.Messages) != 5 || m.Messages[0].Kind != "question" {
		t.Fatalf("messages %+v", m.Messages)
	}
}

// A job logged before agent.message existed still draws from its handoffs.
func TestMetricsOldLogFallsBackToHandoffs(t *testing.T) {
	s := metricsServer(t, "O1")
	s.Bus.Emit("O1", "aristotle", "handoff.written", map[string]any{"note": map[string]any{"to": "archimedes", "did": "planned", "next": "build"}})
	m := metricsOf(t, s)
	if m.Edges["aristotle>archimedes"] != 1 || len(m.Messages) != 1 || m.Messages[0].Kind != "handoff" {
		t.Fatalf("edges %v messages %+v", m.Edges, m.Messages)
	}
}
