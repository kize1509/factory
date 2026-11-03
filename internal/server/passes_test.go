package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"

	"factory/internal/events"
	"factory/internal/harness/fixture"
	"factory/internal/patrol"
	"factory/internal/store"
)

func TestLedgerAPIAndStopAll(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := &patrol.Pass{ID: "P9", Repo: fixture.New(t, "seeded"), Root: t.TempDir(), Store: db, Features: []patrol.Feature{{ID: "cart"}}, Techniques: []string{"tests"},
		Agent: patrol.AgentFunc(func(ctx context.Context, tk *patrol.Task) (float64, error) {
			r, err := tk.Check(ctx)
			if err != nil {
				return 0, err
			}
			return 0, tk.Record(r.Run, patrol.Finding, "seeded bug")
		})}
	if _, err := p.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db, passes: map[string]*patrol.Pass{"P9": p}}
	h := s.Handler()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/passes/P9/ledger", nil))
	var out struct{ Rows []store.LedgerRow }
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || len(out.Rows) != 1 || out.Rows[0].Verdict != "finding" || out.Rows[0].RunDir == "" {
		t.Fatalf("%v %s", err, w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/api/passes/stop-all", nil))
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/passes/P9", nil))
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}

func TestOpsDraftRoutes(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := &Server{Store: db, Bus: events.NewBus(t.TempDir())}
	h := s.Handler()
	id, _ := db.AddDraft(store.Draft{Repo: "/r", Job: "J", Agent: "hermes", Action: "comment", Args: []byte(`{"kind":"comment","issue":"HF-1","text":"x"}`), Summary: "Comment on HF-1: x"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/ops/drafts?state=pending", nil))
	var out struct{ Drafts []store.Draft }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Drafts) != 1 || out.Drafts[0].ID != id {
		t.Fatalf("%s", rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/ops/drafts/"+strconv.FormatInt(id, 10)+"/reject", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/api/ops/drafts/"+strconv.FormatInt(id, 10)+"/reject", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("a second reject should conflict, got %d", rec.Code)
	}
}
