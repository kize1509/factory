package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"factory/internal/job"
	"factory/internal/ops"
)

// The operations agent (Hermes): the tools every agent has to reach it, the
// review queue for its drafts, and the wiring of its service.

// opsToolNames are the tools that belong to the operations service.
var opsToolNames = map[string]bool{"ask_ops": true, "report": true, "tracker_search": true, "tracker_read": true, "knowledge_search": true,
	"tracker_create": true, "tracker_update": true, "tracker_move": true, "tracker_comment": true, "tracker_link": true, "tracker_attach": true,
	"answer": true, "handover": true}

// opsTool runs a tool through the operations service; handled is false for
// any other tool.
func (s *Server) opsTool(scope, agent, tool string, args []byte) (string, bool, error) {
	if !opsToolNames[tool] {
		return "", false, nil
	}
	if s.Ops == nil {
		return "", true, errNoOps
	}
	text, err := s.Ops.Tool(scope, agent, tool, args)
	return text, true, err
}

var errNoOps = &opsErr{"the operations agent is not running"}

type opsErr struct{ msg string }

func (e *opsErr) Error() string { return e.msg }

// startOps builds the operations service and starts it watching events.
func (s *Server) startOps() {
	if s.Ops != nil || s.Store == nil || s.Bus == nil {
		return
	}
	s.Ops = &ops.Service{Store: s.Store, Bus: s.Bus, Runtimes: s.Runtimes, PollEvery: 60 * time.Second, Steer: s.steerActive}
	go s.Ops.Run(context.Background())
}

// steerActive gives a running job's active agent a message.
func (s *Server) steerActive(scope, text string) bool {
	s.mu.Lock()
	j := s.jobs[scope]
	s.mu.Unlock()
	if j == nil {
		return false
	}
	return steer(j, text)
}

func steer(j *job.Job, text string) bool {
	a := j.Active()
	return a != nil && a.Steer(text) == nil
}

func (s *Server) opsRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/ops/drafts", s.opsDrafts)
	mux.HandleFunc("POST /api/ops/drafts/{id}/approve", s.opsApprove)
	mux.HandleFunc("POST /api/ops/drafts/{id}/reject", s.opsReject)
	mux.HandleFunc("GET /api/ops/notes", s.opsNotes)
}

func (s *Server) opsDrafts(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	list, err := s.Store.Drafts(state)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"drafts": list})
}

func (s *Server) opsApprove(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Ops == nil {
		http.Error(w, "no such draft", http.StatusBadRequest)
		return
	}
	issue, err := s.Ops.Approve(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "issue": issue})
}

func (s *Server) opsReject(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || s.Ops == nil {
		http.Error(w, "no such draft", http.StatusBadRequest)
		return
	}
	if err := s.Ops.Reject(id); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) opsNotes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	notes, err := s.Store.Notes(q.Get("repo"), q.Get("job"), q.Get("issue"), strings.Fields(q.Get("q")), 50)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"notes": notes})
}
