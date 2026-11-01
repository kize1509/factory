# Factory harness PRD (draft v0.4)

Status: brainstorm. Settled points are in the decisions table in §5. Open questions are at the end.

## 1. Summary

Factory is a Go engine with a minimal web interface. You give it YouTrack task IDs, and it hands each one to a team of headless coding agents running on Pi, Claude and Codex. The agents research, plan, build and review in fixed roles. The loop repeats until the checks the user picked pass and the reviewers sign off. Then the work is committed, pushed or opened as a PR, depending on the project.

A web canvas set in ancient Greece shows each agent as a philosopher. You can see where each one is, what it's doing, what it says to the others, and the screenshots and videos it uploads.

## 2. Problem

Coding agents handle a single session well. Nobody runs the whole loop for them: read the ticket, research, plan, build, check, review, ship and report back.

Pi, Claude Code and Codex each have their own CLI and event format, so there is no shared way to run them side by side or watch them.

An agent decides on its own when it's finished. Nothing outside the agent checks whether the work does what the ticket asked, or whether it only matches the wording of the prompt.

Agents that run unattended leave no record a person can follow afterwards.

## 3. Goals

1. A self-healing loop that runs against checks the user approved for the job.
2. Agents in roles (research, plan, build, review, cleanup), with several agents per role when the work allows it.
3. Pi, Claude and Codex behind one interface, all headless.
4. YouTrack first, with other trackers added through the same adapter.
5. A live canvas showing every agent, its current step, its messages, its diagrams and its media.
6. Output a person can read easily: simple code, plain prompts and short dev docs, with no scaffolding left behind.

## 4. Non-goals

- Agents don't write tests on their own. A new check is built only when the user asks for it.
- We drive existing agents. We don't build a new agent framework.
- We call the project's own tooling and don't replace CI/CD.
- Local and self-hosted first. No hosted service.

## 5. Decisions

| # | Decision |
|---|---|
| D1 | The Go engine does all the legwork: scheduling, process control, git, tracker calls, the event log and the web server. |
| D2 | The Go engine is the only control plane. Every trigger, timer, webhook, process start and stop, retry, phase change, message and upload passes through it. Agents, including the scheduler agent, send requests. The engine decides and acts. |
| D3 | No new tests by default. When a job starts, agents list every existing way to check the result. The user picks which ones to use, picks none, or asks for a new check built for this job. |
| D4 | Ship mode is set per project: `commit`, `push` or `pr`. The default is `pr` (commit, push and open a PR). |
| D5 | Plan approval happens only on request. The user gives a complexity hint at the start. If the task is complex and the user wants to iterate on the plan, the engine waits for approval. |
| D6 | Each role can run several agents. All agents run headless with permission prompts turned off. The logs are the source of truth. |
| D7 | Every prompt and every doc the factory writes goes through the humanizer skill (`~/.agents/skills/humanizer`). |
| D8 | At finalize, working docs are removed from the repo (a copy stays in `.factory/`). Only short dev-facing docs remain. |
| D9 | When a ticket is done, it moves to the review state and gets a summary comment. |
| D10 | Pi runs through its SDK, inside a small Node sidecar that the engine starts and controls. |
| D11 | Agents are ancient Greek philosophers. Each role has a fixed set of names, and each character keeps a history across jobs. The canvas is set in ancient Greece. |
| D12 | The canvas is pixel art: a pixel-art Greek landscape, pixel portraits of the philosophers, and pixel icons for status, runtimes and media. Agents are round portrait tokens that glide between places. Activity shows as speech bubbles, rings and message lines. Text (names, bubbles, labels) is real text over the canvas, so it stays readable at any size. |
| D13 | Agents upload e2e videos and screenshots. The canvas shows them, and they get attached to the PR and the ticket. |
| D14 | All data stays local in `.factory/` inside the project folder. `factory init` adds it to `.gitignore`. |
| D15 | Each job gets its own worktree and branch, so jobs can run in parallel. |
| D16 | The Symposium (experiment branches) opens when the planner sees more than one workable approach, or when errors keep coming back during build. |
| D17 | The engine picks the Symposium winner with a fixed formula over check results and reviewer grades. Given the same inputs, it always picks the same winner. |
| D18 | The number of parallel jobs is configurable. Jobs running at the same time share a channel where each one announces what it will touch and when it acts. |
| D19 | Isolation: every job and experiment gets a git worktree. E2e checks run in a container, using Apple's `container` tool first. |
| D20 | The factory never picks up tickets by itself. Work starts only when the user gives it task IDs. |
| D21 | The web interface is the only place for input: starting tasks, picking checks, approving plans, answering agents and changing settings. It stays minimal. |
| D22 | The YouTrack URL and token are set once in the factory env. After that the user only enters task IDs. |
| D23 | Check plan: if the project has a saved plan, the job uses it without asking. If not, the job asks in the web Inbox and waits. |
| D24 | The factory builds its own e2e image for each project. The image and its Containerfile live in `.factory/` and are never committed. |
| D25 | Jobs that touch the same area both keep going. They sort it out at merge time, and the Assembly tells them when that's needed. |
| D26 | Agents don't modify test files by default. A job may touch tests only when the task is about tests and the user allowed it at Start (§7.5). |
| D27 | Symposium weights default to 0.6 for checks and 0.4 for review. |
| D28 | Each repo gets a committed `factory.yml`. The YouTrack URL and token stay in the factory env, outside the repo. |
| D29 | Models by kind of work. Planning, hypotheses and scheduling use Opus or GPT-5.6 Sol. Coding uses Sonnet or GPT-5.6 Terra. Plain execution of a detailed plan uses Haiku or GPT-5.6 Luna. Reviewers are on the think tier, researchers and the humanizer on the code tier, the finalizer on the execute tier. |
| D30 | All art is drawn in code, ahead of time, as a fixed catalogue (`docs/ART_CATALOGUE.md`, source in `art/`). Agents never make art while working. The canvas picks a prepared scene setup that matches the work being done. |
| D31 | Runtimes and models are managed in the admin panel, not in code or `factory.yml`. Claude Code runs only Claude models, Codex runs only GPT models, and Pi runs both. Pi is the primary runtime and the default for every role, on the Claude model of the role's tier. |
| D32 | All model traffic goes through JetBrains Central's local wire proxy (no API keys). The engine runs a metering proxy in front of it. See `docs/WIRE.md`. |
| D33 | Cost is tracked live in US dollars for every job and every agent and shown on the canvas. You can pause, stop or switch model at any moment. |
| D36 | The engine tracks every process and container pod an agent starts: command, pid or container id, port, CPU, memory, run time and exit code. You can see them per agent and across all cities, and stop any of them. |
| D37 | An agent that needs you can't be missed: it waves, its bubble pulses with a "Needs you" tag, its city's chip pulses, and on the Aegean its city sends up a beacon. Clicking it and answering sends the reply by UFO, and it goes back to work. |
| D38 | Information passes between agents only through handoff notes with a fixed shape (§7.6). The engine checks every note. If one is missing or incomplete, the engine sends the same agent back, in the same session, to write it. A note with open questions stops the job until they are answered. |
| D34 | No budgets or limits by default, so real usage can be measured first. Limits can be turned on later in the admin panel. The admin panel has a Metrics tab that records everything: agent communication, tokens, cost, time and movement through the pipeline. |
| D35 | Clicking an agent always shows its live internal thoughts. You can send it information from there at any moment, which interrupts its current step. A UFO flies in and beams the message down, so you can see on the map who just got new input. |

