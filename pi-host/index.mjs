// Runs Pi sessions for the factory engine. One process hosts many agents.
// Requests arrive as JSON lines on stdin; replies and events go out as JSON
// lines on stdout. See docs/ENGINE.md §5.
import { createInterface } from "node:readline";
import { mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { Type } from "typebox";
import {
  createAgentSession,
  defineTool,
  DefaultResourceLoader,
  ModelRuntime,
  SessionManager,
} from "@earendil-works/pi-coding-agent";

const agents = new Map(); // agent id -> { session, flush }

const send = (msg) => process.stdout.write(JSON.stringify(msg) + "\n");
const event = (agent, type, data) => send({ event: type, agent, data });

// Factory tools call back into the engine, which decides and logs (D2).
function factoryTools(req) {
  const call = async (tool, params) => {
    const res = await fetch(`${req.engine}/api/tools/${req.job}/${req.agent}/${tool}`, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(params),
    });
    const text = await res.text();
    return { content: [{ type: "text", text: res.ok ? text : `Error: ${text}` }], details: {} };
  };
  return [
    defineTool({
      name: "handoff",
      label: "Handoff",
      description:
        "Write the handoff note for whoever works on this next. Required at the end of your phase. " +
        "Fields: from, to, did, decisions, findings, files (repo paths that exist), open_questions (leave empty: ask for what you need with ask_human while you work), next, evidence (optional: paths in your scratch folder or the repo).",
      parameters: Type.Object({
        from: Type.String(),
        to: Type.String(),
        did: Type.String(),
        decisions: Type.Array(Type.String()),
        findings: Type.Array(Type.String()),
        files: Type.Array(Type.String()),
        open_questions: Type.Array(Type.String()),
        next: Type.String(),
        evidence: Type.Optional(Type.Array(Type.String())),
      }),
      execute: (_id, params) => call("handoff", params),
    }),
    defineTool({
      name: "set_checks",
      label: "Set checks",
      description:
        "Name the exact shell commands that test this work, based on the test notes in your task. " +
        "Use the project's existing tests and tools. The engine runs them after you finish and decides whether the work passes.",
      parameters: Type.Object({ commands: Type.Array(Type.String()), why: Type.Optional(Type.String()) }),
      execute: (_id, params) => call("set_checks", params),
    }),
    defineTool({
      name: "ask_human",
      label: "Ask you",
      description: "Ask the person running the factory for something only they can give, the moment you need it: credentials, an API key or token, access, an account, a URL, or a decision the ticket doesn't make. Also when you're stuck on the same problem after two tries. need: one sentence that starts with what you need, e.g. \"I need the Stripe test API key\" or \"I'm stuck on the failing login check: should I mock the clock or raise the timeout?\". why: one or two sentences on what you tried and why you can't go on. options: the choices, when it's a decision. Then end your turn; the answer comes as your next message.",
      parameters: Type.Object({ need: Type.String(), why: Type.Optional(Type.String()), options: Type.Optional(Type.Array(Type.String())) }),
      execute: (_id, params) => call("ask_human", params),
    }),
    defineTool({
      name: "ask_builder",
      label: "Ask the builder",
      description: "Reviewers only: ask the builder one question about a decision in its work and get its answer. Grill on decisions that matter, one question at a time, at most 4 per round.",
      parameters: Type.Object({ question: Type.String() }),
      execute: (_id, params) => call("ask_builder", params),
    }),
    defineTool({
      name: "ask_agent",
      label: "Ask an agent",
      description: "Ask another agent of this job one question and get its answer. Use it when your input is unclear: ask the agent that wrote it (the roster says who) before you guess. Use to \"operations\" when you don't know who to ask. It waits for the answer, then you carry on; an agent in the middle of its own turn can't answer, and you get at most 3 questions per turn. A credential, access or a decision only the person can make is ask_human, not this. sources: files, notes or diffs the question is about.",
      parameters: Type.Object({ to: Type.String(), question: Type.String(), sources: Type.Optional(Type.Array(Type.String())) }),
      execute: (_id, params) => call("ask_agent", params),
    }),
    defineTool({
      name: "roster",
      label: "Roster",
      description: "Who is working on this job right now: their role, what they are doing and their state, whose output you read, who reads yours, and who to ask about what. Read-only.",
      parameters: Type.Object({}),
      execute: (_id, params) => call("roster", params),
    }),
    defineTool({
      name: "verdict",
      label: "Verdict",
      description: "Reviewers only: end the review. approved true, or false with blocking findings (file, line, why, fix).",
      parameters: Type.Object({ approved: Type.Boolean(), findings: Type.Array(Type.String()), summary: Type.Optional(Type.String()) }),
      execute: (_id, params) => call("verdict", params),
    }),
    defineTool({
      name: "propose_experiments",
      label: "Propose experiments",
      description: "When a task has two or more workable approaches and none is clearly best, or the ticket is vague: propose 2 to 4 experiments. The engine tries each on its own branch with its own builder, grades them blind and keeps the best.",
      parameters: Type.Object({
        why: Type.Optional(Type.String()),
        experiments: Type.Array(Type.Object({ name: Type.String(), hypothesis: Type.String(), approach: Type.String(), success_signal: Type.Optional(Type.String()) })),
      }),
      execute: (_id, params) => call("propose_experiments", params),
    }),
    defineTool({
      name: "grade",
      label: "Grade",
      description: "Graders only: score one experiment (by its letter) from 1 to 5, with a short reason.",
      parameters: Type.Object({ experiment: Type.String(), score: Type.Integer(), reason: Type.Optional(Type.String()) }),
      execute: (_id, params) => call("grade", params),
    }),
    defineTool({
      name: "name_job",
      label: "Name the job",
      description: "Planners only, first thing: give the job a short readable title (at most 60 characters, plain words) and, if asked, the project a name. Shown on the map.",
      parameters: Type.Object({ title: Type.String(), project: Type.Optional(Type.String()) }),
      execute: (_id, params) => call("name_job", params),
    }),
    defineTool({
      name: "set_status",
      label: "Set status",
      description: "Say in a few words what you are doing now. Shown above your head on the map.",
      parameters: Type.Object({ step: Type.String() }),
      execute: (_id, params) => call("set_status", params),
    }),
  ];
}

