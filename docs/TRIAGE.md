# Triage runs (the product agent)

A triage run is the product agent, Pericles (role `product`), going over one repo's board. It reads the open tickets and the code behind them, sets missing fields, splits tickets that are too big, links duplicates, asks the owner when a ticket can't be built as written, files tickets for broken things nobody has written up, and asks the engine to start jobs on the tickets that are ready.

Per D2 the agent decides and the engine executes. Tracker writes go through the operations service's guard, the same one Hermes uses. The engine starts the jobs and holds the run's limits.

## The Board panel

Open the map, press **🏛 Board** (or B) and pick a repository. At the top you choose how he runs:

- **When I say**: he goes over the board when you press **Run Pericles now**.
- **On his own**: he also goes over it by himself when something happens on the board (see below). You can still run him by hand.

**Run now** takes how many jobs he may start (Just suggest, 1, 2, 3 or 5; 2 by default), how long he gets (15 minutes to 2 hours, 30 by default) and an optional note on what to look at first. When the repo's `factory.yml` has no tracker block, the YouTrack project box next to the repo name says which board he works on. It's filled in from his last run or from the repo's jobs' tickets.

He runs the board himself: comments, new tickets, fields and states on any ticket in the project. When a ticket is unclear he asks its owner in a comment and picks up the answer later. There's no level to choose in the panel. The API still takes `level` for anyone who wants a lower one (see the table below).

Only one run goes over a board at a time. The panel lists each run with why it started, what he changed, the jobs he started (click one to open its city), what he suggested, the questions he asked and the tickets still waiting on the owner. Homer tells the story on the map as he works.

## Running on his own

Off for every repository until you pick **On his own**. Then he wakes when:

- an owner answers one of his questions: a comment from a person on a ticket he asked about, after he asked;
- someone creates or changes a ticket: a ticket in the project updated after his last run started, later than the factory's own last write to it. The factory's moves and comments don't count;
- his jobs finish and ready tickets are left: his last run started as many jobs as it could, still listed ready tickets, and no job is going in the repo now.

You pick how often at most (every 30 minutes to every 6 hours, 1 hour by default), how many runs a day at most (6 by default) and how many jobs each run may start. Changes save as you click.

The engine looks every 5 minutes. A run starts only when one of those things happened, no run is going, the gap has passed since the last run ended, and the daily limit isn't reached. The first look after you turn it on starts a run if he has never run on that repo. The panel shows what the last look decided. The settings are kept per repo under `triage` in `~/.factory/settings.json`; Admin's own saves leave them alone. Through the API you can still pick the triggers one by one (`on_board`, `on_ready`); owner answers always count.

The API behind the panel: `GET /api/triage/boards`, `PUT /api/triage/auto`, `POST /api/triage`, `GET /api/triage?repo=`, `GET /api/triage/{id}` and `POST /api/triage/{id}/stop`. Runs are kept in `~/.factory/triage/<id>/run.json` and come back after a restart; a run that was going when the engine stopped is marked stopped.

## What the levels allow

Runs use Fix unless the API call names another level.

| Level | Comments and new tickets | Fields and states |
|---|---|---|
| observe | refused | refused |
| propose | drafts | drafts |
| file | done | drafts |
| fix | done | done, on any ticket in the project |

At Fix the product agent may change fields and states on tickets the factory didn't create. In job scopes those changes stay drafts at every level, Hermes's included. Drafts wait in Admin → Review queue. New tickets are capped by `max_created_per_pass` (10 by default).

## Tools

The agent can read the code (`read`, `grep`, `find`, `ls`) and has no shell. He reads it in `.factory/board`, a checkout of origin's default branch that is fetched and reset for every run, so he sees what is merged and not a main checkout that may be behind.

His first message carries the board: every open ticket (up to 100) with its State, Priority, Type, Complexity, Ship mode, Assignee, comment count and links such as "depends on". It also lists the factory's last 25 jobs in the repo and what became of their PRs (MERGED, CLOSED, or OPEN and whether it can merge), so he knows whether a dependency is on main. He starts a ticket that depends on another only once that one's PR is merged, which keeps PRs from stacking. It gets the same tools on every runtime: Claude and Codex reach them over MCP, pi through `/api/tools/triage-<id>/…`. Its tools:

- `tracker_search` (up to 50 results, with State, Priority and Type; a `sort by:` clause is kept), `tracker_read`, `tracker_create`, `tracker_update`, `tracker_move`, `tracker_comment`, `tracker_link`, `tracker_attach` and `knowledge_search`, served by the operations service for the run's scope `triage-<id>`.
- `ask_owner(issue, question)` posts `[factory] Question for the owner: …` on the ticket. The owner answers there, and a later run reads the answer in the comments. At Observe the comment is refused and the question stays in the run's report.
- `start_job(issue, why, notes, tests_allowed)` starts a job the way `POST /api/jobs` does, with the notes added. `tests_allowed` lets the job change existing tests without the reviewer's check, for tickets whose owner said so. The engine refuses a ticket outside the project, a ticket a live job already works on, a ticket picked twice in one run, and anything past `max_jobs`. The job moves its own ticket on the board.
- `finish_triage(summary, ready, blocked)` ends the run. An agent that stops without it is reminded twice, and then the run fails.

## Events

On the bus under `triage-<id>`: `triage.started` (with `auto` and `why`), `triage.question`, `triage.job`, `triage.report` and `triage.ended`, plus the `tracker.*` events from the guard.