## 6. Concepts

| Concept | Meaning |
|---|---|
| Ticket | A task ID entered in the web interface (or a plain prompt), with a description, dependencies and success criteria. |
| Job | One ticket in progress, with its own repo, worktree, branch, complexity hint, ship mode and budget. |
| Check | One way to confirm the work: a test suite, e2e run, typecheck, lint, build, app smoke run or project script. |
| Check plan | The checks the user picked for a job. Agents can't change it. |
| Phase | One stage of the pipeline: survey, research, plan, build, check, review, finalize. |
| Role | The job an agent has: scheduler, researcher, planner, builder, reviewer, humanizer, finalizer. |
| Agent | One headless process (pi, claude or codex) playing a role, shown as a philosopher. |
| Board | Shared space inside a job where agents post messages, notes and diagrams. |
| Assembly | Shared channel between jobs running at the same time. |
| Symposium | A set of experiment branches inside a job, each testing one idea, scored and compared. |
| Event | One record in the log. The web interface and replay read events. |

## 7. Pipeline

```
TICKET + complexity hint
   │
   ▼
0. SURVEY        agents look for every way to check the result
   │             (tests, e2e, typecheck, lint, build, run the app, scripts)
   ▼
   USER PICKS    which checks to use, none, or "build a new one for this"
   │             (plan approval gate requested here if wanted)
   ▼
   SCHEDULER     Solon splits the work and asks the engine to start agents
   │
   ▼
1. RESEARCH      ticket, dependencies, docs, related work, working tree
   ▼
2. PLAN          working docs + diagrams ── more than one approach? ──► SYMPOSIUM
   ▼
3. BUILD ◄───────────────────────┐
   ▼                             │
4. CHECK   fail ─► diagnose ─────┘  same errors keep coming? ──► SYMPOSIUM
   │ pass
   ▼
5. REVIEW        blocking findings go back to BUILD
   ▼
6. FINALIZE      humanizer, docs cleanup, commit / push / PR, ticket to review
```

