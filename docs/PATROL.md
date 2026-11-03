# Patrol passes (HF-8, experiment: every check is a harness run)

A pass is the swarm going over a project on its own. This page covers the part built so far: map, split, patrol, record, with the static and tests techniques. The testing harness ([TESTING.md](TESTING.md)) does every check; the pass only schedules them and keeps the index.

## One format

Every check is a harness run folder, `.factory/runs/<id>/`, with `result.json`, `logs/` and a `replay_cmd`. The ledger is an index over those results, keyed by feature × technique × commit. Nothing else holds evidence, so replay, metrics and later scenarios and perf baselines read the same files. A failing Result is already a finding.

`Result` gained `kind` (`job`, `scenario`, `static`, `tests`), `feature`, `technique` and `commit`. Job runs set `kind: job` and the rest stay empty, so `factory test results <run>` works on both.

## Check

`harness.Check(ctx, root, repo, feature, technique, opts)`, shell: `factory test check <repo> --feature X --technique static|tests [--commit C] [--cmd CMD ...] [--root D] [--json]`. Exit 0 pass, 1 fail, 2 could not run.

It adds a detached git worktree of the repo at a fixed commit (HEAD unless `--commit`), runs the technique's commands there, and removes the worktree. The commands default from the repo: Go gets `go vet ./...` and `go test ./...`, Node gets `npm run lint` and `npm test`. `--cmd` replaces them. One log per command; the first non-zero exit becomes `failure{step, expected: "exit 0", actual: "exit N" plus the last 40 lines}`. `metrics` has `wall_ms`, per-command `cmdN_ms` and `cost_usd`. `replay_cmd` is the same check on the same commit.

## Pass

`internal/patrol`. `Pass.Run`:

1. **Map.** `MapFeatures` makes one feature per source folder (`root` for the top). A surveyor agent can add routes, screens and commands later.
2. **Split.** Cells of feature × technique, never-checked first, then least recently checked (from the ledger). `Budget.Checks` caps the count.
3. **Patrol.** `Budget.Agents` workers take cells. Each agent gets `PromptFor(task)`: run `factory test check`, read the Result and logs, call `record_check(run, verdict, note)`.
4. **Record.** `record_check` checks that the run belongs to the agent's cell and commit, and that a failed run is not recorded `ok`. After the agent returns, the pass writes a `runs` row and a `ledger` row. An agent that recorded nothing gets a `blocked` row, so a cell is never silently lost.

Per D2 the agent only sends requests: `record_check` is an engine tool (MCP and `/api/tools/pass-<id>/<agent>/record_check`).

## Budget and kill switch

The pass context carries the time budget. Before handing out a cell, and before every `Task.Check`, the pass checks for stop, USD spent and the deadline. `Pass.Stop` cancels the context, which stops runtime agents (and their shell child). A stopped or over-budget pass still returns a summary; cells not reached go into `next`. USD is counted when an agent returns, so up to `Agents` in-flight checks can overshoot the USD budget by their own cost.

## Storage and API

SQLite tables next to `events`: `runs` (id, setup, commit, kind, feature, technique, pass, wall_ms, cost_usd, dir), `passes` (state, summary JSON) and `ledger` (pass, feature, technique, agent, commit, run, verdict, note).

- `POST /api/passes` `{repo, runtime, usd, minutes, agents, checks, techniques}` starts a pass.
- `GET /api/passes/{id}` and `GET /api/passes/{id}/ledger` (rows with `run`, `run_dir`, `verdict`, `wall_ms`, `cost_usd`).
- `POST /api/passes/{id}/stop`, `POST /api/passes/stop-all`.

## Not built yet

Control levels, triage and dedup, filing, fix jobs, milestones, the explore and review techniques, `factory pass`, scheduling, the canvas place and the Metrics tab. The ledger's `verdict` is the agent's; triage will add the second reproduction by running the row's `replay_cmd`.
