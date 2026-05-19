# Architecture

## The core idea

agentq builds multi-agent workflows on top of [entroq](https://github.com/shiblon/entroq),
a durable task queue with atomic document storage. The queue is not just a transport
layer -- it is the coordination substrate. Agents don't call each other; they post
work and pick up work, with the queue providing ordering, fault tolerance, and
visibility for free.

## Stateless agents and the continuity document

Each agent is stateless. It has no memory of previous tasks and no connection to
any session between invocations. What enables coherent multi-step workflows despite
this is the **continuity document** (called `Session` in the code): a durable record
in the eq doc store that carries everything a fresh agent needs to pick up where
the last one left off.

A useful mental model: each agent is a pure function.

```
f(system_prompt, continuity_doc, task) -> (updated_continuity_doc, follow_up_tasks)
```

The queue handles the plumbing between calls. The continuity doc is the accumulated
return value of the workflow so far.

When an agent picks up a task it reconstructs context from three sources:

1. **System prompt** -- loaded from a `doc:` URI. This is the agent's identity and
   instructions: what it does, how it reasons, what tools it can call. Fixed for
   the lifetime of the agent role.

2. **Continuity document** -- the `Session` doc in the eq doc store, referenced by
   `session_uri` in the task payload. Carries the original user prompt, status,
   and the index of artifacts produced so far.

3. **Task payload** -- the immediate instruction for this step.

After doing its work the agent appends an artifact, enqueues any follow-up tasks,
and deletes its own task -- atomically -- then goes back to claiming.

Consequences:

- **Any agent instance can handle any task** for its role. No per-session state is
  pinned to a process. Scale out by running more instances.
- **Context cost is bounded per task**, not per session. A long workflow is many
  short context windows, not one ever-growing one.
- **Crash recovery is automatic.** If an agent dies mid-task the claim expires and
  the task is redelivered. The continuity doc is only updated on successful
  completion.

## The supervisor as trampoline

The supervisor is a trampoline: work goes out to a specialist agent, comes back to
the supervisor, and the supervisor decides what happens next.

```
User
  |
  v
submit --prompt "..."
  |
  +-- creates continuity doc
  +-- inserts task into "supervisor" queue
  |
  v
Supervisor
  |
  +-- reads continuity doc
  +-- decides which specialist to invoke next
  +-- updates continuity doc (status, dispatch artifact)
  +-- inserts task into specialist queue, deletes own task
  |
  v
Specialist (coder, reviewer, researcher, ...)
  |
  +-- reads continuity doc
  +-- does its work
  +-- appends result artifact to continuity doc
  +-- inserts task back into "supervisor" queue, deletes own task
  |
  v
Supervisor (again)
  |
  +-- reads updated continuity doc (now includes specialist's artifact)
  +-- decides what to do next: another specialist, done, human review, ...
```

This structure keeps the continuity doc effectively single-writer (the supervisor
owns status; specialists append artifacts in their own turns) and makes the
dependency chain between steps explicit in the routing logic rather than implicit
in a concurrency model.

## Artifact files: immutable outputs by convention

Artifacts produced by agents are written as **files in a session-specific
directory**, named with a timestamp and agent name:

```
sessions/{session_id}/{timestamp}-{agent_name}-{artifact_type}.md
```

This convention means:

- **No write conflicts.** Two agents working on the same session (unusual but
  possible) cannot overwrite each other's output because every file name is unique.
- **Temporal ordering is self-evident.** If a step is retried or superseded, the
  later file has a later timestamp. Reasoning about which result to use is a
  sort, not a lock.
- **The continuity doc stays lightweight.** It holds an index of artifact paths and
  small inline summaries. The actual content lives in files. The doc never grows
  unboundedly.

The `Artifact.Path` field carries the file path. `Artifact.Content` holds small
inline content for convenience (e.g., dispatch summaries). Larger outputs belong
in the file, with the path as the reference.

## Storage: eq docs as the document store

Continuity documents and agent configs live in entroq's document store rather than
a separate database. One backend holds both the task queues and the document store.

Addressing convention:

| Content           | Namespace   | Key              |
|-------------------|-------------|------------------|
| Continuity doc    | `sessions`  | `{session_id}`   |
| Agent config      | `configs`   | `{agent_name}`   |

The `store` package wraps these conventions behind typed `Get`/`Put`/`UpdateSession`
functions. `UpdateSession` uses an optimistic-concurrency retry loop for the cases
where concurrent writes do occur.

For now the default document store is the eq server itself. If continuity documents
outgrow what fits comfortably in an eq doc value, or if query patterns require
indexing, the `store` package is the right place to swap in a different backend.

## Routing is just queue names

An agent decides what to do next by choosing which queue to post to. The supervisor
maps reasoning output to queue names. That's it. There is no central router, no
orchestration framework, no workflow DSL. Adding a new agent type means: pick a
queue name, run an instance watching that queue.

## The eq server

The queue server is `ghcr.io/shiblon/entroq-mem`, the published eqmem image from
the entroq project. It supports optional journal persistence; the journal replays
on restart so state survives process restarts. For production, the postgres-backed
entroq server (`eqpg`) provides durability without journal size concerns.