### 7.1 Survey and check plan

Before any real work, researchers look at the repo and list every way the result could be checked. Each entry says what it runs, what it covers, roughly how long it takes, and whether it looks flaky or broken. Typical findings are a test suite, an e2e suite, typecheck, lint, a build, starting the app and probing it, and project scripts.

If the project has a saved check plan, the job uses it and moves on (D23). Otherwise the list goes to the Inbox in the web interface and the job waits. The user can:

- pick any subset of the listed checks
- pick none, in which case the job is judged by review only
- ask for a new check built for this job

A new check is built first, as its own small step, and the user approves it. After that it's frozen like the other checks. Reviewers flag any change an agent makes to a check file.

### 7.2 Scheduler

Solon, the scheduler agent, reads the check plan and the research. It decides how many agents each role needs, splits the build into subtasks when they can run in parallel, and decides when a Symposium should open. It doesn't start anything itself. It sends requests, and the engine carries them out within the job's limits (D2).

### 7.3 Symposium

A Symposium opens when one of these happens:

- the planner finds two or more workable approaches
- the same check fails the same way twice in a row
- the build loop fails `n` times in total (configurable)

```
              job branch  factory/ABC-123
                    │
     ┌──────────────┼──────────────┐       propose_experiment(hypothesis)
     ▼              ▼              ▼       the engine creates a branch + worktree for each
 exp/ABC-123/1  exp/ABC-123/2  exp/ABC-123/3
 "cache layer"  "fix query"    "batch writes"
     │              │              │       one builder per branch
   checks         checks         checks    same check plan, same base commit
     │              │              │
   grades         grades         grades    the same reviewers grade every branch
     └──────────────┼──────────────┘
                    ▼
            engine computes scores, picks the winner
                    ▼
   winner merged into the job branch, the rest archived in .factory/
```

The engine scores each experiment in three steps:

1. Disqualify any experiment that fails a required check, edits a check file or doesn't build.
2. Score the rest: `score = w_checks × check_pass_rate + w_review × average_grade`. Each reviewer grades every experiment from 1 to 5 on its own focus, using a fixed rubric and returning structured output. Grades are normalized to 0..1. Reviewers don't see which agent built which branch.
3. If scores tie, pick the smaller diff in changed lines, then the one with fewer files changed, then the one that finished first.

If the check plan is empty, the score comes from grades alone. The weights live in config. Reviewer grades come from a model, so they can vary between runs. The counting is fixed: given the same results and grades, the engine always picks the same winner.

Losing branches are deleted. Their diffs, results, grades and debate notes stay in `.factory/`, so later runs can see what already failed.

Limits: maximum open experiments per job, maximum cost and time per experiment, and whether an experiment can open its own Symposium.

### 7.4 Parallel jobs and the Assembly

The number of jobs running at once is set in config. Jobs talk through the Assembly, a channel between jobs that the engine runs:

- Each job announces the areas it plans to touch (paths or modules) after planning.
- The engine tracks these areas. When two jobs overlap, both get told who else is there and both keep working (D25). Nobody waits.
- A job announces when it pushes or merges. Any other job that overlaps rebases onto that change. If the rebase has conflicts, a builder in that job resolves them, and the job runs its checks again before it can ship.
- If a job's builder can't resolve the conflict, the engine posts the conflict to the Assembly, and builders from both jobs work it out together on the canvas. If that still fails, it goes to the Inbox.

### 7.5 Test files

By default, test files are protected. The engine (not the agents) enforces this:

- During the survey the engine records which paths are test files: test folders, files matching the framework's patterns (`*_test.go`, `*.spec.ts`, `tests/`), e2e specs, fixtures and snapshots.
- After every build loop the engine checks the diff. If a protected path changed, the engine reverts that change and tells the builder why. It shows up on the canvas as a blocked write.
- Reviewers still look for the indirect ways around this, such as changing test config, skip flags, or environment variables that turn tests off.

Some tasks are about tests, like "fix the flaky login test" or "update the snapshots after the redesign". For those, the Start form has a "this task works on tests" option. When it's set:

- The planner lists the exact test files the task needs to touch, and the user approves that list in the Inbox.
- Only those files open up. Every other test file stays protected.
- Reviewers get one more focus: does each test change match the task, and does no test get weaker without a reason?

If the planner decides mid-job that a test file has to change and the option isn't set, it asks in the Inbox instead of making the change.

### 7.6 Handoffs

Agents don't share memory. Each role runs in its own session, so the only thing that reaches the next agent is what the previous one wrote down. Every phase ends with a handoff note, and the next agent's prompt is built from that note and the files it points to.

The note is written with the `handoff(note)` tool and saved as `.factory/jobs/<job>/handoff/<nn>-<phase>.yaml`:

