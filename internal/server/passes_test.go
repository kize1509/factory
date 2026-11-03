package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"

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