// models.json routes Pi's built-in providers through the engine's gateway.
function writeModels(req) {
  mkdirSync(req.agentDir, { recursive: true });
  const base = `${req.engine}/llm/${req.job}/${req.agent}/pi`;
  const [provider, id] = req.model.split("/");
  const models = {
    providers: {
      anthropic: { baseUrl: `${base}/anthropic`, apiKey: "wire-proxy", api: "anthropic-messages" },
      openai: { baseUrl: `${base}/openai/v1`, apiKey: "wire-proxy", api: "openai-responses" },
    },
  };
  // Current Claude models only take adaptive thinking and no temperature,
  // the same compat Pi's own catalog uses for them.
  const compat = provider === "anthropic" ? { forceAdaptiveThinking: true, supportsTemperature: false, supportsStrictTools: true } : undefined;
  models.providers[provider].models = [{ id, name: id, reasoning: true, contextWindow: 200000, maxTokens: 32000, compat }];
  const path = join(req.agentDir, `models-${req.job}-${req.agent}.json`);
  writeFileSync(path, JSON.stringify(models, null, 2));
  return path;
}

async function start(req) {
  const modelsPath = writeModels(req);
  const modelRuntime = await ModelRuntime.create({
    modelsPath,
    authPath: join(req.agentDir, "auth.json"),
    modelsStorePath: join(req.agentDir, "models-store.json"),
  });
  const [provider, id] = req.model.split("/");
  const model = modelRuntime.getModel(provider, id);
  if (!model) throw new Error(`model ${req.model} not found`);
  const loader = new DefaultResourceLoader({
    cwd: req.cwd,
    agentDir: req.agentDir,
    noExtensions: true,
    additionalSkillPaths: req.skills || [],
    appendSystemPrompt: req.system ? [req.system] : [],
  });
  await loader.reload();
  const { session } = await createAgentSession({
    cwd: req.cwd,
    agentDir: req.agentDir,
    model,
    thinkingLevel: req.thinking || "medium",
    tools: [...(req.tools || ["read", "bash", "edit", "write", "grep", "find", "ls"]), "handoff", "set_checks", "set_status", "roster", "ask_agent",
      ...(req.role === "reviewer" ? ["ask_builder", "verdict", "grade"] : ["ask_human"]),
      ...(req.role === "builder" || req.role === "planner" ? ["propose_experiments"] : []),
      ...(req.role === "planner" ? ["name_job"] : [])],
    customTools: factoryTools(req),
    resourceLoader: loader,
    sessionManager: SessionManager.inMemory(req.cwd),
    modelRuntime,
  });

  // Thinking and text arrive in small deltas; send them in readable chunks.
  const buf = { thinking: "", text: "" };
  const flush = (kind) => {
    if (buf[kind].trim()) event(req.agent, kind === "thinking" ? "agent.thinking" : "agent.text", { text: buf[kind] });
    buf[kind] = "";
  };
  const timer = setInterval(() => { flush("thinking"); flush("text"); }, 700);
  session.subscribe((e) => {
    switch (e.type) {
      case "message_update": {
        const d = e.assistantMessageEvent;
        if (d.type === "thinking_delta") buf.thinking += d.delta;
        if (d.type === "text_delta") buf.text += d.delta;
        break;
      }
      case "tool_execution_start":
        flush("thinking"); flush("text");
        event(req.agent, "agent.tool", { tool: e.toolName, args: e.args });
        break;
      case "tool_execution_end":
        event(req.agent, "agent.tool.done", { tool: e.toolName, error: !!e.isError });
        break;
      case "auto_retry_start":
        event(req.agent, "agent.retry", { attempt: e.attempt, reason: e.errorMessage });
        break;
      case "agent_end":
        flush("thinking"); flush("text");
        break;
    }
  });
  agents.set(req.agent, { session, timer });
}