```yaml
from: aristotle (planner)
to: builders
did: Split the fix into two subtasks.
decisions:
  - Retry with backoff instead of a longer timeout (timeout is already 30s)
findings:
  - Retry helper already exists in net/retry.go
files: [src/auth/session.go, src/config/auth.ts]
open_questions: []
next: Archimedes wraps the session call in withRetry(); executor moves 30000 to config.
evidence: [docs/plan.md, diagrams/login-flow.mmd]
```

Fields:

| Field | Required | Meaning |
|---|---|---|
| `from`, `to` | yes | who wrote it and which role or agent it is for |
| `did` | yes | what this phase did, in a sentence or two |
| `decisions` | yes, may be empty | choices made, each with its reason |
| `findings` | yes, may be empty | facts the next agent needs and would not find quickly |
| `files` | yes, may be empty | files read or changed that matter for the next step |
| `open_questions` | yes, may be empty | anything still unknown that blocks the next step |
| `next` | yes | the concrete first step for whoever comes next |
| `evidence` | no | paths to docs, diagrams, check results or media |

How the engine enforces it:

1. When an agent finishes its phase, the engine looks for its handoff note.
2. It checks the note: valid YAML, every required field present, `did` and `next` not empty, every path in `files` and `evidence` exists in the worktree or `.factory/`.
3. If the note is missing or fails a check, the engine resumes the same agent in the same session with a short prompt: "Your handoff is missing or incomplete: <what is wrong>. Write it with the handoff tool." The agent still has its full context, so it can write an accurate note.
4. It tries this up to 2 times. If the note is still not valid, the job pauses and an Inbox card asks you to write or fix it.
5. If `open_questions` is not empty, the next phase doesn't start. The questions go to the agent who can answer them (named in the question) or to you, and the answers are added to the note.
6. Every step is an event: `handoff.written`, `handoff.missing`, `handoff.rejected` (with the reason), `handoff.rewritten`, `handoff.blocked`. Missing handoffs per role show up in the Metrics tab.

The humanizer pass applies to handoff notes too, so they stay short and plain.

On the map, a handoff is a scroll carried along the Sacred Way from one station to the next. Click it to read the note. When a note is missing, the agent who owes it gets a "Writing my handoff" bubble before the scroll sets off.

### 7.7 Heal limits

Each job has limits on loops per phase, tokens, cost and wall time. The job stops when the same failure repeats past the limit. A retry can switch to a different agent or model. When a job runs out of budget, it hands off to a human with a summary.

## 8. Roles

| Role | Philosophers | What it does | Parallel |
|---|---|---|---|
| Scheduler | Solon | Splits work, sizes the team, opens Symposiums. Only sends requests to the engine. | one per job |
| Researcher | Herodotus, Thales, Anaximander, Eratosthenes | Surveys checks. Reads the ticket, dependencies, docs, related work and code. Posts findings. | yes, split by area |
| Planner | Aristotle, Plato | Turns research into a plan and working docs. Draws diagrams. | usually one |
| Builder | Archimedes, Hero, Euclid, Pythagoras | Implements, runs checks, fixes failures. | yes, by subtask or experiment |
| Reviewer | Socrates (criteria really met), Diogenes (hardcoding and shortcuts), Zeno (edge cases), Heraclitus (simplicity) | Read-only review with one focus each. Also grades Symposium branches. Looks for code shaped to the prompt or to the checks, special cases for check inputs, and edits to check files. | yes, one per focus |
| Humanizer | Protagoras, Isocrates | Runs the humanizer skill on prompts, docs, comments and PR text. | yes |
| Finalizer | Xenophon | Cleans up docs, commits, pushes, opens the PR, updates the ticket. | one |

Each role's prompt is a short markdown file (`prompts/<role>.md`). Config sets which runtime plays which role.

## 9. Agent runtimes

All agents run headless and stream JSON events. The engine turns them into one event format.

| Runtime | How it runs |
|---|---|
| Pi (primary) | Pi SDK inside a Node sidecar (`pi-host`). The engine starts the sidecar and talks to it over JSON on stdio. One sidecar can host many Pi sessions. Runs Claude and GPT models. |
| Claude | `claude -p --output-format stream-json --dangerously-skip-permissions`. Claude models only. |
| Codex | `codex exec --json -m <model>`, with approvals and its sandbox turned off. GPT models only. |

### 9.1 Models

Default catalogue, read from the wire and editable in the admin panel (§12.2). The wire offers a newer GPT generation than 5.6, so the defaults use it:

| Tier | Claude | GPT | Used for |
|---|---|---|---|
| think | `claude-opus-5-5` | `gpt-6.1-sol` | scheduler, planner, Symposium hypotheses, reviewers |
| code | `claude-sonnet-5-5` | `gpt-6.1-terra` | builders, researchers, humanizer |
| execute | `claude-haiku-5-5` | `gpt-6-luna` | executors, finalizer |

