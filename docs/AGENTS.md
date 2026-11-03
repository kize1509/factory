# How agents work together

Agents in a job know who else is working, whose output they read, who reads theirs, and how to reach them. This page holds the rules. The same rules are in the prompt `internal/prompts/roles/team.md`, which the engine adds to every role's system prompt.

## The job roster

The job owns the roster (`internal/job/roster.go`). Each member has a role, a state (`working`, `waiting`, `blocked`, `done`), what it is doing now, why it was started, and two lists: who it reads from and who it writes to.

- The engine updates it when an agent starts, starts or ends a turn, calls `set_status`, asks the person for help (blocked), writes a handoff (done), and when a Symposium loser leaves.
- Every change is logged as a `roster.updated` event with the whole roster. The map reads it.
- Agents get it two ways. The engine puts a short version at the top of each task prompt it sends. The `roster` tool returns the live one, with each agent's state and what it is doing. The prompt version leaves out anything that changes from run to run, so recorded prompts replay without drift.
- Patrol passes have a `roster` tool too: the pass's agents and their checks. Patrol agents work alone; `ask_agent` there tells them to record the check as blocked.

A builder's roster also lists, sorted and capped at 30, the paths the planner read, grepped or found (from its `agent.tool` events), so the builder doesn't repeat that exploration. `redo_reads` measures whether it still does.

The flow is fixed:

| Agent | Reads from | Writes to |
| --- | --- | --- |
| Planner | the person and the ticket | the builders: the plan |
| Builder | the planner's plan | the checks, then the reviewer (or the grader, in an experiment) |
| Experiment builder | the planner's hypothesis | the checks and the grader, without its name |
| Grader | the experiments' diffs, by letter | the engine: grades |
| Reviewer | the builder's diff and note | the builder: findings; the engine: the verdict |

## Messages

Every message between agents has the same envelope: `id`, `kind`, `from`, `to`, `job`, `ticket`, `text`, and optionally `sources` (links to notes, files, diffs) and `reply_to`. The engine fills in the job and the ticket (the ticket's ID, else the job ID), refuses a message with no sender or receiver, and logs each as an `agent.message` event. The older events (`handoff.written`, `review.question`, `review.answer`) are still written next to it.

| Kind | Who sends it | To whom | When |
| --- | --- | --- | --- |
| question | any agent but the grader | another agent, or `operations` | your input is unclear |
| answer | the agent asked | the one who asked | in the same call |
| handover | an agent with a finished handoff note | the next step | when the note is accepted |
| report | the engine, for each agent | `operations` | a turn starts and finishes |
| blocker | any agent | `you` | `ask_human` |

## Who to ask

- About your input: the agent you read from, with `ask_agent`.
- Not sure who knows: `ask_agent` to `operations`. If the operations agent (HF-9) isn't in the job, the engine sends it to the producer of your input that is running and idle. If there is none, you are told to use `ask_human`. HF-9 only has to join the roster and register its session to take this over.
- A credential, access or a decision only the person can give: `ask_human`.

Limits: you can't ask yourself, an agent in the middle of its own turn (so two agents never wait on each other), the grader (grading is blind), or another experiment. At most three questions per agent per turn. The reviewer's `ask_builder` is the same path with its own limit of four per round. If the agent you ask has gone (after a restart), you get the path of its saved handoff note.

## Waiting and going on

- `ask_agent` blocks until the answer comes. Ask only what you need to continue.
- If the other agent can't answer now, carry on with your best reading and write what you assumed in your handoff.
- `ask_human` ends your turn. The answer arrives as your next message.
- A blocker means stop.

## Where it shows

- Map: an agent's panel shows Reads from and Writes to, with buttons to open those agents and links to their notes.
- Admin → Metrics → Who talks to whom: drawn from `agent.message` events. Reports to `operations` are left out of the lines, or they would swamp the graph. Jobs logged before this existed fall back to the handoff events.
- Eval metrics (`harness.Metrics`): `asks` counts questions; `redo_reads` counts the builder's reads of paths the planner already read. Both take agent names from package `job` (`job.Planner`, `job.IsBuilder`).
