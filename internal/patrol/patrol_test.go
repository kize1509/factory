package patrol

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"factory/internal/harness"
	"factory/internal/harness/fixture"
	"factory/internal/store"
)

// honest is an agent that does what the prompt says: run the check, then
// record it with the verdict its result suggests.
func honest(ctx context.Context, t *Task) (float64, error) {
	r, err := t.Check(ctx)
	if err != nil {
		return 0, err
	}
	v := OK
	if !r.Pass {
		v = Finding
	}
	return 0.01, t.Record(r.Run, v, "ran "+r.Run)
}

func newPass(t *testing.T, a Agent, b Budget) (*Pass, string) {
	repo := fixture.New(t, "seeded")
	db, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Pass{ID: "P1", Project: "seeded", Repo: repo, Root: t.TempDir(), Agent: a, Store: db, Budget: b,
		Features: []Feature{{ID: "cart.total"}, {ID: "cart.count"}}}, repo
}

func TestPassLeavesRunFolderPerCell(t *testing.T) {
	p, _ := newPass(t, AgentFunc(honest), Budget{Agents: 2})
	sum, err := p.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sum.State != Done || sum.Checks != 4 || sum.Findings != 2 || sum.OK != 2 || sum.CostUSD < 0.039 {
		t.Fatalf("%+v", sum)
	}
	rows, _ := p.Ledger()
	if len(rows) != 4 {
		t.Fatalf("%+v", rows)
	}
	for _, r := range rows {
		res, err := harness.Results(p.Root, r.Run)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(r.RunDir, "result.json")); err != nil || res.ReplayCmd == "" || r.Commit != res.Commit {
			t.Fatalf("%+v %+v", r, res)
		}
		if (r.Technique == "tests") != (r.Verdict == Finding) {
			t.Fatalf("verdict %+v", r)
		}
		if r.WallMS <= 0 || r.CostUSD < 0.01 {
			t.Fatalf("metrics not joined: %+v", r)
		}
	}
}

func TestRecordRejectsWrongVerdictAndMissingRecord(t *testing.T) {
	lazy := AgentFunc(func(ctx context.Context, t *Task) (float64, error) {
		r, err := t.Check(ctx)
		if err != nil {
			return 0, err
		}
		if !r.Pass {
			if t.Record(r.Run, OK, "") == nil {
				panic("a failed run recorded ok")
			}
		}
		return 0, nil // never records
	})
	p, _ := newPass(t, lazy, Budget{})
	sum, _ := p.Run(context.Background())
	if sum.Blocked != 4 || sum.OK != 0 {
		t.Fatalf("%+v", sum)
	}
}

func TestBudgetStopsHandingOutWork(t *testing.T) {
	p, _ := newPass(t, AgentFunc(honest), Budget{USD: 0.015, Agents: 1})
	sum, _ := p.Run(context.Background())
	if sum.State != OverRun || sum.Checks != 2 || len(sum.Next) != 2 { // 0.01 each: the third is refused
		t.Fatalf("%+v", sum)
	}
	p2, _ := newPass(t, AgentFunc(honest), Budget{Checks: 1})
	if s, _ := p2.Run(context.Background()); s.Checks != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestStopEndsPassWithinSeconds(t *testing.T) {
	started := make(chan struct{}, 4)
	slow := AgentFunc(func(ctx context.Context, tk *Task) (float64, error) {
		started <- struct{}{}
		<-ctx.Done()
		_, err := tk.Check(ctx) // refused after the stop
		return 0, err
	})
	p, _ := newPass(t, slow, Budget{Agents: 2})
	done := make(chan *Summary)
	go func() { s, _ := p.Run(context.Background()); done <- s }()
	<-started
	at := time.Now()
	p.Stop()
	select {
	case s := <-done:
		if s.State != Stopped || time.Since(at) > 3*time.Second || len(s.Next) == 0 {
			t.Fatalf("%+v after %s", s, time.Since(at))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pass did not stop")
	}
}

func TestMapFeatures(t *testing.T) {
	fs := MapFeatures(fixture.New(t, "seeded"))
	if len(fs) != 1 || fs[0].ID != "root" {
		t.Fatalf("%+v", fs)
	}
}