Which runtime can run which model:

| Runtime | Claude models | GPT models |
|---|---|---|
| Pi | yes | yes |
| Claude Code | yes | no |
| Codex | no | yes |

The engine refuses a role setting that pairs a runtime with a model it can't run, and the admin panel won't let you save one. Luna does poorly on long context, so the scheduler gives executors only short, self-contained steps.

The engine makes the humanizer skill available to every runtime, by skill directory or by adding it to the prompt.

The engine runs a local MCP server that every agent connects to. Pi gets the same tools through the sidecar. The tools are:

- `board_post(message, to?)` and `board_read()` for talking to other agents in the job
- `draw(diagram)` to post a Mermaid or node/edge diagram to the canvas
- `set_status(step, detail)` to say what the agent is doing now
- `run_checks()` to run the job's check plan and get structured results
- `upload_artifact(path, kind, caption)` for screenshots, e2e videos, traces and reports
- `propose_experiment(hypothesis, success_signal)`, `report_experiment(result)` and `grade(experiment, score, reason)` for the Symposium
- `announce(areas, action)` for the Assembly
- `handoff(note)` to write the phase's handoff note (§7.6)
- `read_ticket()` and `ask_human(question)`

Every tool call is a request to the engine. The engine logs it, checks it against limits and carries it out.

## 10. Web canvas

### 10.1 The world

Each phase is a place in one pixel-art landscape: a coastal Greek city on a hill, with the Harbour at the bottom and Delphi up on the mountain.

| Phase or concept | Place |
|---|---|
| Intake | The Gates |
| Survey and research | Library of Alexandria |
| Scheduler | Council house (Bouleuterion), where Solon sits |
| Plan | Academy |
| Build | Workshop of Hephaestus |
| Check | Oracle of Delphi, which gives the verdict |
| Review | Areopagus, the court |
| Experiments | Symposium |
| Messages inside a job | Agora |
| Ship | Harbour of Piraeus |
| Asking the human | A messenger runs to you |

In the multi-job view each job is a city-state on a map. The Assembly is drawn as roads between the cities, with messengers carrying the announcements.

### 10.1a Art direction

- The scene is drawn at 640×360. It is scaled up by a whole number with nearest-neighbour scaling first, then fitted to the window, so pixels stay sharp at any size.
- The palette is Hellenic: whitewashed marble, Aegean blues, gold, olive and navy ink, with terracotta only on roofs and pottery. Dusk swaps it for sunset colours over a dark Aegean.
- Each place is a building or landmark drawn as one tile set: Gates with columns, the Library with scrolls, the Academy with an olive grove, a glowing forge for the Workshop, the Delphi temple on the mountain, the Areopagus rock, a banquet hall for the Symposium, an open square for the Agora, and ships at the Harbour.
- The scenery barely moves. Water, the forge fire and the Oracle's smoke loop over a few frames. A building lights up while any agent is working in it.
- Portraits are 32×32 pixel busts in a round bronze frame, one per philosopher, each recognisable at a glance (Socrates bald and bearded, Diogenes with his lamp, Archimedes with a compass, and so on).
- Icons are 16×16 pixel art for status (thinking, tool, waiting, blocked, done, failed), runtime (pi, claude, codex), media (video, screenshot, trace, report) and Inbox items.
- Speech bubbles, captions and panels use a pixel font for headings and a plain readable font for text and code, so transcripts and diffs stay easy to read.
- The art is made for the project and kept in the factory's own repo (`web/assets/`), not loaded from outside.

### 10.2 Agents as figures

Each agent is a round pixel portrait (a bust in a circle) labeled with its name and role, such as "Diogenes, Reviewer". A small mark on the rim shows the runtime. The portrait sits at its current place and jumps to the next place when its phase changes.

A ring around the portrait shows the status: thinking, running a tool, waiting, blocked, done or failed. A one-line caption says what it's doing, for example "running `make test`" or "reading `auth/session.go`". A small counter shows tokens and cost. When agents talk, the sender gets a speech bubble and an arrow points to the receiver.

### 10.3 Agent detail panel

Clicking a portrait opens a panel with:

- the current step, the current tool call and the elapsed time
- Thoughts: the agent's live internal reasoning, streamed as it happens, always visible at the top of the panel
- a box to send the agent information. Sending interrupts its current step, and the message and the agent's reply both appear in the thoughts stream
- a live transcript of messages and tool calls
- files read and changed, with live diffs
- messages sent and received
- diagrams it posted
- screenshots, videos and traces it uploaded
- controls to pause, stop, send a message or restart on another model, all routed through the engine

### 10.3a Thoughts and interrupts per runtime

