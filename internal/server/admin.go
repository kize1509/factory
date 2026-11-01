package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"factory/internal/config"
	"factory/internal/events"
	"factory/internal/gateway"
	"factory/internal/prices"
	"factory/internal/prompts"
	"factory/internal/skills"
	"factory/internal/store"
)

// The admin panel's API: connections and runtimes, the model list from the
// wire, settings (roles, YouTrack, limits) and metrics built from events.

func (s *Server) adminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/admin/status", s.adminStatus)
	mux.HandleFunc("GET /api/admin/models", s.adminModels)
	mux.HandleFunc("GET /api/admin/settings", s.getSettings)
	mux.HandleFunc("PUT /api/admin/settings", s.putSettings)
	mux.HandleFunc("GET /api/admin/metrics", s.adminMetrics)
	mux.HandleFunc("GET /api/admin/prompts", s.getPrompts)
	mux.HandleFunc("GET /api/admin/prices", s.getPrices)
	mux.HandleFunc("POST /api/admin/prices/sync", s.syncPrices)
	mux.HandleFunc("PUT /api/admin/prices", s.putPrice)
	mux.HandleFunc("GET /api/admin/skills", s.getSkills)
	mux.HandleFunc("POST /api/admin/skills/sync", s.syncSkills)
	mux.HandleFunc("PUT /api/admin/skills", s.putSkills)
	mux.HandleFunc("PUT /api/admin/prompts/{role}", s.putPrompt)
	mux.HandleFunc("DELETE /api/admin/prompts/{role}", s.resetPrompt)
}

