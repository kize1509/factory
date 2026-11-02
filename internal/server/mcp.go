package server

import (
	"encoding/json"
	"net/http"
)

// A small MCP server (streamable HTTP, JSON replies) so Claude Code and
// Codex agents get the same factory tools as Pi agents. Each agent has its
// own endpoint, /mcp/<job>/<agent>, so the engine knows who is calling.

var mcpTools = []map[string]any{
	{
		"name": "handoff",
		"description": "Write the handoff note for whoever works on this next. Required at the end of your phase. " +
			"files lists repo paths that exist; evidence is optional and may point into your scratch folder. " +
			"Leave open_questions empty: ask for what you need with ask_human while you work.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"from": str_(), "to": str_(), "did": str_(), "next": str_(),
				"decisions": list_(), "findings": list_(), "files": list_(), "open_questions": list_(), "evidence": list_(),
			},
			"required": []string{"from", "to", "did", "decisions", "findings", "files", "open_questions", "next"},
		},
	},
	{
		"name": "set_checks",
		"description": "Name the exact shell commands that test this work, based on the test notes in your task. " +
			"Use the project's existing tests and tools. The engine runs them after you finish and decides whether the work passes.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"commands": list_(), "why": str_()}, "required": []string{"commands"}},
	},
	{
		"name":        "ask_human",
		"description": "Ask the person running the factory for something only they can give, the moment you need it: credentials, an API key or token, access, an account, a URL, or a decision the ticket doesn't make. Also when you're stuck on the same problem after two tries. need: one sentence that starts with what you need, e.g. \"I need the Stripe test API key\" or \"I'm stuck on the failing login check: should I mock the clock or raise the timeout?\". why: one or two sentences on what you tried and why you can't go on. options: the choices, when it's a decision. Then end your turn; the answer comes as your next message.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"need": str_(), "why": str_(), "options": list_()}, "required": []string{"need"}},
	},
	{
		"name": "ask_builder",
		"description": "Reviewers only: ask the builder one question about a decision in its work and get its answer. " +
			"Grill on decisions that matter, one question at a time, at most 4 per round.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"question": str_()}, "required": []string{"question"}},
	},
	{
		"name": "propose_experiments",
		"description": "When a task has two or more workable approaches and none is clearly best, or the ticket is vague: propose 2 to 4 experiments. " +
			"The engine tries each on its own branch with its own builder, grades them blind and keeps the best.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"why": str_(),
			"experiments": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
				"name": str_(), "hypothesis": str_(), "approach": str_(), "success_signal": str_()}, "required": []string{"name", "hypothesis", "approach"}}},
		}, "required": []string{"experiments"}},
	},
	{
		"name":        "grade",
		"description": "Graders only: score one experiment (by its letter) from 1 to 5, with a short reason.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"experiment": str_(), "score": map[string]any{"type": "integer"}, "reason": str_()},
			"required": []string{"experiment", "score"}},
	},
	{
		"name":        "verdict",
		"description": "Reviewers only: end the review. approved true, or false with blocking findings (file, line, why, fix).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"approved": map[string]any{"type": "boolean"}, "findings": list_(), "summary": str_()}, "required": []string{"approved", "findings"}},
	},
	{
		"name":        "name_job",
		"description": "Planners only, first thing: give the job a short readable title (at most 60 characters, plain words) and, if asked, the project a name. Shown on the map.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"title": str_(), "project": str_()}, "required": []string{"title"}},
	},
	{
		"name":        "set_status",
		"description": "Say in a few words what you are doing now. Shown above your head on the map.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"step": str_()}, "required": []string{"step"}},
	},
}

func str_() map[string]any  { return map[string]any{"type": "string"} }
func list_() map[string]any { return map[string]any{"type": "array", "items": str_()} }

type rpc struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	var req rpc
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.ID) == 0 { // a notification needs no answer
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reply := func(result any) {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = "2025-06-18"
		}
		reply(map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "factory", "version": "0.1"},
		})
	case "ping":
		reply(map[string]any{})
	case "tools/list":
		reply(map[string]any{"tools": mcpTools})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		json.Unmarshal(req.Params, &p)
		s.mu.Lock()
		j := s.jobs[r.PathValue("job")]
		s.mu.Unlock()
		if j == nil {
			reply(toolResult("no job "+r.PathValue("job"), true))
			return
		}
		text, err := s.callTool(j, r.PathValue("agent"), p.Name, p.Arguments)
		if err != nil {
			reply(toolResult(err.Error(), true))
			return
		}
		reply(toolResult(text, false))
	default:
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found: " + req.Method}})
	}
}

func toolResult(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}