| Runtime | Thoughts | Interrupt with new information |
|---|---|---|
| Pi (SDK) | thinking and text events from the session | Pi's steering message, delivered before the agent's next step |
| Claude Code | thinking blocks in `stream-json` output | a user message on stdin with `--input-format stream-json` |
| Codex | reasoning summaries in `--json` events (GPT models show summaries, not full reasoning) | stop the run and resume the same session with the message |

The exact mechanics for Claude Code and Codex need checking against their current CLIs before M1.

### 10.3b Map behaviour

- The world is 1280×720 with the city in the middle. It always fills the view, and you can never zoom out past its edges, so there are no empty borders. It opens on the city. Zoom with the wheel or buttons, drag to pan, City returns to the city, and World shows everything.
- The map always fills the whole window. The header and the bottom bar float over it with a light blur, so there is no empty space. A Fullscreen button (or the F key) hides the browser around it too.
- Parallel jobs are separate cities. The Aegean view (1600×900, draggable and zoomable like a city) shows every job as a city-state on its own island, with dashed sea routes and boats for Assembly messages, and a card per city with phase, agents, cost in dollars and progress. City chips in the info box switch between cities. Each view remembers where you left the camera.
- Each city follows the Sacred Way: the stations are numbered in pipeline order, labels are always shown (dimmed when idle), and a step tracker in the info box shows where the job is.
- Bubbles carry a pixel status icon and a status-coloured border, so the state reads at a glance.
- Five landmarks around the city (Mount Olympus, the wooden horse, the Temple of Poseidon, the Lighthouse of Pharos and the Colossus) are there to discover. The HUD counts what you've found.
- The camera drifts toward where the work is happening, and stops following for a few seconds after you move it yourself.
- Agents glide between places with a small hop, so you can follow where each one goes.
- Bubbles stack so they never overlap each other or the place labels, and the text scales with zoom.
- On a phone the map fills the screen height and the agent panel opens as a bottom sheet.
- Panels (agent drawer, legend, place popups) slide over the map. The map never changes size when they open, so it never flickers.
- Agents who talk walk over and stand next to each other, with animated speaking portraits.
- Shipping is shown as the finalizer boarding the ship and sailing out.
- Clicking a building opens a small popup explaining it. A Legend panel explains every place, character and sign.
- A Theatre hosts a DJ (Solomun) who plays a local mp3 mix with sound when you start a set. See `docs/ART_CATALOGUE.md` §14a.
- Homer, the narrator, explains in a caption what is happening and why, and the Chronicle keeps the full story per city. In the real app the narration comes from the events (phase changes, handoffs, check results, your messages), written by a small model on the execute tier and passed through the humanizer.

### 10.3c Processes and pods

The engine starts every agent command as a child process in its own process group, and every e2e run in an Apple container. It records each one with its agent, job, command, pid or container id, ports, CPU and memory (sampled every second from the process table and `container stats`), start time and exit code, and emits these as events.

- The Processes panel lists everything across all cities, grouped by city, with totals for CPU and memory and a Stop button per row.
- The agent panel shows that agent's processes and pods.
- On the map, an agent's name tag shows how many processes (⚙) and pods (▣) it has running.
- Stopping sends SIGTERM to the process group (or `container stop`), and the agent is told in its thoughts.

### 10.4 Media

Agents upload e2e videos, screenshots, Playwright traces and test reports through `upload_artifact`. The engine also collects whatever the check tools already save, such as Playwright's `test-results/`.

Media cards appear next to the agent that made them. Each job also has a gallery you can filter by phase, agent and pass or fail. Videos play inline and screenshots open full size. UI changes show before and after pairs. When a check fails, its video and screenshots are linked from the result. The finalizer attaches the chosen media to the PR and the ticket comment.

### 10.5 The rest of the canvas

- The places connect in order: Gates, Library, Council house, Academy, Workshop and Oracle (back and forth), Areopagus, Harbour. The Agora and Symposium sit in the middle.
- The Symposium view has one bubble per experiment branch, showing its hypothesis, owner, check results, grades and score. The winner is highlighted once the engine decides.
- Each job's branch and its experiment branches are drawn as a small tree.
- Diagrams from `draw()` appear as cards that can be pinned or enlarged.
- A timeline lets you replay a finished job from its event log, with portraits, bubbles and media in order.

The Go engine serves the UI, streams events over WebSocket and serves media from `.factory/`. The canvas uses PixiJS, because it renders pixel art sharply at whole-number scales and handles many small animated parts well. The top bar, Inbox, Jobs, Settings and the agent drawer are plain HTML on top of it.

### 10.6 Local storage

