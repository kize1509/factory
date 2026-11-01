// Package gateway is the metering proxy every agent's model traffic goes
// through on its way to the JetBrains Central wire. It reads token usage
// from the stream, turns it into dollars, and can hold an agent's requests
// to pause it.
package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"factory/internal/events"
)

type Gateway struct {
	Wire string // e.g. http://127.0.0.1:19516/wire/<id>
	Bus  *events.Bus

	mu     sync.Mutex
	paused map[string]chan struct{} // job/agent -> closed when resumed
}

func New(wire string, bus *events.Bus) *Gateway {
	return &Gateway{Wire: strings.TrimRight(wire, "/"), Bus: bus, paused: map[string]chan struct{}{}}
}

// Pause holds the agent's next model request until Resume.
func (g *Gateway) Pause(job, agent string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.paused[job+"/"+agent]; !ok {
		g.paused[job+"/"+agent] = make(chan struct{})
		g.Bus.Emit(job, agent, "agent.paused", nil)
	}
}

func (g *Gateway) Resume(job, agent string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ch, ok := g.paused[job+"/"+agent]; ok {
		close(ch)
		delete(g.paused, job+"/"+agent)
		g.Bus.Emit(job, agent, "agent.resumed", nil)
	}
}

func (g *Gateway) gate(job, agent string) chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused[job+"/"+agent]
}

// ServeHTTP handles /llm/<job>/<agent>/<rest> and forwards <rest> to the wire.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/llm/"), "/", 3)
	if len(parts) < 3 {
		http.Error(w, "want /llm/<job>/<agent>/<path>", http.StatusBadRequest)
		return
	}
	job, agent, rest := parts[0], parts[1], parts[2]
	if ch := g.gate(job, agent); ch != nil {
		select {
		case <-ch:
		case <-r.Context().Done():
			return
		}
	}
	model := ""
	if r.Body != nil {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		var m struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &m)
		model = m.Model
	}
	target, _ := url.Parse(g.Wire + "/" + rest)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.URL.RawQuery = r.URL.RawQuery
			pr.Out.Host = target.Host
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			resp.Body = &meter{ReadCloser: resp.Body, g: g, job: job, agent: agent, usage: Usage{Model: model}}
			return nil
		},
	}
	proxy.ServeHTTP(w, r)
}

// meter watches a response as it streams and emits usage when it ends.
type meter struct {
	io.ReadCloser
	g          *Gateway
	job, agent string
	usage      Usage
	line       bytes.Buffer
	whole      bytes.Buffer // for non-streaming JSON bodies
	done       bool
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.ReadCloser.Read(p)
	if n > 0 {
		m.scan(p[:n])
	}
	if err == io.EOF {
		m.finish()
	}
	return n, err
}

func (m *meter) Close() error { m.finish(); return m.ReadCloser.Close() }

func (m *meter) scan(b []byte) {
	if m.whole.Len() < 1<<20 {
		m.whole.Write(b)
	}
	m.line.Write(b)
	for {
		i := bytes.IndexByte(m.line.Bytes(), '\n')
		if i < 0 {
			return
		}
		line := strings.TrimSpace(string(m.line.Next(i + 1)))
		if strings.HasPrefix(line, "data:") {
			m.event([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))))
		}
	}
}

// event reads usage from one streamed event, Anthropic or OpenAI style.
func (m *meter) event(data []byte) {
	var e struct {
		Type    string `json:"type"`
		Message struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		Usage    anthropicUsage `json:"usage"`
		Response struct {
			Model string      `json:"model"`
			Usage openaiUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &e) != nil {
		return
	}
	switch e.Type {
	case "message_start":
		if e.Message.Model != "" {
			m.usage.Model = e.Message.Model
		}
		e.Message.Usage.addTo(&m.usage)
	case "message_delta":
		if e.Usage.OutputTokens > 0 {
			m.usage.Output = e.Usage.OutputTokens
		}
	case "response.completed":
		if e.Response.Model != "" {
			m.usage.Model = e.Response.Model
		}
		e.Response.Usage.addTo(&m.usage)
	}
}

func (m *meter) finish() {
	if m.done {
		return
	}
	m.done = true
	if m.usage.Input+m.usage.Output == 0 {
		// not streamed: look for a usage object in the whole body
		var body struct {
			Model string          `json:"model"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(m.whole.Bytes(), &body) == nil && len(body.Usage) > 0 {
			if body.Model != "" {
				m.usage.Model = body.Model
			}
			var a anthropicUsage
			var o openaiUsage
			json.Unmarshal(body.Usage, &a)
			json.Unmarshal(body.Usage, &o)
			a.addTo(&m.usage)
			if m.usage.Input+m.usage.Output == 0 {
				o.addTo(&m.usage)
			}
		}
	}
	if m.usage.Input+m.usage.Output == 0 {
		return
	}
	u := m.usage
	m.g.Bus.Emit(m.job, m.agent, "usage", map[string]any{
		"model": u.Model, "input": u.Input, "output": u.Output,
		"cache_read": u.CacheRead, "cache_write": u.CacheWrite, "usd": u.USD(),
	})
}

type anthropicUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

func (a anthropicUsage) addTo(u *Usage) {
	u.Input += a.InputTokens
	u.Output = max(u.Output, a.OutputTokens)
	u.CacheWrite += a.CacheCreationTokens
	u.CacheRead += a.CacheReadTokens
}

type openaiUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Details      struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (o openaiUsage) addTo(u *Usage) {
	u.Input += o.InputTokens - o.Details.CachedTokens
	u.CacheRead += o.Details.CachedTokens
	u.Output = max(u.Output, o.OutputTokens)
}
