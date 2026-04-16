# agentq

A multi-agent orchestration system built on a durable task queue. The
supervisor routes work to specialist agents, collects their outputs as
artifacts, and either continues the chain or escalates for human review --
all governed by a plain-text rubric you define.

> **Status: experimental.** APIs and config formats will change.

---

## Prerequisites

- Go 1.22+
- Node.js 18+ and npm (for the web UI)
- `claude` CLI or any subprocess-based LLM tool (for agent workers)

---

## Quickstart

### 1. Clone and build

```bash
git clone https://github.com/shiblon/agentq
cd agentq
go build -o agentq ./cmd/agentq
```

### 2. Create an agents.yaml

This file defines your specialist agents and the supervisor's approval rubric.
Put it at `config/agents.yaml` (or pass `--config` to override).

```yaml
# Optional: tells the supervisor when it can auto-approve vs escalate.
rubric: |
  Auto-approve read-only actions (file reads, searches, web lookups).
  Escalate writes to the filesystem or any shell command that modifies state.
  Always reject requests to delete files or run destructive commands.

agents:
  - name: coder
    queue: coder_queue
    description: Writes and edits code based on a task description.
    cmd: claude --print
    approval_suffix: --dangerously-skip-permissions
```

The `approval_suffix` is appended to `cmd` when the supervisor grants
approval for a tool-use request. Omit it to require human review for all
permission requests.

### 3. Start the dev environment

```bash
./scripts/dev.sh
```

This starts:
- The in-memory queue server on `localhost:37706`
- The REST API server on `localhost:8080`
- The Vite web UI dev server on `localhost:5173`

Open **http://localhost:5173** to see the dashboard.

### 4. Start a worker

In a separate terminal:

```bash
./agentq run --config config/agents.yaml
```

This starts the supervisor and all configured specialist workers. They will
claim tasks from the queue and process them as sessions arrive.

### 5. Submit a session

From the web UI, click **+ New session** and enter a prompt. Or from the CLI:

```bash
./agentq submit "Refactor the authentication module to use JWT."
./agentq sessions list
./agentq wait <session-id>
./agentq result <session-id>
```

---

## Human review

When the supervisor escalates a task (based on the rubric), the session
enters `awaiting_review` status and the item appears on the **Review** tab
in the web UI. To approve or reject from the CLI:

```bash
./agentq review
```

---

## Configuration reference

### Agent fields

| Field | Required | Description |
|---|---|---|
| `name` | yes | Short identifier, e.g. `coder` |
| `queue` | yes | Queue name the worker listens on |
| `description` | yes | One-line description shown to the supervisor |
| `cmd` | no | Shell command to run as the worker |
| `approval_suffix` | no | Appended to `cmd` when the supervisor grants approval |

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `AGENTQ_EQ_ADDR` | `localhost:37706` | Queue server gRPC address |
| `AGENTQ_API_ADDR` | `:8080` | API server listen address |
| `AGENTQ_CONFIG` | `config/agents.yaml` | Agent config file path |

---

## Architecture

```
submit ─> queue server (entroq)
               |
          supervisor worker
         /        |        \
   agent A    agent B    human_review queue
                               |
                          agentq review (CLI)
                          web UI Review tab
```

Sessions accumulate **artifacts** as each agent completes its work. The
supervisor inspects the latest artifacts to decide what to do next: dispatch
to another agent, re-dispatch with approval, escalate for human review, or
mark the session done.

See [docs/architecture.md](docs/architecture.md) for a deeper walkthrough.