```
.factory/
  factory.db                          # SQLite index of jobs, agents, events
  agents/                             # philosopher profiles and stats
  assembly/                           # cross-job announcements and overlaps
  images/
    <project>/Containerfile           # generated e2e image, never committed
  jobs/
    ABC-123-fix-login-timeout/        # ticket id + short slug
      events.jsonl                    # full event log, used for replay
      ticket.md                       # ticket snapshot
      check-plan.yaml                 # checks the user picked
      docs/                           # working docs, kept here after cleanup
      diagrams/
      media/
        workshop/archimedes/...png
        oracle/e2e-login-retry.webm
      symposium/
        exp-1-cache-layer/            # diff, results, grades, debate notes
        exp-2-fix-query/
  worktrees/
    ABC-123/                          # job branch factory/ABC-123
    ABC-123-exp-1/                    # experiment branch
```

## 11. Isolation: worktrees and containers

A git worktree is a second checkout of the same repo on its own branch. It keeps agents from editing each other's files, and that's all it isolates. Agents in a worktree still run as you, on your machine, with your home directory, credentials, network, ports and running processes.

A container runs the agent in its own filesystem, process space and network. Only the job's worktree and the secrets the engine passes in are visible. It can also run the job's own app and database without clashing with other jobs.

The two work together, and the factory uses both (D19):

- Every job and every experiment gets its own worktree. Agents work in it directly on the host.
- E2e checks run in a container. The engine starts the app, its services and the e2e runner inside the container, with the job's worktree mounted. Parallel jobs then don't fight over ports or a shared database, and videos and screenshots are copied out to `.factory/jobs/<job>/media/`.
- The factory builds the e2e image itself (D24), because most projects don't have one. During the survey, a researcher writes a Containerfile from what it finds in the repo: language and versions, lockfiles, services like Postgres or Redis, and the e2e runner with its browsers. The engine builds it, runs a smoke test, and stores both in `.factory/images/<project>/`. The image is rebuilt only when lockfiles or the Containerfile change. None of it is committed.
- The first container backend is Apple's `container` CLI, which runs each container in its own small VM on Apple silicon and uses standard OCI images. It needs macOS 26 on Apple silicon. This machine meets that, but the tool isn't installed yet. Docker or Podman can be added later behind the same interface for Linux and CI.
- The engine also gives each job its own port range, so non-e2e checks that start a server don't collide either.

## 12. Web interface and CLI

All input goes through the web interface (D21). The CLI only sets up the project and starts the server.

### 12.1 Screens

The interface has three screens and one drawer. Nothing else.

```
┌──────────────────────────────────────────────────────────────────┐
│  [ ABC-123            ] [Start]       Inbox (2)   Jobs   Settings │
├──────────────────────────────────────────────────────────────────┤
│                                                                  │
│                      canvas of the open job                      │
│                                                                  │
│                                              ┌─ agent drawer ──┐ │
│                                              │ click a portrait│ │
│                                              └─────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

- Top bar: ⚙ Admin and ＋ New task next to the logo (keys A and N). New task asks for an issue ID and optional notes; the engine loads the ticket from YouTrack. Repository, checks, runtime and model sit in a folded section and are remembered.
- Inbox: everything waiting for you, with one card per item and a button on each. That covers check plans to pick, plans to approve, agent questions and finished jobs to look at. The count shows in the top bar.
- Jobs: a list of running and finished jobs, each opening its canvas. Running jobs also appear as city-states on the map view.
- Settings: the admin panel (§12.2).
- Agent drawer: opens from the right when you click a portrait (§10.3). It closes with Esc or a click outside.

Every action is at most two clicks from any screen. The UI uses no modals except the agent drawer, and shows no settings on the canvas.

### 12.2 Admin panel

Settings is an admin panel with six pages:

- Connections: YouTrack URL and token, and the model gateway (JetBrains Central wire by default, see `docs/WIRE.md`), with a "test" button for each. Shows the Central credit balance.
- Runtimes: Pi, Claude Code and Codex, each with its install status, version and an on/off switch.
- Models: the model catalogue from §9.1. Add, remove or rename models, set which tier each belongs to, see prices, and run a one-prompt test.
- Roles: for each role, pick runtime and model (defaults: Pi with the tier's Claude model). Only valid pairs can be picked. A project can override these.
- Metrics: see §12.3.
- Limits: off by default (D34). Can be turned on per job, per agent or per model later.

Everything set here is stored in `~/.factory/`, outside any repo.

### 12.3 Metrics tab

Every event is already in the event log, so the Metrics tab is a set of views over it. Nothing extra has to be recorded.

- Communication graph: agents as nodes, messages as edges, sized by count and tokens. Can be shown for one job, a time range or all jobs, and replayed over time.
- Tokens and cost: by model, runtime, role, agent, job and day, with input, cached input, cache write and output split out. Central credits are shown next to the factory's own count.
- Time: wall time per phase, per agent and per job, and time spent waiting on you (Inbox) versus working.
- Movement: a timeline of each agent moving between places (Library, Workshop, Oracle and so on), and a flow chart of how jobs move through the phases, including loops back from Oracle and Areopagus to the Workshop.
- Loops: build loops until checks pass, review rounds, Symposium count and which experiment won.
- Requests: every model request with latency, tokens, cache hits and errors, from the meter.
- Comparisons: the same numbers side by side for models, runtimes and roles, to see which setup gets work done for the least cost and time.

Every chart can be filtered by job, project, role, model and date, and exported as CSV.

### 12.4 CLI

```
factory init      # create factory.yml and .factory/, update .gitignore
factory serve     # start the engine and open the web interface
```

## 13. Project config (sketch)

```yaml
# factory.yml
# YouTrack URL and token live in the factory env (Settings, stored in
# ~/.factory/env), never in this file.
tracker: { kind: youtrack, project: ABC, done_state: "In Review" }
ship: pr                       # commit | push | pr
checks:
  saved_plan: []               # if set, used without asking; if empty, the job asks in the Inbox
