// Package events is the engine's single source of truth: everything that
// happens is an Event, appended to the job's log and fanned out to
// subscribers.
package events

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Event struct {
	Seq   int64          `json:"seq"`
	Time  time.Time      `json:"time"`
	Job   string         `json:"job"`
	Agent string         `json:"agent,omitempty"`
	Type  string         `json:"type"`
	Data  map[string]any `json:"data,omitempty"`
}

// Bus appends events to per-job JSONL files and delivers them to
// subscribers. Subscribers that fall behind lose events rather than block
// the engine; they can replay from the log.
type Bus struct {
	dir  string
	mu   sync.Mutex
	seq  int64
	subs map[chan Event]string // channel -> job filter ("" for all)
	logs map[string]*os.File
	dirs map[string]string // job -> folder its log lives in

	// Persist, when set, stores each event (in SQLite) and returns its
	// sequence number; Load reads a job's events back from there.
	Persist func(Event) int64
	Load    func(job string) ([]Event, error)
}

func NewBus(dir string) *Bus {
	return &Bus{dir: dir, subs: map[chan Event]string{}, logs: map[string]*os.File{}, dirs: map[string]string{}}
}

// Place keeps a job's log in its own folder (the project's .factory/jobs/<id>)
// instead of the bus's default folder.
func (b *Bus) Place(job, dir string) {
	b.mu.Lock()
	b.dirs[job] = dir
	b.mu.Unlock()
}

func (b *Bus) jobDir(job string) string {
	if d, ok := b.dirs[job]; ok {
		return d
	}
	return filepath.Join(b.dir, job)
}

// Emit records and publishes one event.
func (b *Bus) Emit(job, agent, typ string, data map[string]any) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e := Event{Seq: b.seq, Time: time.Now(), Job: job, Agent: agent, Type: typ, Data: data}
	if b.Persist != nil {
		e.Seq = b.Persist(e)
	}
	if f := b.log(job); f != nil {
		line, _ := json.Marshal(e)
		f.Write(append(line, '\n'))
	}
	for ch, filter := range b.subs {
		if filter != "" && filter != job {
			continue
		}
		select {
		case ch <- e:
		default:
		}
	}
	return e
}

func (b *Bus) log(job string) *os.File {
	if job == "" {
		return nil
	}
	if f, ok := b.logs[job]; ok {
		return f
	}
	path := filepath.Join(b.jobDir(job), "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	b.logs[job] = f
	return f
}

// Subscribe returns a channel of new events for one job, or all jobs when
// job is empty. Call the returned function to stop.
func (b *Bus) Subscribe(job string) (<-chan Event, func()) {
	ch := make(chan Event, 256)
	b.mu.Lock()
	b.subs[ch] = job
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// Replay reads a job's events: from the database when there is one, else
// from the job's events.jsonl.
func (b *Bus) Replay(job string) ([]Event, error) {
	if b.Load != nil {
		if evs, err := b.Load(job); err == nil && len(evs) > 0 {
			return evs, nil
		}
	}
	return b.ReplayFile(job)
}

// ReplayFile reads a job's events.jsonl.
func (b *Bus) ReplayFile(job string) ([]Event, error) {
	b.mu.Lock()
	dir := b.jobDir(job)
	b.mu.Unlock()
	f, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, f := range b.logs {
		f.Close()
	}
}
