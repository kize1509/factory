# Model access through JetBrains Central (wire)

How the factory reaches Claude and GPT models on this machine, and how it tracks cost live for every job and agent.

## 1. What's installed

`central` (JetBrains Central CLI 2.1.0) runs a local proxy on `127.0.0.1:19516`, signed in to JetBrains Air. There are no API keys on the machine. Agents send requests to the proxy, and it adds the real credentials and forwards them to `api.jetbrains.ai`.

Each agent gets its own "wire" URL:

```
http://127.0.0.1:19516/wire/<wire-id>/<agent>/<provider>
```

What `central add` set up on this machine:

| Agent | Where | Base URL |
|---|---|---|
| Claude Code | `~/.claude/settings.json`, `env.ANTHROPIC_BASE_URL` | `.../wire/<id>/claude-code/anthropic` |
| Codex | `~/.codex/config.toml`, `model_providers.wire` with `wire_api = "responses"` | `.../wire/<id>/codex/openai/v1` |
| Pi | `~/.pi/agent/extensions/jetbrains-central.ts`, which calls `registerProvider` | `.../wire/<id>/pi/anthropic` and `.../wire/<id>/pi/openai/v1` |

The API key is the placeholder `wire-proxy`, and the proxy ignores it. The `<agent>` part of the path tells Central which agent sent the request.

The `wire_YYYY-MM-DD.log` files in `~/.jetbrains-central/logs/` are the proxy's own daemon log: startup, token refresh and upstream errors. They don't contain per-request tokens or cost, so the factory can't track spend from them.

`central log` lists requests (path, status, latency) without token counts. `central limit` shows the monthly credit balance (208 of 2000 credits used this month).

## 2. Models available through the wire

`GET /v1/models` on the wire returns these (checked 2026-10-30):

| Family | Newest |
|---|---|
| Claude | `claude-fable-5-1`, `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5` |
| GPT | `gpt-6.1-sol`, `gpt-6.1-terra`, `gpt-6-luna`, `gpt-6-astra`, and the `gpt-5.6-*` family |

There's a newer GPT generation than 5.6. Codex on this machine already defaults to `gpt-6.1-sol`. Because model lists change often, the admin panel reads them from the wire instead of hardcoding them.

Default tiers:

| Tier | Claude | GPT |
|---|---|---|
| think | `claude-opus-5-5` | `gpt-6.1-sol` |
| code | `claude-sonnet-5-5` | `gpt-6.1-terra` |
| execute | `claude-haiku-5-5` | `gpt-6-luna` (there's no 6.1 Luna yet) |

## 3. Design: a metering proxy in the engine

Every model request from every agent goes through a small reverse proxy inside the Go engine, and from there to the Central wire:

```
 agent (pi / claude / codex)
     │  base URL = http://127.0.0.1:<engine>/llm/<job>/<agent-id>/<provider>
     ▼
 Go engine meter ── reads usage from the stream, emits cost events,
     │              can hold or refuse requests
     ▼
 http://127.0.0.1:19516/wire/<wire-id>/<agent-kind>/<provider>
     ▼
 JetBrains Air
```

Every agent request carries the job and agent in its URL path, so the meter knows exactly who spent what. Nothing has to be read back from Central's logs.

### 3.1 How each runtime is pointed at the meter

The factory never edits your global agent config. Each agent process gets its settings only for that run.

| Runtime | How |
|---|---|
| Pi (sidecar, SDK) | The sidecar starts the Pi SDK with its own agent directory (`.factory/pi-agent/`), so your personal extensions, including the Central one, don't load. It calls `registerProvider("anthropic", …)` and `registerProvider("openai", …)` with meter URLs and `apiKey: "wire-proxy"`. Pi can then use both Claude and GPT models in one session. |
| Claude Code | Env for that process only: `ANTHROPIC_BASE_URL=<meter>/anthropic`. |
| Codex | Overrides for that run only: `codex exec -c model_provider=factory -c model_providers.factory.base_url=<meter>/openai/v1 -c model_providers.factory.wire_api=responses -m <model>`. |

The meter forwards Pi traffic to `/wire/<id>/pi/…`, Claude Code traffic to `/wire/<id>/claude-code/…` and Codex traffic to `/wire/<id>/codex/…`, so Central's own per-agent numbers still add up.

### 3.2 Finding the wire

On start, the engine:

1. Uses the gateway URL from the admin panel if it's set.
2. Otherwise reads the wire ID from Claude Code's `ANTHROPIC_BASE_URL`, then from Codex's `model_providers.wire.base_url`.
3. Runs `central proxy start --ensure-updated --caller agent` (the same command the Pi extension uses) and checks `central status` until the proxy is up.
4. Calls `/v1/models` on both providers to fill the model catalogue.

If any of this fails, a card in the Inbox says what's wrong and suggests `central repair`.

### 3.3 Keeping it general

The wire is one "gateway" type. The meter's upstream is configurable in the admin panel, so the same design works with direct API keys or another proxy later:

```yaml
# ~/.factory/gateway.yml (written by the admin panel)
kind: jetbrains-central
base: http://127.0.0.1:19516/wire/<wire-id>
paths:
  anthropic: "{base}/{agent}/anthropic"
  openai:    "{base}/{agent}/openai/v1"
```

## 4. Live cost tracking

### 4.1 Reading usage

The meter passes every response through as it arrives, with no buffering, and reads the usage fields on the way:

| Provider | Where usage is |
|---|---|
| Anthropic messages (SSE) | `message_start.message.usage`: input, cache write and cache read tokens. `message_delta.usage.output_tokens`: output tokens. |
| OpenAI responses (SSE) | `response.completed.response.usage`: `input_tokens`, `input_tokens_details.cached_tokens`, `output_tokens`, `output_tokens_details.reasoning_tokens`. |
| Non-streaming | the `usage` object in the JSON body |

While output is still streaming, the meter counts output deltas, so the counter on the canvas moves during long answers. When the final usage arrives, it replaces the estimate.

Cost is tokens × the price for that model in the admin panel, split into input, cached input, cache write and output, and always shown in US dollars. Central bills in credits, so the admin panel also shows the credit balance from `central limit` (checked every minute) next to the dollar numbers.

The runtimes also report usage in their own event streams (Pi SDK message usage, Claude's `total_cost_usd`, Codex token counts). The engine compares those with the meter's numbers and flags a large gap in the admin panel's Usage page.

### 4.2 On the canvas

- Each portrait has a live cost counter. When limits are on, a small bar shows how much of the limit is used.
- The top bar shows the open job's total and its burn rate (cost per minute).
- The agent drawer shows a cost graph over time, the cost of each request, the model used, and cache hit rate.
- In the multi-job map, each city shows its job's total.

### 4.3 Stepping in

The meter sits on every request, so the engine can control any agent the same way, whatever runtime it uses:

| Action | What happens |
|---|---|
| Pause | The meter holds the agent's next model request until you resume. The agent just waits. |
| Stop | The engine ends the agent's process (Pi SDK abort, SIGINT for Claude Code and Codex) and marks it stopped. |
| Raise limit | Only when limits are on. Changes the limit, and any held request goes through. |
| Switch model | Stops the agent and restarts it on another model with a summary of its work so far. |
| Message | Adds your text to the agent's next turn. |

There are no budgets by default (PRD D34), so real usage can be measured first. Limits can be turned on later in the admin panel. When a limit is on, the engine posts a warning to the Inbox at 80%. At 100% the meter holds that agent's requests, which pauses it, and an Inbox card asks you to raise the limit, switch model or stop.

## 5. Open points

- Central may have a direct command that prints the wire ID. If it does, the engine should use it instead of reading other agents' config.
- Prices per model aren't exposed by the wire. They're entered in the admin panel, and the credit balance from `central limit` is the source of truth for what was really spent.
- Check how the wire handles many parallel requests from one machine (rate limits), before running several jobs at once.