jobs:
  max_parallel: 3
roles:                         # tier and count only; runtime and model come from the admin panel
  scheduler:  { tier: think }
  planner:    { tier: think }
  researcher: { tier: code, count: 2 }
  builder:    { tier: code, count: 1 }
  executor:   { tier: execute }          # builder subtasks the planner spelled out in full
  reviewer:   { tier: think, focus: [criteria, hardcoding, edge_cases, simplicity] }
  humanizer:  { tier: code, skill: ~/.agents/skills/humanizer }
  finalizer:  { tier: execute }
limits: { build_loops: 5, review_loops: 2, max_usd: 20, max_minutes: 90 }
symposium:
  open_on: [multiple_approaches, repeated_failure]
  repeated_failure_after: 2
  max_open: 4
  max_usd_each: 3
  weights: { checks: 0.6, review: 0.4 }
isolation:
  workspace: worktree          # always a worktree per job / experiment
  e2e: { runtime: apple-container }   # image built by the factory into .factory/images/
  ports_per_job: 20
docs:
  keep: [docs/]                # dev-facing docs that stay after cleanup
```

## 14. Architecture

```
                   ┌─────────────────────── Go engine ───────────────────────┐
Trackers ────────► │ Intake ─► Survey ─► Check plan (user) ─► Job state machine │ ──► git / PR / ticket
(YouTrack, ...)    │                                        │                 │
                   │   Assembly (cross-job leases)    Scheduler requests      │
                   │                                        │                 │
                   │            Agent supervisor (start, stream, stop)        │
                   │              │               │               │           │
                   │         pi-host (Node,     claude          codex         │
                   │         Pi SDK)            (CLI)           (CLI)         │
                   │              └──── MCP tools ┴───────────────┘           │
                   │                         │                                │
                   │     Board, Symposium scoring, check runner               │
                   │                         │                                │
                   │     Event bus ──► Event store (JSONL + SQLite)           │
                   │                └─► Artifact store (.factory/)            │
                   │                         │                                │
                   │              HTTP + WebSocket server                     │
                   └─────────────────────────┬────────────────────────────────┘
                                             ▼
                                   Web canvas (browser)
```

## 15. Phasing

| Phase | Scope |
|---|---|
| A0 Art | The full asset catalogue: palette, scene, buildings, portraits, icons, UI frames, fonts. Every asset drawn, exported and shown on one preview page before any app code (D30). |
| M0 Spine (done) | Go engine, `factory serve`, Pi sidecar with one builder, user-picked checks with healing loop, enforced handoff, worktree, metering gateway, event log and stream. See `docs/ENGINE.md` §8. |
| M1 Pipeline | Survey and check plan, all roles, scheduler, board and MCP tools, Claude and Codex runtimes, ship modes. |
| M2 Canvas | Greek landscape, philosopher portraits, detail panel, messages and status. Then media gallery, diagrams and replay. |
| M3 Tracker | YouTrack settings, load ticket by ID, comments, state changes, dependencies and related tickets. |
| M4 Quality and Symposium | Reviewer rubrics, humanizer pass, docs cleanup, overfitting checks, experiment branches and scoring. |
| M5 Scale | Parallel jobs with the Assembly, e2e containers, budgets across jobs. |

## 16. Success metrics

- Share of tickets that reach review with no human code edits
- Median build loops until checks pass, and share of jobs handed to a human
- Review findings per job, which should drop over time
- Cost and wall time per ticket
- How often a ticket the factory marked done gets sent back in human review

## 17. Open questions

1. Art: does the first pass of the scene (`go run ./cmd/artbuild`, then open `web/assets/preview.html`) have the right feel before the rest of the catalogue is drawn?
