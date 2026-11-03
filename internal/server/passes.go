package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"factory/internal/config"
	"factory/internal/patrol"
)

// Patrol passes: start one, read its ledger, stop it. The engine starts the
// agents and owns the budget and the kill switch (D2).

func (s *Server) passRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/passes", s.startPass)
	mux.HandleFunc("POST /api/passes/stop-all", s.stopAllPasses)
	mux.HandleFunc("GET /api/passes/{pass}", s.getPass)
	mux.HandleFunc("GET /api/passes/{pass}/ledger", s.passLedger)
	mux.HandleFunc("POST /api/passes/{pass}/stop", s.stopPass)
}

// passOf finds a pass from the "job" id a patrol agent's tool URL carries
// ("pass-<id>").
func (s *Server) passOf(job string) *patrol.Pass {
	id, ok := strings.CutPrefix(job, "pass-")
	if !ok {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.passes[id]
}

func passTool(p *patrol.Pass, agent, tool string, args []byte) (string, error) {
	if tool != "record_check" {
		if tool == "set_status" {
			return "ok", nil
		}
		return "", fmt.Errorf("patrol agents have no tool %q", tool)
	}
	var b struct{ Run, Verdict, Note string }
	if err := json.Unmarshal(args, &b); err != nil {
		return "", err
	}
	if err := p.RecordFor(agent, b.Run, b.Verdict, b.Note); err != nil {
		return "", err
	}
	return "Recorded.", nil
}

func (s *Server) startPass(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Repo    string   `json:"repo"`
		Runtime string   `json:"runtime"`
		USD     float64  `json:"usd"`
		Minutes float64  `json:"minutes"`
		Agents  int      `json:"agents"`
		Checks  int      `json:"checks"`
		Tech    []string `json:"techniques"`
	}
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil || b.Repo == "" {
		http.Error(w, "need a repo", http.StatusBadRequest)
		return
	}
	repo, err := patrol.CanonicalRepo(b.Repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b.Repo = repo
	if b.Runtime == "" {
		b.Runtime = "claude-code"
	}
	rt := s.Runtimes[b.Runtime]
	if rt == nil {
		http.Error(w, "no runtime "+b.Runtime, http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	if s.passes == nil {
		s.passes = map[string]*patrol.Pass{}
	}
	id := fmt.Sprintf("P%d", time.Now().UnixMilli())
	p := &patrol.Pass{ID: id, Project: filepath.Base(b.Repo), Repo: b.Repo, Techniques: b.Tech, Store: s.Store,
		Budget: patrol.Budget{USD: b.USD, Time: time.Duration(b.Minutes * float64(time.Minute)), Agents: b.Agents, Checks: b.Checks}}
	p.Agent = &patrol.RuntimeAgent{RT: rt, Model: s.Model, Cost: func(pass, agent string) float64 { return s.agentCost("pass-"+pass, agent) }}
	s.passes[id] = p
	s.mu.Unlock()
	s.Bus.Place("pass-"+id, filepath.Join(config.Home(), "passes", id))
	go func() {
		if _, err := p.Run(context.Background()); err != nil {
			s.Bus.Emit("pass-"+id, "", "pass.failed", map[string]any{"error": err.Error()})
		}
	}()
	writeJSON(w, map[string]any{"id": id})
}

// agentCost sums the usage events one agent has produced.
func (s *Server) agentCost(job, agent string) float64 {
	evs, _ := s.Bus.Replay(job)
	var usd float64
	for _, e := range evs {
		if e.Type == "usage" && e.Agent == agent {
			f, _ := e.Data["usd"].(float64)
			usd += f
		}
	}
	return usd
}

func (s *Server) pass(w http.ResponseWriter, id string) *patrol.Pass {
	s.mu.Lock()
	p := s.passes[id]
	s.mu.Unlock()
	if p == nil {
		http.Error(w, "no pass "+id, http.StatusNotFound)
	}
	return p
}

func (s *Server) getPass(w http.ResponseWriter, r *http.Request) {
	row, err := s.Store.Pass(r.PathValue("pass"))
	if err != nil {
		http.Error(w, "no pass "+r.PathValue("pass"), http.StatusNotFound)
		return
	}
	writeJSON(w, row)
}

func (s *Server) passLedger(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.Ledger(r.PathValue("pass"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"pass": r.PathValue("pass"), "rows": rows})
}

func (s *Server) stopPass(w http.ResponseWriter, r *http.Request) {
	if p := s.pass(w, r.PathValue("pass")); p != nil {
		p.Stop()
		writeJSON(w, map[string]any{"stopped": []string{p.ID}})
	}
}

func (s *Server) stopAllPasses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	var list []*patrol.Pass
	for _, p := range s.passes {
		if p.State() == patrol.Running {
			list = append(list, p)
		}
	}
	s.mu.Unlock()
	ids := []string{}
	for _, p := range list {
		p.Stop()
		ids = append(ids, p.ID)
	}
	writeJSON(w, map[string]any{"stopped": ids})
}