func version(name string, args ...string) string {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

func (s *Server) adminStatus(w http.ResponseWriter, _ *http.Request) {
	wireOK := false
	if resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(s.Gateway.Wire + "/pi/anthropic/v1/models"); err == nil {
		wireOK = resp.StatusCode == 200
		resp.Body.Close()
	}
	pi := ""
	if data, err := os.ReadFile(filepath.Join(s.Root, "pi-host", "node_modules", "@earendil-works", "pi-coding-agent", "package.json")); err == nil {
		var p struct{ Version string }
		json.Unmarshal(data, &p)
		pi = p.Version
	}
	settings := config.LoadEngine()
	writeJSON(w, map[string]any{
		"wire":     map[string]any{"url": s.Gateway.Wire, "ok": wireOK, "central": version("central", "--version")},
		"credits":  credits(),
		"youtrack": map[string]any{"url": settings.YouTrack.URL, "token_set": settings.YouTrack.Token != ""},
		"runtimes": []map[string]any{
			{"id": "pi", "name": "Pi", "version": pi, "installed": pi != "", "primary": true, "runs": "Claude and GPT models"},
			{"id": "claude-code", "name": "Claude Code", "version": version("claude", "--version"), "installed": version("claude", "--version") != "", "runs": "Claude models"},
			{"id": "codex", "name": "Codex", "version": version("codex", "--version"), "installed": version("codex", "--version") != "", "runs": "GPT models"},
		},
		"node":   version("node", "--version"),
		"engine": map[string]any{"started": s.Started, "jobs": len(s.jobs)},
	})
}

// adminModels lists what the wire offers, for both providers.
func (s *Server) adminModels(w http.ResponseWriter, _ *http.Request) {
	out := map[string][]string{}
	client := &http.Client{Timeout: 8 * time.Second}
	for provider, path := range map[string]string{"anthropic": "/pi/anthropic/v1/models", "openai": "/pi/openai/v1/models"} {
		req, _ := http.NewRequest("GET", s.Gateway.Wire+path, nil)
		req.Header.Set("x-api-key", "wire-proxy")
		req.Header.Set("authorization", "Bearer wire-proxy")
		req.Header.Set("anthropic-version", "2023-06-01")
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		json.Unmarshal(body, &list)
		for _, m := range list.Data {
			if provider == "openai" && !strings.HasPrefix(m.ID, "gpt-") && !strings.HasPrefix(m.ID, "o") {
				continue
			}
			if strings.Contains(m.ID, "transcribe") || strings.Contains(m.ID, "embedding") || strings.HasPrefix(m.ID, "whisper") {
				continue
			}
			out[provider] = append(out[provider], provider+"/"+m.ID)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(out[provider])))
	}
	writeJSON(w, out)
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	e := config.LoadEngine()
	roles := map[string]config.Role{}
	for _, name := range config.RoleNames {
		roles[name] = e.RoleFor(name)
	}
	token := ""
	if e.YouTrack.Token != "" {
		token = "••••" + e.YouTrack.Token[max(0, len(e.YouTrack.Token)-4):]
	}
	writeJSON(w, map[string]any{"roles": roles, "role_names": config.RoleNames, "youtrack": map[string]any{"url": e.YouTrack.URL, "token": token},
		"limits": e.Limits, "wire": e.Wire})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Roles    map[string]config.Role `json:"roles"`
		YouTrack struct {
			URL   string `json:"url"`
			Token string `json:"token"` // empty or masked keeps the stored one
		} `json:"youtrack"`
		Limits *config.Limits `json:"limits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e := config.LoadEngine()
	if in.Roles != nil {
		problems := map[string]string{}
		for name, role := range in.Roles {
			if p := config.CheckRole(role); p != "" {
				problems[name] = p
			}
		}
		if len(problems) > 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			writeJSON(w, map[string]any{"problems": problems})
			return
		}
		e.Roles = in.Roles
	}
	e.YouTrack.URL = strings.TrimSpace(in.YouTrack.URL)
	if t := strings.TrimSpace(in.YouTrack.Token); t != "" && !strings.HasPrefix(t, "••••") {
		e.YouTrack.Token = t
	}
	if in.Limits != nil {
		e.Limits = *in.Limits
	}
	if err := config.SaveEngine(e); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.Bus.Emit("", "", "settings.saved", nil)
	writeJSON(w, map[string]any{"ok": true})
}

// metrics is everything the Metrics tab shows, worked out from the event logs.
type metrics struct {
	Totals struct {
		USD      float64 `json:"usd"`
		Input    int     `json:"input"`
		Output   int     `json:"output"`
		Cache    int     `json:"cache_read"`
		Requests int     `json:"requests"`
		Jobs     int     `json:"jobs"`
	} `json:"totals"`
	Jobs   []jobMetric          `json:"jobs"`
	Models map[string]*bucket   `json:"models"`
	Agents map[string]*bucket   `json:"agents"`
	Tools  map[string]int       `json:"tools"`
	Phases map[string]float64   `json:"phase_seconds"`
	Edges  map[string]int       `json:"edges"` // "from>to" -> count
	Checks map[string]*checkTot `json:"checks"`
	// Messages are the individual events behind Edges, newest last.
	Messages []edgeMsg `json:"messages"`
}

type edgeMsg struct {
	Job  string    `json:"job"`
	Time time.Time `json:"time"`
	From string    `json:"from"`
	To   string    `json:"to"`
	Kind string    `json:"kind"` // handoff | check | you
	Text string    `json:"text"`
}

// short trims s to n runes for the message list.
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

type bucket struct {
	USD      float64 `json:"usd"`
	Input    int     `json:"input"`
	Output   int     `json:"output"`
	Cache    int     `json:"cache_read"`
	Requests int     `json:"requests"`
}

type checkTot struct{ Passed, Failed int }

type jobMetric struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	State    string  `json:"state"`
	USD      float64 `json:"usd"`
	Seconds  float64 `json:"seconds"`
	Loops    int     `json:"loops"`
	Handoffs struct {
		Written, Missing, Rejected int
	} `json:"handoffs"`
	Requests int `json:"requests"`
}

func (s *Server) adminMetrics(w http.ResponseWriter, _ *http.Request) {
	m := metrics{Models: map[string]*bucket{}, Agents: map[string]*bucket{}, Tools: map[string]int{}, Phases: map[string]float64{},
		Edges: map[string]int{}, Checks: map[string]*checkTot{}, Messages: []edgeMsg{}}
	s.mu.Lock()
	ids := make([]string, 0, len(s.jobs))
	titles := map[string]string{}
	for id, j := range s.jobs {
		ids = append(ids, id)
		titles[id] = j.Title
	}
	s.mu.Unlock()
	sort.Strings(ids)
	add := func(b map[string]*bucket, k string, d map[string]any) {
		if b[k] == nil {
			b[k] = &bucket{}
		}
		x := b[k]
		x.USD += num(d["usd"])
		x.Input += int(num(d["input"]))
		x.Output += int(num(d["output"]))
		x.Cache += int(num(d["cache_read"]))
		x.Requests++
	}
	for _, id := range ids {
		log, _ := s.Bus.Replay(id)
		jm := jobMetric{ID: id, Title: titles[id]}
		var phase string
		var phaseStart, first, last time.Time
		for _, e := range log {
			if first.IsZero() {
				first = e.Time
			}
			last = e.Time
			d := e.Data
			switch e.Type {
			case "usage":
				add(m.Models, str(d["model"]), d)
				add(m.Agents, e.Agent, d)
				jm.USD += num(d["usd"])
				jm.Requests++
				m.Totals.USD += num(d["usd"])
				m.Totals.Input += int(num(d["input"]))
				m.Totals.Output += int(num(d["output"]))
				m.Totals.Cache += int(num(d["cache_read"]))
				m.Totals.Requests++
			case "job.state":
				if phase != "" {
					m.Phases[phase] += e.Time.Sub(phaseStart).Seconds()
				}
				phase, phaseStart = str(d["state"]), e.Time
				jm.State = phase
			case "agent.tool":
				m.Tools[str(d["tool"])]++
			case "check.passed", "check.failed":
				c := str(d["command"])
				if m.Checks[c] == nil {
					m.Checks[c] = &checkTot{}
				}
				if e.Type == "check.passed" {
					m.Checks[c].Passed++
				} else {
					m.Checks[c].Failed++
					to := agentOr(e, "archimedes")
					m.Edges["oracle>"+to]++
					m.Messages = append(m.Messages, edgeMsg{id, e.Time, "oracle", to, "check", short(c+" (exit "+strconv.Itoa(int(num(d["exit_code"])))+")", 200)})
				}
			case "checks.failed":
				jm.Loops++
			case "handoff.written":
				jm.Handoffs.Written++
				if n, ok := d["note"].(map[string]any); ok {
					to := strings.Fields(str(n["to"]) + " next")[0]
					m.Edges[e.Agent+">"+to]++
					m.Messages = append(m.Messages, edgeMsg{id, e.Time, e.Agent, to, "handoff", short(str(n["did"])+" → "+str(n["next"]), 200)})
				}
			case "handoff.missing":
				jm.Handoffs.Missing++
			case "handoff.rejected":
				jm.Handoffs.Rejected++
			case "human.message":
				m.Edges["you>"+e.Agent]++
				m.Messages = append(m.Messages, edgeMsg{id, e.Time, "you", e.Agent, "you", short(str(d["text"]), 200)})
			}
		}
		jm.Seconds = last.Sub(first).Seconds()
		m.Jobs = append(m.Jobs, jm)
	}
	m.Totals.Jobs = len(m.Jobs)
	sort.SliceStable(m.Messages, func(i, j int) bool { return m.Messages[i].Time.Before(m.Messages[j].Time) })
	if len(m.Messages) > 500 {
		m.Messages = m.Messages[len(m.Messages)-500:]
	}
	writeJSON(w, m)
}

func agentOr(e events.Event, fallback string) string {
	if e.Agent != "" && e.Agent != "oracle" {
		return e.Agent
	}
	return fallback
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// credits reads the "Usage: x / y credits" line from `central limit`.
func credits() string {
	out, _ := exec.Command("central", "limit").CombinedOutput()
	plain := regexp.MustCompile("\x1b\\[[0-9;]*m").ReplaceAllString(string(out), "")
	for _, line := range strings.Split(plain, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "Usage:") {
			return strings.TrimSpace(strings.TrimPrefix(t, "Usage:"))
		}
	}
	return ""
}

// prompts: the system prompt per role, with your edits (Admin → Prompts)

var liveRoles = map[string]bool{"builder": true, "reviewer": true, "planner": true} // roles the engine runs today

func (s *Server) getPrompts(w http.ResponseWriter, _ *http.Request) {
	var out []map[string]any
	for _, role := range config.RoleNames {
		out = append(out, map[string]any{"role": role, "text": prompts.For(role), "default": prompts.Default(role),
			"edited": prompts.Overridden(role), "live": liveRoles[role]})
	}
	writeJSON(w, out)
}

func (s *Server) putPrompt(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "the prompt can't be empty", http.StatusBadRequest)
		return
	}
	if err := prompts.Save(r.PathValue("role"), body.Text); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) resetPrompt(w http.ResponseWriter, r *http.Request) {
	if err := prompts.Reset(r.PathValue("role")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// skills: the local copy of mattpocock/skills and which role gets which

func (s *Server) getSkills(w http.ResponseWriter, _ *http.Request) {
	e := config.LoadEngine()
	byRole := map[string][]string{}
	for _, role := range config.RoleNames {
		byRole[role] = e.SkillsFor(role)
	}
	writeJSON(w, map[string]any{"repo": skills.Repo, "dir": skills.Dir(), "version": skills.Version(),
		"skills": skills.List(), "roles": config.RoleNames, "by_role": byRole, "live": liveRoles})
}

func (s *Server) syncSkills(w http.ResponseWriter, _ *http.Request) {
	v, err := skills.Sync()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.Bus.Emit("", "", "skills.synced", map[string]any{"version": v})
	writeJSON(w, map[string]any{"version": v})
}

func (s *Server) putSkills(w http.ResponseWriter, r *http.Request) {
	var byRole map[string][]string
	if err := json.NewDecoder(r.Body).Decode(&byRole); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e := config.LoadEngine()
	e.Skills = byRole
	if err := config.SaveEngine(e); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// prices: synced from models.dev (LiteLLM as fallback), editable by hand

// ApplyPrices loads stored prices into the gateway.
func (s *Server) ApplyPrices() int {
	list, _ := s.Store.Prices()
	m := map[string]gateway.Price{}
	for _, p := range list {
		m[p.Model] = gateway.Price{In: p.Input, Out: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite}
	}
	gateway.SetPrices(m)
	return len(list)
}

// SyncPrices fetches current prices, keeps the ones you set by hand, and
// works out past costs again with them.
func (s *Server) SyncPrices(ctx context.Context) (string, int, error) {
	list, source, err := prices.Fetch(ctx)
	if err != nil {
		return "", 0, err
	}
	if err := s.Store.SavePrices(list, true); err != nil {
		return "", 0, err
	}
	n := s.ApplyPrices()
	s.Store.RecomputeUsage(gateway.Cost)
	s.Bus.Emit("", "", "prices.synced", map[string]any{"source": source, "models": len(list)})
	return source, n, nil
}

func (s *Server) getPrices(w http.ResponseWriter, _ *http.Request) {
	list, err := s.Store.Prices()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	used := map[string]bool{} // models a role uses come first
	e := config.LoadEngine()
	for _, r := range config.RoleNames {
		used[e.RoleFor(r).Model] = true
	}
	writeJSON(w, map[string]any{"prices": list, "used": used, "sources": []string{prices.ModelsDev, prices.LiteLLM}})
}

func (s *Server) syncPrices(w http.ResponseWriter, r *http.Request) {
	source, n, err := s.SyncPrices(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{"source": source, "models": n})
}

func (s *Server) putPrice(w http.ResponseWriter, r *http.Request) {
	var p store.Price
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Model == "" {
		http.Error(w, "need model and prices", http.StatusBadRequest)
		return
	}
	p.Source = "manual"
	if err := s.Store.SavePrices([]store.Price{p}, false); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.ApplyPrices()
	s.Store.RecomputeUsage(gateway.Cost)
	writeJSON(w, map[string]any{"ok": true})
}
