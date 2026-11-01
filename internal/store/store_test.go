package store

import (
	"path/filepath"
	"testing"
	"time"

	"factory/internal/events"
)

func TestJobsEventsPrices(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SaveJob("J1", "ABC-1", "fix it", "/repo", map[string]any{"id": "J1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.AddEvent(events.Event{Job: "J1", Type: "workspace.ready", Time: time.Now(), Data: map[string]any{"branch": "factory/J1"}})
	s.AddEvent(events.Event{Job: "J1", Agent: "archimedes", Type: "usage", Time: time.Now(), Data: map[string]any{"usd": 0.5}})
	s.AddEvent(events.Event{Job: "J1", Type: "job.state", Time: time.Now(), Data: map[string]any{"state": "done"}})
	jobs, _ := s.Jobs()
	if len(jobs) != 1 || jobs[0].State != "done" || jobs[0].Branch != "factory/J1" {
		t.Fatalf("%+v", jobs)
	}
	evs, _ := s.Events("J1")
	if len(evs) != 3 || evs[1].Data["usd"].(float64) != 0.5 || evs[1].Agent != "archimedes" {
		t.Fatalf("%+v", evs)
	}
	if n, _ := s.RecomputeUsage(func(string, float64, float64, float64, float64) float64 { return 0.25 }); n != 1 {
		t.Fatalf("recomputed %d", n)
	}
	if evs, _ := s.Events("J1"); evs[1].Data["usd"].(float64) != 0.25 {
		t.Fatal("usd not recomputed")
	}
	s.SavePrices([]Price{{Model: "anthropic/x", Input: 1, Output: 2, Source: "manual"}}, false)
	s.SavePrices([]Price{{Model: "anthropic/x", Input: 9, Output: 9, Source: "models.dev"}}, true) // keeps the manual price
	ps, _ := s.Prices()
	if len(ps) != 1 || ps[0].Input != 1 {
		t.Fatalf("%+v", ps)
	}
}
