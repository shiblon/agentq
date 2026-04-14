# Proposal: `eqc claimid` -- deterministic optimistic task claim by ID

## Problem

`eqc claim` is non-deterministic: it claims whatever task is next available in
the queue. When a human reviewer lists pending tasks and selects a specific one
by ID, there is no way to atomically reserve that specific task. The user sees
task A, but `claim` might hand them task B.

Encoding the correct sequence in a caller (list -> pre-check -> mod -> retry)
is subtle and easy to get wrong, especially in an agent-driven workflow.

## Proposed command

```sh
eqc claimid -t <task-id> -d <duration>
```

Claims a specific task by ID, held for `<duration>` (default 30s, same as
`claim`). Outputs the full task JSON on success, same format as `claim`.

## Semantics

1. Read the task by ID.
2. If not found: exit with a clear "task not found" error.
3. Check preconditions -- proceed only if:
   - The task is available (zero claimant, `At` <= now), OR
   - The caller is already the claimant (renewal).
   If neither: exit with a clear "task is held by another claimant" error.
4. Attempt `Modify` with `ArrivalTimeIn(duration)` and the task's current
   version (optimistic concurrency check).
5. On dependency error (version changed between read and mod): retry from
   step 1 up to a small number of times (e.g. 3), then exit with
   "task state changed, could not claim" error.
6. On success: print the updated task JSON and exit 0.

The retry loop handles the race between the pre-check read and the mod. In
practice contention on a human_review task is rare, so the fast path (succeed
on first attempt) is nearly always taken.

## Why not `mod --in`

`mod --in` could implement the arrival-time extension, but it has no knowledge
of claim state. The caller would still need to implement the pre-check,
conditional logic, and retry loop manually -- easy to get wrong in a script or
agent prompt. `claimid` encapsulates all of that behind a single command with
clear exit codes and error messages.

## Relationship to `ArrivalTimeIn` as a ChangeArg

The implementation will need `entroq.ArrivalTimeIn(duration)` as a `ChangeArg`.
Check whether it exists; if only `WithArrivalTimeIn` exists as an `InsertArg`,
a parallel `ArrivalTimeIn` ChangeArg constructor needs to be added to the
entroq library first, following the same pattern.

## Usage in human review flow

```sh
# 1. List pending tasks
eqc ts -q human_review

# 2. User picks task ID abc123

# 3. Claim it deterministically
eqc claimid -t abc123 -d 1h
# -> "task not found": workflow completed elsewhere; notify user
# -> "held by another claimant": someone else grabbed it; re-list
# -> success: prints task JSON, proceed with work

# 4. Do the work, then complete:
eqc ins -q <reply_queue> -v '<HumanReviewReply json>'
eqc rm -t abc123 -f
```
