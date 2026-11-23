# Triage runs (the product agent)

A triage run is the product agent, Pericles (role `product`), going over one repo's board. It reads the open tickets and the code behind them, sets missing fields, splits tickets that are too big, links duplicates, asks the owner when a ticket can't be built as written, files tickets for broken things nobody has written up, and asks the engine to start jobs on the tickets that are ready.

Per D2 the agent decides and the engine executes. Tracker writes go through the operations service's guard, the same one Hermes uses. The engine starts the jobs and holds the run's limits.

## Starting one

```
factory triage [-project HF] [-level fix] [-jobs 2] [-minutes 30] [-focus "the export bugs"] [repo]
```

or `POST /api/triage` with `{repo, project, level, max_jobs, minutes, focus}`.

- `project` is the YouTrack project. It is only needed when the repo's `factory.yml` has no tracker block, and it must match the block when there is one.
- `level` defaults to the tracker's level, which is `fix` unless `factory.yml` says otherwise.
- `max_jobs` defaults to 2 and goes up to 10. With 0, `start_job` only records a recommendation.
- `minutes` defaults to 30. The agent is stopped when the time runs out.
- Only one run goes over a board at a time. A second one gets 409.

`GET /api/triage` lists this server's runs, `GET /api/triage/{id}` shows one (from `~/.factory/triage/<id>/run.json` after a restart), and `POST /api/triage/{id}/stop` stops it.

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

On the bus under `triage-<id>`: `triage.started`, `triage.question`, `triage.job`, `triage.report` and `triage.ended`, plus the `tracker.*` events from the guard.
