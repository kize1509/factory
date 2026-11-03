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
		"name": "record_check",
		"description": "Patrol agents only: after `factory test check`, record what you make of the run. run is the run id from its JSON, " +
			"verdict is ok, finding or blocked (a failed run is a finding), note is one line.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"run": str_(), "verdict": str_(), "note": str_()}, "required": []string{"run", "verdict"}},
	},
	{
		"name": "ask_ops",
		"description": "Ask the operations agent (Hermes) a question: is there a ticket about X, what did the planner decide about Y, what did the user say on HF-5, who works on what. " +
			"It answers briefly and names its sources (issue IDs, job, agent, event).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"question": str_()}, "required": []string{"question"}},
	},
	{
		"name": "report",
		"description": "Tell the operations agent something the factory should remember: what you did, a decision, a finding, a blocker, or a note from the user. " +
			"kind is did, decision, finding, blocker or user_note; issues lists the tickets it is about.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"kind": str_(), "text": str_(), "issues": list_()}, "required": []string{"kind", "text"}},
	},
	{
		"name": "tracker_search", "description": "Hermes only: search the tracker with a YouTrack query (the mapped project is added).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": str_()}, "required": []string{"query"}},
	},
	{
		"name": "tracker_read", "description": "Hermes only: read one issue with its comments, links, attachments, tags and recent changes.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": str_()}, "required": []string{"id"}},
	},
	{
		"name": "knowledge_search", "description": "Hermes only: search the notes the factory kept (decisions, findings, blockers, user notes).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"query": str_()}, "required": []string{"query"}},
	},
	{
		"name": "tracker_create", "description": "Hermes only: create an issue in the mapped project. Description: what is wrong today, Expected, Done when, with file paths and line hints.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{
			"project": str_(), "summary": str_(), "description": str_(), "type": str_(), "priority": str_(), "subsystem": str_(), "complexity": str_(), "ship_mode": str_(),
			"tags":  list_(),
			"links": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"type": str_(), "id": str_()}, "required": []string{"type", "id"}}},
		}, "required": []string{"summary", "description"}},
	},
	{
		"name": "tracker_update", "description": "Hermes only: set fields (Type, Priority, Subsystem, Complexity, Ship mode, Assignee, State) on an issue.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": str_(), "fields": map[string]any{"type": "object", "additionalProperties": str_()}}, "required": []string{"id", "fields"}},
	},
	{
		"name": "tracker_move", "description": "Hermes only: move an issue to a state on the board.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": str_(), "state": str_()}, "required": []string{"id", "state"}},
	},
	{
		"name": "tracker_comment", "description": "Hermes only: comment on an issue.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": str_(), "text": str_()}, "required": []string{"id", "text"}},
	},
	{
		"name": "tracker_link", "description": "Hermes only: link an issue to another (subtask of, parent for, relates to, duplicates, is duplicated by, depends on, is required for), optionally with a tag.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": str_(), "type": str_(), "target": str_(), "tag": str_()}, "required": []string{"id", "type", "target"}},
	},
	{
		"name": "tracker_attach", "description": "Hermes only: attach a file (screenshot, video, log) from the repo or the factory's folder to an issue.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"id": str_(), "path": str_()}, "required": []string{"id", "path"}},
	},
	{
		"name": "answer", "description": "Hermes only: end a question with a short answer and its sources (issue IDs, job, agent, event #).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": str_(), "sources": list_()}, "required": []string{"text"}},
	},
	{
		"name": "handover", "description": "Hermes only: end a handover with context, decisions, open points and links for the next agent.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"context": str_(), "decisions": list_(), "open_points": list_(), "links": list_()}, "required": []string{"context"}},
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
		if ps := s.passOf(r.PathValue("job")); ps != nil {
			if text, handled, err := s.opsTool(r.PathValue("job"), r.PathValue("agent"), p.Name, p.Arguments); handled {
				if err != nil {
					reply(toolResult(err.Error(), true))
					return
				}
				reply(toolResult(text, false))
				return
			}
			text, err := passTool(ps, r.PathValue("agent"), p.Name, p.Arguments)
			if err != nil {
				reply(toolResult(err.Error(), true))
				return
			}
			reply(toolResult(text, false))
			return
		}
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