function lastText(session) {
  for (let i = session.messages.length - 1; i >= 0; i--) {
    const m = session.messages[i];
    if (m.role === "assistant") return (m.content || []).filter((c) => c.type === "text").map((c) => c.text).join("\n");
  }
  return "";
}

async function handle(req) {
  const a = agents.get(req.agent);
  switch (req.op) {
    case "start":
      await start(req);
      return {};
    case "prompt":
      if (!a) throw new Error(`no agent ${req.agent}`);
      await a.session.prompt(req.text);
      return { text: lastText(a.session), error: a.session.agent.state.errorMessage || undefined };
    case "steer":
      if (!a) throw new Error(`no agent ${req.agent}`);
      if (a.session.isStreaming) await a.session.steer(req.text);
      else await a.session.followUp(req.text);
      return {};
    case "stop":
      if (a) {
        await a.session.abort().catch(() => {});
        clearInterval(a.timer);
        a.session.dispose();
        agents.delete(req.agent);
      }
      return {};
    default:
      throw new Error(`unknown op ${req.op}`);
  }
}

const rl = createInterface({ input: process.stdin });
rl.on("line", (line) => {
  let req;
  try { req = JSON.parse(line); } catch { return; }
  handle(req)
    .then((res) => send({ id: req.id, ok: true, ...res }))
    .catch((err) => send({ id: req.id, ok: false, error: String(err?.message || err) }));
});
rl.on("close", () => process.exit(0));
send({ event: "host.ready", data: { pid: process.pid } });
