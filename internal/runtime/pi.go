package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"factory/internal/events"
)

// Pi runs agents in one long-lived pi-host sidecar process.
type Pi struct {
	HostDir  string // folder with pi-host/index.mjs
	AgentDir string // Pi's own config folder for factory agents
	Engine   string // engine base URL the sidecar calls back to
	Bus      *events.Bus

	once    sync.Once
	startEr error
	stdin   io.WriteCloser
	nextID  atomic.Int64
	mu      sync.Mutex
	waiting map[int64]chan reply
	jobOf   sync.Map // agent -> job, to tag events
}

type reply struct {
	ID    int64          `json:"id"`
	OK    bool           `json:"ok"`
	Error string         `json:"error"`
	Text  string         `json:"text"`
	Event string         `json:"event"`
	Agent string         `json:"agent"`
	Data  map[string]any `json:"data"`
}

func (p *Pi) boot() error {
	p.once.Do(func() {
		cmd := exec.Command("node", "index.mjs")
		cmd.Dir = p.HostDir
		cmd.Stderr = os.Stderr
		in, err := cmd.StdinPipe()
		if err != nil {
			p.startEr = err
			return
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			p.startEr = err
			return
		}
		if err := cmd.Start(); err != nil {
			p.startEr = fmt.Errorf("start pi-host: %w", err)
			return
		}
		p.stdin, p.waiting = in, map[int64]chan reply{}
		p.Bus.Emit("", "", "pi.host.started", map[string]any{"pid": cmd.Process.Pid})
		go p.read(out)
	})
	return p.startEr
}

func (p *Pi) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var r reply
		if json.Unmarshal(sc.Bytes(), &r) != nil {
			continue
		}
		if r.Event != "" {
			job, _ := p.jobOf.Load(r.Agent)
			j, _ := job.(string)
			p.Bus.Emit(j, r.Agent, r.Event, r.Data)
			continue
		}
		p.mu.Lock()
		ch := p.waiting[r.ID]
		delete(p.waiting, r.ID)
		p.mu.Unlock()
		if ch != nil {
			ch <- r
		}
	}
	// the sidecar died: fail everything still waiting
	p.mu.Lock()
	for id, ch := range p.waiting {
		ch <- reply{ID: id, Error: "pi-host exited"}
	}
	p.waiting = map[int64]chan reply{}
	p.mu.Unlock()
}

func (p *Pi) call(ctx context.Context, req map[string]any) (reply, error) {
	if err := p.boot(); err != nil {
		return reply{}, err
	}
	id := p.nextID.Add(1)
	req["id"] = id
	ch := make(chan reply, 1)
	p.mu.Lock()
	p.waiting[id] = ch
	line, _ := json.Marshal(req)
	_, err := p.stdin.Write(append(line, '\n'))
	p.mu.Unlock()
	if err != nil {
		return reply{}, err
	}
	select {
	case r := <-ch:
		if !r.OK {
			return r, errors.New(r.Error)
		}
		return r, nil
	case <-ctx.Done():
		return reply{}, ctx.Err()
	}
}

func (p *Pi) Start(ctx context.Context, s AgentSpec) (Agent, error) {
	p.jobOf.Store(s.Agent, s.Job)
	_, err := p.call(ctx, map[string]any{
		"op": "start", "agent": s.Agent, "job": s.Job, "cwd": s.Cwd, "model": s.Model,
		"system": s.System, "tools": s.Tools, "thinking": s.Thinking,
		"engine": p.Engine, "agentDir": p.AgentDir, "skills": s.Skills, "role": s.Role,
	})
	if err != nil {
		return nil, err
	}
	p.Bus.Emit(s.Job, s.Agent, "agent.started", map[string]any{"runtime": "pi", "model": s.Model})
	return &piAgent{p: p, id: s.Agent}, nil
}

type piAgent struct {
	p  *Pi
	id string
}

func (a *piAgent) Prompt(ctx context.Context, text string) (string, error) {
	r, err := a.p.call(ctx, map[string]any{"op": "prompt", "agent": a.id, "text": text})
	if err == nil && r.Error != "" {
		err = errors.New(r.Error)
	}
	return r.Text, err
}

func (a *piAgent) Steer(text string) error {
	_, err := a.p.call(context.Background(), map[string]any{"op": "steer", "agent": a.id, "text": text})
	return err
}

func (a *piAgent) Stop() error {
	_, err := a.p.call(context.Background(), map[string]any{"op": "stop", "agent": a.id})
	return err
}
