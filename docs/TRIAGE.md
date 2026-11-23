# Triage runs (the product agent)

A triage run is the product agent, Pericles (role `product`), going over one repo's board. It reads the open tickets and the code behind them, sets missing fields, splits tickets that are too big, links duplicates, asks the owner when a ticket can't be built as written, files tickets for broken things nobody has written up, and asks the engine to start jobs on the tickets that are ready.

Per D2 the agent decides and the engine executes. Tracker writes go through the operations service's guard, the same one Hermes uses. The engine starts the jobs and holds the run's limits.

## Starting one

Open the map, press **🏛 Board** (or B) and pick a repository. **Run Pericles now** takes:

- **YouTrack project**: the board's short name, like HF. It's filled in from the repo's `factory.yml` tracker block when there is one, else from its last run or its jobs' tickets.
- **Jobs he may start**: 2 by default, up to 10. With 0 he only recommends.
- **Time limit**: 30 minutes by default. The agent is stopped when it runs out.
- **What he may change**: the control level, by default the tracker's level (Fix unless `factory.yml` says otherwise).
- **Focus**: optional, what to look at first.

Only one run goes over a board at a time. The panel lists each run with why it started, what he changed, the jobs he started (click one to open its city), what he recommended, the questions he asked and the tickets still waiting on the owner. Homer tells the story on the map as he works.

## Running on his own

Off for every repository until you turn it on in the panel. You tick what makes him run:

- **An owner answered one of his questions**: a comment from a person on a ticket he asked about, after he asked.
- **Someone created or changed a ticket**: a ticket in the project updated after his last run started, later than the factory's own last write to it. The factory's moves and comments don't count.
- **His jobs are done and he left ready tickets**: his last run started as many jobs as it could, still listed ready tickets, and no job is going in the repo now.

The engine looks every 5 minutes. A run starts only when one of the ticked things happened, no run is going, at least the gap (60 minutes by default) has passed since the last run ended, and fewer than the daily limit (6 by default) automatic runs started today. The first look after you turn it on starts a run if he has never run on that repo. The panel shows what the last look decided. The settings are kept per repo under `triage` in `~/.factory/settings.json`; Admin's own saves leave them alone.

The API behind the panel: `GET /api/triage/boards`, `PUT /api/triage/auto`, `POST /api/triage`, `GET /api/triage?repo=`, `GET /api/triage/{id}` and `POST /api/triage/{id}/stop`. Runs are kept in `~/.factory/triage/<id>/run.json` and come back after a restart; a run that was going when the engine stopped is marked stopped.

## What the levels allow

| Level | Comments and new tickets | Fields and states |
|---|---|---|
| observe | refused | refused |
| propose | drafts | drafts |
| file | done | drafts |
| fix | done | done, on any ticket in the project |

At Fix the product agent may change fields and states on tickets the factory didn't create. In job scopes those changes stay drafts at every level, Hermes's included. Drafts wait in Admin → Review queue. New tickets are capped by `max_created_per_pass` (10 by default).

## Tools

The agent can read the repo (`read`, `grep`, `find`, `ls`) and has no shell. Its tools:

- `tracker_search`, `tracker_read`, `tracker_create`, `tracker_update`, `tracker_move`, `tracker_comment`, `tracker_link`, `tracker_attach` and `knowledge_search`, served by the operations service for the run's scope `triage-<id>`.
- `ask_owner(issue, question)` posts `[factory] Question for the owner: …` on the ticket. The owner answers there, and a later run reads the answer in the comments. At Observe the comment is refused and the question stays in the run's report.
- `start_job(issue, why, notes)` starts a job the way `POST /api/jobs` does, with the notes added. The engine refuses a ticket outside the project, a ticket a live job already works on, a ticket picked twice in one run, and anything past `max_jobs`. The job moves its own ticket on the board.
- `finish_triage(summary, ready, blocked)` ends the run. An agent that stops without it is reminded twice, and then the run fails.

## Events

On the bus under `triage-<id>`: `triage.started` (with `auto` and `why`), `triage.question`, `triage.job`, `triage.report` and `triage.ended`, plus the `tracker.*` events from the guard.
