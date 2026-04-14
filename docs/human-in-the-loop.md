# Human-in-the-loop

## The key insight

Agents don't need a special "waiting for human" mode. Waiting for a human is the
same as waiting for any other agent: post work to a queue and stop. The difference
is that the queue consumer on the other end happens to be a person (or a dashboard
backed by a person) rather than another automated process.

This falls out of the architecture for free.

## When an agent needs human input

An agent that needs approval, additional privileges, or human judgment does this:

1. Appends a `human_review` artifact to the session, describing what it needs and
   why it stopped.
2. Inserts a task into the `human_review` queue carrying the session URI and a
   summary of the decision or action that requires approval.
3. Deletes its own task.

The agent's work for this step is done. It goes back to claiming other tasks.
Nothing is blocked. No goroutine is sleeping. No timeout is ticking.

## The human side

A human dashboard is a queue consumer with a UI instead of a handler function. It:

- Watches the `human_review` queue for new tasks.
- Displays the session context (prompt, artifacts so far, what the agent needs).
- Lets the reviewer approve, reject, or provide additional input.

On approval, the dashboard posts a reply task into the requesting agent's queue
(or a designated reply queue), carrying the session URI and the outcome. The agent
picks that up on its next claim cycle, loads the session, and continues.

On rejection or escalation, it routes to an error queue or a different specialist.

## Why this works well

**Visibility is automatic.** The `human_review` queue is observable with standard
eq tooling (eqc, dashboards, metrics). You can see how many items are pending,
how long they have been waiting, and which sessions they belong to -- without any
special instrumentation.

**It composes with everything else.** A human-reviewed task looks like any other
task to the rest of the system. An agent that sometimes needs human approval and
sometimes doesn't just branches: route to `human_review` or route to the next
specialist, based on its own reasoning. The harness doesn't change.

**Escalation is just routing.** If a human reviewer decides a task needs a senior
review or a different team, they insert a task into the appropriate queue. The
session doc already carries the full history.

**No deadlock risk.** Because agents don't block waiting for the human response --
they post and exit -- a slow or absent human reviewer doesn't stall the entire
system. Other sessions and tasks continue processing. Only the specific task that
needs human input is paused, held in the queue until acted on.

## Example queue layout

```
supervisor          - orchestration decisions
coder_queue         - code generation and editing
reviewer_queue      - code and output review
researcher_queue    - web search and research
human_review        - tasks requiring human judgment or approval
human_review/infra  - tasks requiring infrastructure access grants
human_review/legal  - tasks requiring legal sign-off
```

Namespacing the human review queues (e.g. `human_review/infra`) lets different
teams or dashboards watch only the work relevant to them, without coordination.

## Example task payload for a human review request

```json
{
  "session_uri": "doc:sessions/029d3e20de69940d",
  "requesting_agent": "coder",
  "reason": "need write access to the production database to run migration",
  "reply_queue": "coder_queue",
  "context_summary": "implementing login page; migration adds sessions table"
}
```

The `reply_queue` tells the dashboard where to post the approval response. The
`context_summary` gives the reviewer enough to act without loading the full session.
