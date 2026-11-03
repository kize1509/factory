# Testing and eval harness

Status: design, with the first slice built (cassettes at the Agent seam). Ticket HF-3.

Two goals drive everything here, and HF-8's patrol agents are the main user:

1. **Find bugs.** Start an app, run realistic scenarios, get pass or fail with evidence. A failure is already a bug report.
2. **Measure performance.** Both the app (speed, resources) and the factory (quality, cost, time, how often a human steps in), compared across setups and over time.

## The idea in this branch: cassettes at the Agent seam

Every agent, on every runtime, is a `runtime.Agent`. The job only talks to it through `Prompt(text) -> reply`, plus the tool calls the agent makes back into the `Job` (`SaveHandoff`, `SetChecks`, `SetVerdict`, `Name`, `AskBuilder`, `ProposeExperiments`, `Grade`) and the files it leaves in the worktree. So one recording point covers Pi, Claude, Codex and scripted agents alike.

A **cassette** is `.factory/jobs/<job>/cassette/<agent>/<n>.json`, one file per `Prompt` call:

| field | what it holds |
|---|---|
| `prompt`, `reply`, `error` | the turn's input and output |
| `calls` | factory tool calls made during the turn, in order |
| `events` | everything on the bus during the turn (runtime events are re-emitted on replay, the engine makes its own again) |
| `tree_before`, `tree_after`, `diff` | worktree tree hashes and `git diff --binary` between them |

The diff is between two tree snapshots taken with a temporary git index, so it never touches the real index and it is exactly what this turn changed. That makes each turn apply on its own, in order.

Pieces (all in `internal/harness/`):

- `record.Runtime` wraps any `runtime.Runtime`. `Attach(job)` sets `Job.Tap`, a hook that reports tool calls as they arrive.
- `replay.Runtime` loads a cassette. Each `Prompt` checks the prompt, re-emits runtime events, runs `git apply`, re-issues the tool calls through the job, and returns the recorded reply.
- `fixture.New(t, name)` copies `testdata/fixtures/<name>/` into a temp git repo with a fixed commit.
- Machine paths (the repo folder) are stored as `{{repo}}` so a cassette recorded in one temp folder replays in another.

**Drift fails loudly.** If the engine's prompt differs from the recording, replay returns a `*replay.Drift` error and the job gets stuck with a line diff (`-` recorded, `+` now). That is the signal that a prompt, role or engine change altered behaviour. It is also how you bisect: replay the same cassette on two commits and the first one that drifts is the one that changed the conversation.

What replay proves: the engine, given the same agent behaviour, produces the same events, tree, commit and handoff. What it cannot prove: that a model would behave this way under the new prompt. That is what evals (Part 4) and live runs (Part 2) are for.

Known limits of this first slice:

- `ask_human` is not recorded. A job that waits on a person can't replay without an answer; record the answer as a guidance turn later.
- Parallel Symposium experiments share one bus, so event slices per turn can interleave. Tool calls and diffs stay correct (they are per agent and per worktree).
- Runtime events are told apart from engine events by type prefix (`agent.`, `tool.`, `message.`, `pi.`, `claude.`, `codex.`; `agent.retry` is the engine's). A new runtime event family needs adding to `record.IsRuntimeEvent`.
- Timing and cost in a cassette are what was recorded. Replay is for behaviour, not for speed or spend numbers.

## Part 1: run and reproduce

**Bring-up.** `harness.Up(project, opts)` (CLI: `factory test up <project>`) does:

1. make a clean copy of the project at a known commit (fixtures come from `testdata/fixtures/<name>/`, or a repo URL plus commit);
2. load fixture data (a `testdata/seed/` script per project, run in the container);
3. start the e2e container from PRD §11 on free ports and wait for its health check;
4. return a handle: base URL, ports, container ID, run folder, and a `Down()`.

It is idempotent: the same call on the same inputs gives the same state. Everything it starts is named after the run so `factory test down <run>` removes it.

**Fake runtime and replay.** Built here (above). Next: a *recorded gateway layer*, the same idea one level down. It records the model gateway's request and response per call so replay also works for runtime internals, not only at the Agent seam. It matters once we test the runtimes themselves (retries, token counting); the Agent seam covers job-level tests.

**Evidence.** Each run has a folder `.factory/runs/<run>/` that holds: `events.jsonl` (copy), `cassette/`, container and app logs, browser screenshots and video (Playwright), console and network errors, `metrics.json`, and `result.json`. `result.json` links to all of it by relative path. A failing scenario's bug report is built from `result.json` plus the cassette: steps, expected vs actual, evidence links, replay command.

## Part 2: end-to-end tests

**Job e2e (built).** `internal/harness/e2e_test.go` records a scripted job on the `hello` fixture and replays it. Asserts on event types (`events.jsonl`), the job branch's tree hash, the commit message, the handoff note and the final job state. Failure paths covered: checks that keep failing (stuck), an agent that never writes a handoff (stuck after two reminders), prompt drift.

To add next, same pattern: budget limit, a paused agent (`Steer` during a turn), an agent that stops early (empty reply, no change), review rounds (verdict blocks, then approves), Symposium, ticket update through the tracker fake.

**Live runs.** A handful of scenarios run against real Pi, Claude and Codex, behind `FACTORY_LIVE=1` and `go test -run Live -tags live`. Never in CI. Each live run is recorded as a cassette, so a good live run becomes a free replay test afterwards. This is how cassettes get refreshed.

**Per-feature scenarios.** A scenario is a file `scenarios/<feature-id>/<name>.yaml` tied to an entry in the project's feature map (HF-8):

```yaml
feature: invoices.export
setup: seed/invoices.sql
steps:
  - open: /invoices
  - click: "Export CSV"
  - expect: { download: "invoices.csv", rows: ">= 3" }
perf: { page_load_ms: 1500 }
```

Patrol agents run scenarios, record the run, and where the project allows it write new ones. When a bug is fixed, the scenario that found it is attached to the ticket as its regression check (`ticket.regression = scenario id`), and the job's checks run it.

## Part 3: performance

**App performance**, recorded in `metrics.json` on every run: page load (navigation timing), API latency (p50/p95 per route), job wall time, container memory and CPU (sampled every second). Compared with a stored baseline; a regression past the threshold becomes a finding and is filed like a bug (with the run folder as evidence).

**Factory performance**, from PRD §16, computed from `events.jsonl` after each job:

- share of tasks that pass with no human edits;
- heal loop count (`checks.failed` events);
- cost and wall time per task (sum of gateway usage; first to last event);
- how often a done ticket is sent back.

HF-8 adds patrol metrics: bugs found, false positives, duplicates, cost per accepted finding, human interventions per pass.

**Storage.** One row per run in SQLite (`runs` and `run_metrics` tables, next to `events`), keyed by run ID, setup ID (model, runtime, prompt version, strategy) and commit. The admin Metrics tab reads the same tables. Trends need nothing more than a `GROUP BY` over time.

## Part 4: agent evals

An **eval task** is a folder: `evals/<id>/{repo/ or repo.ref, ticket.md, hidden/, expected.yaml}`. `hidden/` holds checks the agent never sees; the runner copies them in only after the agent finishes.

- **Fix tasks:** fixture repo plus ticket plus hidden checks. Metrics: hidden checks pass, reviewer agreement with them, plus the factory metrics above.
- **Bug-finding tasks:** a fixture app with seeded bugs (broken feature, regression, a11y issue, flaky test, slow endpoint), listed in `expected.yaml` with how to recognise each. A patrol pass is scored on recall, false-positive rate, duplicates and cost per bug found.
- **Real bugs become eval tasks.** When a bug ticket closes, a hook builds `evals/<ticket>/`: repo at the parent of the fix commit, the ticket text, and the fix's tests as hidden checks. No manual step.
- **Comparison runner.** `harness.Compare(tasks, A, B)` (CLI: `factory test compare`) runs two setups on the same tasks, N times each, and reports per-metric difference with the spread, so noise is visible. A setup is `{model, runtime, prompt version, patrol strategy}`.

Replay and evals fit together: an eval run records cassettes, so any surprising result can be replayed for free and inspected.

## The interface HF-8 patrol agents call

Go functions in `internal/harness`; each has a CLI verb that prints JSON (`--json`) so agents in any runtime can call it as a shell command.

```go
// Bring-up: clean, known state. Returns a handle.
func Up(ctx context.Context, project string, o UpOptions) (*Env, error) // factory test up <project> [--commit X] [--fixture N]
func (e *Env) Down() error                                            // factory test down <run>

// Run one scenario against a running env; records evidence.
func RunScenario(ctx context.Context, e *Env, scenario string) (*Result, error) // factory test run <scenario> [--env <run>]

// Replay a recorded run (a cassette) to reproduce or bisect.
func Replay(ctx context.Context, run string, o ReplayOptions) (*Result, error) // factory test replay <run> [--at <commit>]

// Read results and metrics.
func Results(run string) (*Result, error)                       // factory test results <run>
func Metrics(q MetricsQuery) ([]Row, error)                     // factory test metrics --setup X --since 7d
func Compare(tasks []string, a, b Setup, n int) (*Report, error) // factory test compare --a X --b Y
```

`Result` is the same for scenario runs, replays and evals: `{run, pass, steps[], failure{step, expected, actual}, evidence{events, cassette, logs, screenshots, video, console, network}, metrics{}, replay_cmd}`. A patrol agent that gets `pass=false` can file `failure` and `evidence` as a ticket with no further work, and `replay_cmd` lets the fixer reproduce it.

Bisecting with it: `factory test replay <run> --at <commit>` for a range of commits; the first one whose result differs (or that reports drift) is the culprit.

## Answers to the open questions

- **First eval tasks:** a mix. Start with 5 hand-written fix tasks on small fixtures (they cost nothing to get right), 5 seeded-bug apps for patrol, then add past tickets with clean fix commits. HF-8 findings feed the set from then on.
- **Live-model spend per eval run:** cap at a fixed budget per run, default 5 USD for the nightly set, 1 USD for a comparison on a handful of tasks, enforced by the gateway's budget limit (the same one jobs use). Anything bigger needs a flag. Replay is the default; live is opt-in.
- **Baselines and thresholds:** baselines live in the same SQLite tables, as the median of the last 10 passing runs on the main branch for the same setup. A regression is more than 20% worse on a metric, and also more than its own run-to-run spread (so a noisy metric doesn't file bugs). Thresholds can be overridden per scenario in `perf:`. Change them once we see real noise.
- **Symposium reuse:** yes. A Symposium is already several setups on one task; `Compare` is the same loop with a hidden-check grader in place of the Grader agent. Run the Symposium's experiments through `Compare` so both share one scoring path. Decide after the runner exists.

## Subtasks

Order matters: Part 1 first, since HF-8 and the e2e tests both depend on it.

1. **HF-3.1 Cassettes at the Agent seam** (this branch): `record`, `replay`, `fixture`, job e2e on the `hello` fixture, failure paths. *Done.*
2. **HF-3.2 Evidence folder and `Result` type:** `.factory/runs/<run>/`, `result.json`, copying events and cassette in, one place that builds it.
3. **HF-3.3 Bring-up:** `Up` / `Down`, e2e container from PRD §11, fixture seeding, free ports, health wait. CLI `factory test up|down`.
4. **HF-3.4 Browser evidence:** Playwright runner with screenshots, video, console and network errors, linked from `Result`.
5. **HF-3.5 More job e2e:** review rounds, budget limit, pause, early stop, Symposium, ticket update; record `ask_human` answers as cassette entries.
6. **HF-3.6 Scenarios:** file format, `RunScenario`, tie to the feature map (needs HF-8's map), regression link on tickets.
7. **HF-3.7 Recorded gateway layer:** record and replay at the model gateway for runtime-level tests.
8. **HF-3.8 Live runs:** Pi, Claude, Codex behind `FACTORY_LIVE`, recorded as cassettes.
9. **HF-3.9 Metrics:** app metrics collection, factory metrics from events, SQLite tables, baselines and regression findings, admin Metrics tab.
10. **HF-3.10 Eval tasks and runner:** task format, hidden checks, fix-task scoring, five hand-written tasks.
11. **HF-3.11 Bug-finding evals:** seeded-bug fixtures, patrol scoring (recall, false positives, duplicates, cost).
12. **HF-3.12 Real bugs become tasks:** the close-ticket hook.
13. **HF-3.13 Comparison runner:** `Compare`, spread reporting, CLI; decide on Symposium reuse.
14. **HF-3.14 `factory test` CLI and HF-8 interface docs:** wire the verbs above to the functions, JSON output.
