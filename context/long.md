# Long

## supervisor-mental-model
### Core framing
The supervisor is the 'chat AI with legions beneath it'. It is a long-running conversation
that farms out work instead of doing any itself. All context management, compaction, and
memory live here. Specialist agents are ephemeral hands; the supervisor is the brain.

### Context ownership
- Supervisor decides what context each task needs and includes it in the task payload.
- AgentQ is a mechanical relay: reads task, issues JWT (carries tool allowlist + filesystem context
  for MCP), dispatches to runner via eqlink, collects delta. No decisions.
- Runner presents the JWT to MCP at SSE handshake -- no separate config call from AgentQ.
- Delta per step = a new message in the supervisor's conversation.
- Supervisor accumulates deltas and decides what to include in the next task (compaction is its problem).

### Scaling
- Supervisor workers are stateless -- task carries all context.
- Multiple supervisor workers claim from the same queue; EntroQ load-balances.
- Specialist agent pods scale the same way. Parallelism is free by construction.

### Initial implementation
- Turn-based, one session at a time. User waits for supervisor to return.
- Supervisor loop: claim result task -> incorporate delta -> decide next step -> dispatch or return.
- Concurrency is a performance optimization later, not a correctness requirement now.

### What supervisor needs from result tasks
- What the agent said (delta transcript)
- What the agent did (file change log from MCP)
- It does NOT need to understand how agents work, only what they reported.

## What was built
pkg/mcp/server.go -- Server type: New(tools) starts on random localhost port,
URL() returns base URL for MCP client, Close(ctx) shuts down gracefully.
Uses mark3labs/mcp-go v0.54.0 SSEServer as http.Handler over a net.Listener.

pkg/mcp/tools.go -- SessionTools(session, agentName, sink) returns the two
baseline tools every agent gets:
  - read_session: returns session JSON (prompt, artifacts, metadata)
  - write_artifact(type, content): appends an artifact via ArtifactSink callback

CollectingSink() returns a sink + pointer to collected []models.Artifact.

NOTE: This code reflects an earlier design where AgentQ started MCP per-task and
injected baseline tools. The settled design (see deployment-architecture) uses MCP
as a pool service with JWT-at-handshake; pkg/mcp needs to evolve to match.

## MCP as the agent sandbox
The sandboxing strategy for AgentQ: agents run in locked-down runner pods with
NO shell, filesystem, or network access except:
  1. The LLM API endpoint (e.g. Anthropic API)
  2. The MCP service (a stateless pool, separate pod type)

The MCP service is the capability enforcer. Configuration is carried in the JWT
that AgentQ issues -- no separate admin call. MCP validates the JWT at SSE handshake,
sets up the allowed tool list and filesystem context, and holds that configuration
static for the duration of the session. The agent can only do what MCP exposes.

This sidesteps the prompt injection / confused deputy problem structurally: the
blast radius is bounded by the MCP tool set, not by system prompts.

Prior design note: earlier iterations started MCP fresh per-task within the AgentQ
worker pod. The settled design uses MCP as a long-lived pool service, configured
via JWT at handshake, scaling independently of both AgentQ and Runner.

## deployment-architecture
### Four component types -- independent roles and scaling criteria

**Supervisor** (chat harness, turn-based for now)
- Long-running conversation; farms out work, never does it directly.
- Owns context, compaction, memory. Specialist agents are ephemeral hands.
- Scales: on user demand (like opening a terminal).

**AgentQ worker** (worker + eqlink outbound)
- Claims sessions from EntroQ inbox, issues signed JWT, bundles prompt + session context,
  enqueues task onto runner queue via eqlink outbound.
- Lightweight; never speaks directly to MCP or Runner.
- Scales: on queue depth.

**Runner** (eqlink inbound + runner process)
- eqlink claims tasks from runner queue; runner receives JWT + prompt + session context.
- Contacts MCP directly (presents JWT at SSE handshake), runs AI agent (Claude CLI or other).
- Needs external egress for LLM API; can reach MCP service; nothing else.
- Posts result back to EntroQ queue when done.
- Scales: on token budget -- eqlink auto-queues when runner count lags AgentQ, so runner
  pool can safely be smaller. (Specific token-budget scaling policy: tabled.)

**MCP** (Deployment + Service, stateless pool)
- Auto-configures on first session interaction via JWT (no separate admin call).
- JWT carries: allowed tool list, volume, working directory, session ID, expiry, issuer.
- Validates JWT at SSE handshake; stores allowlist as session state; filters tools/list response.
- Allowlist is static for the session -- does not deviate mid-run.
- SSE is multiplexed: one pod handles many concurrent agent sessions. Pod count need not match runner count.
- Scales: on connection load (HPA on connection count / request rate).

### JWT (issued by AgentQ, consumed by MCP)
Claims: tool allowlist, volume, workdir, session ID, iss (AgentQ), exp (task duration upper bound).
MCP protocol-level session ID is separate from this JWT.

### Network policy
- AgentQ: egress to EntroQ eqlink only.
- Runner: egress to EntroQ eqlink + MCP Service ClusterIP + AI API (external).
- MCP: ingress from Runner; egress as needed for tool implementation (git, web, etc.).

### What this solved vs. prior design
Prior: single pod with AgentQ+MCP+Runner sharing network namespace (MCP egress bled to AgentQ).
Current: four separate component types, proper network isolation via NetworkPolicy, independent scaling.

## workspace-file-sharing
Design decided: Gitea for VCS, MinIO for large/binary files.
- Git won; Mercurial considered but rejected for ecosystem reasons
- Gitea: single Go binary, REST API for repo management, runs in Docker Compose
- MinIO: S3-compatible, HTTP API (no volume mount needed), for non-git artifacts
- Co-located workers: each exec worker manages its own local clone/worktree; git is the transport

Architecture:
- Each project gets a Gitea repo created via API at workspace init time
- Each session gets a branch: session/<id>
- Exec workers: checkout worktree before task, commit+push after, working dir = worktree
- Large/binary outputs -> MinIO under sessions/<id>/, ref stored in artifact metadata
- --continue-from sessions branch from parent session's final commit

WorkspaceBackend interface (planned):
  type WorkspaceBackend interface {
      Init(ctx, name string) error
      Checkout(ctx, sessionID, parentID string) (dir string, err error)
      Commit(ctx, sessionID, message string) (ref string, err error)
      Push(ctx, sessionID string) error
  }

New CLI command planned: agentq workspace init --name <project>

## idp-choice
Working choice: Zitadel. Self-contained, single-binary, Go, Apache 2.0. Has a first-class machine user concept (non-human principals with their own credential lifecycle). API-first. OIDC/OAuth2 compliant. Easy to add to docker-compose. The machine user model maps directly onto "agent as a principal".

Strong alternatives:
- Dex (Go, Apache 2.0, CNCF): user has hands-on experience. Designed as a federated OIDC connector -- sits in front of another credential store. If there is a natural upstream (e.g., Gitea already has users), Dex as the OIDC layer is very clean.
- Ory Hydra + Kratos (Go, Apache 2.0): very principled OAuth2/OIDC, more assembly required, good choice if Zitadel hits a rough edge.

Ruled out: Keycloak (too heavy, Java), Authentik (human-SSO focused).

Design goal: whatever IDP we use must support credential issuance to non-human principals via API, token exchange, and fit in docker-compose without dominating the stack.

## image-registry
Use ghcr.io/shiblon/agentq as the image reference in all configs and docs. Docker Hub only used historically because it was the only option. ghcr.io is now mainstream, pulls without extra docker login friction for public images, auth in Actions is just GITHUB_TOKEN, and the image lives alongside the code.

## image-build-ci
User prefers manual image building over full gitflow CI, but is open to tag-triggered builds as a middle ground (push a tag -> image published). No current gitflow/release hygiene, so explicit control over when an image is cut is desirable.

When setting up image publishing: propose a GitHub Actions workflow that fires only on v* tag pushes (not every commit or PR). Also offer a scripts/build-image.sh as a fully manual fallback.

## session-chaining
Session chaining is complete and end-to-end tested (2026-04-15). Compact mode supervisor logic is also implemented (2026-04-23).

Implemented:
- agentq submit --continue-from=<id> copies parent non-supervisor artifacts into new session
- Session.ParentSessionID links sessions for chain inspection
- Artifact.OriginSessionID tags inherited artifacts
- agentq inspect --chain <id> walks parent links oldest-first
- Supervisor buildUserPrompt separates inherited vs fresh artifacts in routing context
- Compact mode: when session.Meta.CompactInherited is true and LLM is configured, supervisor calls maybeCompactInherited on first dispatch, which asks LLM to summarize inherited artifacts into a single supervisor/compact_summary artifact. Idempotent.

End-to-end test result: Session 1 wrote an LRU cache, Session 2 --continue-from added benchmarks and tests successfully.

## security-context
agentq has the right shapes for security -- the Authorizer interface, UserID on sessions, AgentName on artifacts, the OPA hook -- but every principal in the system is currently a string. Nothing is issued, nothing is signed, nothing is revocable. Fine for a dev prototype.

"We built the skeleton, now we are putting bones in it" -- this framing is better than "we will add security later." Worth using in the blog post.

Where the strings are (bones to add):
- Session.UserID -- a string; should be a verified OIDC subject claim
- Artifact.AgentName -- a string; should be identity-derived, not self-reported
- Authorizer.Allow(ctx, r) -- sees a raw HTTP request; no JWT verification, no claims extraction
- Task payloads -- carry agent names but no credentials; nothing binds a worker to the identity it claims

The work: take every place a string stands in for an identity and replace it with something issued and verifiable. Zitadel issues credentials, OPA verifies them, queue payloads carry the identity forward between hops.

## blog-readiness
Blog-readiness as of 2026-04-23. Items marked DONE have been completed.

DONE: Review approve/reject in the UI (POST /api/v1/review/{id}/approve|reject + buttons in card)
DONE: Start all workers in one command (agentq run --all)
DONE: Security basics (JWT auth, OPA authz, token exchange, PKCE web auth, device flow CLI login)
DONE: agentq agent update command
DONE: Session cancel endpoint + CLI + UI button
DONE: Compact mode supervisor summarization

Still pending:
- UI polish -- current interface is too bare; needs a visual dashboard
- Deployment -- docker-compose dev only; need top 2-3 deployment targets airtight before 0.1
- Blog post series structure -- multiple posts, not one; plan how to divide them

North-star: zero-configuration story. User installs agentq and gets a working multi-agent loop with no config file, no env vars, no manual queue setup. Every design decision should be evaluated against how much it moves toward or away from that ideal.

Closing sentence the series should earn: "Every action taken in this system can be traced to a human authorization decision, through a chain of issued credentials, with no gaps."

## agent-identity-framework
OAuth answered the authorization question. OIDC answered the human authentication question. Nobody has properly answered the agent authentication question yet. agentq is the showcase for what that answer looks like.

BLOG POST ACTION ITEM: ask user to share or draft blog post ideas before designing the identity model. That post is the input for the design work -- do not design without it.

The supervisor as surprise-enabler: not just an orchestrator but a chokepoint for intent. Every worker communicates only through the supervisor, which sees both what was asked (intent) and what was done (artifacts) simultaneously -- can catch drift, flag surprises, escalate before damage compounds. Surprise has to be structurally enabled, not bolted on.

What is actually new about agent identity:
1. Presence vs. legitimate issuance: human auth requires presence; agents need legitimate issuance -- a credential properly minted, for a specific principal, with specific scope, at a known time. Issuance + revocation replaces presence as the trust anchor.
2. Agent/user are tied but not the same: agents are delegated principals, not proxies. The user grants a scoped credential derived from (but not equal to) their own.
3. Users need controls over which agents they can invoke -- this exists nowhere today.

The right model (five pillars):
1. User/agent distinction -- systems must distinguish human from agent principals
2. Agents are OAuth clients -- client credentials flow is underused but exactly right
3. Agent invocation as a permission -- users need explicit permission to invoke specific agents
4. Dynamic scoping / downscoping -- permissions narrowed to what is needed for the specific task
5. Intent is load-bearing -- authorization must be grounded in what the user actually asked for

Key refinements:
- Agents as apps: issuance is the declaration moment; that app token is the identity anchor
- Confused deputy vs. prompt injection: do not conflate; confused deputy is an authorization failure, prompt injection is a content attack that can cause it
- Workers with minimal and distinct permissions: blast radius of any single worker is bounded by design, not policy
- Fine-grained resource access (which specific resources a worker can touch) is not yet sketched -- save for a future session

Design implication: the OPA authorizer hook is the natural home for dynamic scoping and intent-checking. Zitadel handles issuance/revocation for agent principals.

## agent-vocabulary
Core vocabulary (use consistently in code, docs, and blog posts):
- Agent (bare): "the thing I am talking to, that can cause things to happen and give me information." Intentionally vague at this level.
- Behavior: the artifact you author, publish, install. "I wrote an agent" means "I defined a set of behaviors."
- Identity: a system account with relaxed constraints that allow it to look more like a user.
- Actor: Behavior + Identity. The complete runtime identity of an agent as it presents to the world. (Formerly Persona -- renamed to avoid UX terminology collision.)
- Instance: Behavior + Context. Like the difference between an app in a store (behavior) and on your phone (instance).
- Invocation: the act of triggering agent Behavior by engaging with an agent Identity.

Invocation models:
- Bundle model: identity and behavior installed together. Strong attestation, poor fit for democratized development.
- Service account model (PREFERRED): identity and permissions independent of behavior. Team defines what the identity does. This is the GitHub Actions model.

Permission classes:
- Impersonation: agent is the user. High risk, poor auditability.
- Autonomy: agent identity fully distinct. Clean model, good auditability.
- Service: agent has more permissions than the user in specific domains plus targeted impersonation. Dynamic escalation.
- Subordinate: agent is the user, downscoped. Start with user permissions, drop until barely sufficient. Dynamic constraint. Should become the most familiar model.

The discretion problem: strong technical controls exist for access; only cultural controls exist for discretion. Agents do not respond to cultural controls. Unsolved problem AI deployment has made urgent.

The trampoline conversion: if a workflow node can become a trampoline rather than a passthrough, some discretion controls can become access controls. The supervisor-as-chokepoint in agentq is an early sketch of this.

## chimeroll
chimeroll is a browser-based ABC music notation experiment (JS + abcjs). Moved from ~/chris/www/chimeroll to ~/chris/code/src/github.com/shiblon/chimeroll. Action needed: create github.com/shiblon/chimeroll repo and push. Not urgent, but do not lose it.

## security-research-conclusions
Security research synthesis (2026-04-26). Full docs in docs/research/.

What agentq is genuinely ahead on:
- RFC 8693 delegation chains: implemented and central; rare in agent frameworks
- Supervisor as structural chokepoint: sees intent + outcome simultaneously; structural, not bolted on
- Queue topology as auditable communication graph: BeyondProd-style declared graph
- OPA as declarative policy enforcement point

Honest gaps:
- Per-task scoping: not implemented; RFC 9396 (RAR) is the path; IDPs not ready yet
- Queue ACLs: not enforced; any process can claim any queue; entroq needs this
- Workload attestation: absent; SPIFFE/SPIRE is the documented path
- Artifact signing: absent; compromise of queue could inject artifacts
- Supervisor still highest-privilege component; queue ACL gap means write-only constraint not enforced

The novel combination claim:
Queue-mediated communication + delegation chains + structural chokepoint = every action traceable to a human authorization decision, communication graph auditable by design. This combination does not exist in LangGraph/AutoGen/CrewAI.

Macaroons (Google 2014) are worth examining as the attenuation mechanism for approved_actions -- bearer tokens with embedded caveats that the supervisor could attenuate before handing to agents.

Things to verify before blog post:
- Whether any 2024-2025 agent framework has engaged with delegation chains or capability security
- CNCF TAG Security whitepaper on AI/ML workloads
- OpenFGA applied to agent authorization
- Academic work on privilege-separated LLM agents

## premortem-and-security-direction
Premortem and security research session (2026-04-26). Full research docs in docs/research/.

## dogfooding-use-cases


## security-posture-honest


## k8s-first-philosophy


## test-and-ux-plan


## queue-naming-migration


## worker-model-evolution


## auditability-design


## agentq-scope


## project_prompt_wiring


## invocation-vs-action-privileges


## user-invocation-permissions


## prosumer-model-decision


## eqlink-k8s-routing


## k8s-deployment-jettison


## project_security_context


## artifact-filesystem-model


## Artifact and Filesystem Model (settled 2026-05-16)
### Core model
- Session working directory on shared volume (NFS now, RustFS later -- MinIO went closed source)
- Artifacts ARE files. No blob store in the DB; session store holds metadata only.
- Multiple agents share the same working directory (the codebase). Conflicts accepted for now.
- `/.agentq/` reserved for intermediate work, memory files, step markers. Agents instructed to use it.

### MCP as chroot
- MCP provides all file tools. Agent addresses paths from `/`; never sees the real mount path.
- MCP prepends the real working directory root to every operation and rejects `..` traversal.
- No OS-level chroot needed -- MCP IS the chroot.
- No `write_artifact` MCP tool. Agent writes files directly via MCP file tools.
- MCP logs every tool call (tool, args, result, timestamp) to `/.agentq/logs/step-N.jsonl`.

### MCP configuration (per task)
AgentQ passes to MCP at configure time:
- Real working directory path (for file tool chroot)
- Session context from task payload (for `read_session` tool)
- Allowed tool list (read from task payload, set by supervisor)

### Result task (delta)
AgentQ returns to supervisor:
- Delta transcript: agent stdout for this step (what the agent said)
- File change log: from MCP tool events (what was created/modified/deleted)
- Step metadata: agent name, duration, exit status, step number
- Inline content or URI reference -- both supported, supervisor handles either

## The skeleton without bones
agentq has the right *shapes* for security -- the Authorizer interface, UserID
on sessions, AgentName on artifacts, the OPA hook -- but every principal in the
system is currently a string. Nothing is issued, nothing is signed, nothing is
revocable. The system works by trusting the process boundary and local network,
which is fine for a dev prototype.

The skeleton is correct. It just has no bones in it yet.

## EntroQ and AgentQ are complementary, not overlapping
EntroQ handles **invocation privileges between services** natively -- it controls
what can claim from what queue, enforcing inter-service boundaries at the queue
level.

AgentQ handles **provenance and user-based invocation** -- the human side, the
chain of custody from a user action through to the agent that executed it.

They solve different parts of the same problem. Don't collapse them.

## Agents as confused deputies -- the core threat model
Agents are all potentially confused deputies: they hold permissions, and crafted
inputs can trick them into using those permissions in ways the user never intended.
This is the *primary* motivator for EntroQ's tight security requirements. The
queue is the chokepoint -- if you control what an agent can claim and from where,
you constrain the blast radius of a confused deputy.

## Native queues over microservices
Could be done with microservices + service mesh. But:
- Sidecars (Envoy, etc.) intercept *some* traffic, not all -- gaps exist by design
- With native queue workers, every interaction goes through the queue, allowing
  uniform security enforcement at one seam
- Ingress/egress questions simply don't arise for queue-internal communication

Native queues are the tighter option here.

## Where the strings are
Every place a string stands in for an identity is a bone to add:

- Session.UserID -- a string; should be a verified OIDC subject claim
- Artifact.AgentName -- a string; should be identity-derived, not self-reported
- Authorizer.Allow(ctx, r) -- sees a raw HTTP request; no JWT verification
- Task payloads -- carry agent names but no credentials

## The work
Replace strings with issued, verifiable identities:
- **Zitadel** issues credentials (machine users for agents, OIDC tokens for humans)
- **OPA** verifies them against policy at the Authorizer hook
- **Queue payloads** carry the identity forward between hops

The Authorizer interface is exactly where JWT verification belongs.
Session.UserID is exactly where a verified subject claim belongs.
Artifact.AgentName is exactly where an identity-derived label belongs.

## IDP decision
Start with Zitadel (single container, machine users first-class, API-driven
issuance). The Authorizer interface is the only seam that touches the IDP.

## k8s deployment: what agentq jettisons in favor of eqk8s operator
eqk8s operator (../entroq/cmd/eqk8s) is stable as of 2026-05-14. Two CRDs:
  EntroQIdentity -- maps k8s ServiceAccount names to mesh label claims
  EntroQQueue    -- declares queue patterns and allowedCallers label predicates
Operator watches both, builds OPA mesh doc, writes to ConfigMap + PUTs to OPA data API.

**What agentq jettisons for the k8s deployment path:**
- config/entroq-policy/data.json (REPLACE_WITH_*_SUB bootstrap dance) -> EntroQIdentity + EntroQQueue CRDs
- deploy/zitadel-bootstrap.sh for agent machine users -> k8s ServiceAccounts + EntroQIdentity
- Zitadel machine users for agent identity -> k8s SAs are the identity anchor

**What stays:**
- Zitadel (or other IDP) for human auth at the agentq API level
- --no-auth dev path (unaffected, always local)
- OPA policy for agentq API (separate from mesh OPA; governs session/review API access)

**What agentq needs to add for k8s:**
- deploy/k8s/: ServiceAccounts (one per agent role), Deployments, Services
- deploy/k8s/entroq-identity.yaml: EntroQIdentity CRD for all agent SAs
- deploy/k8s/entroq-queues.yaml: EntroQQueue CRDs for each queue with allowedCallers

**k8s deployment story:** install entroq operator, apply agentq manifests, done.
No bootstrap scripts, no UUID hunting, no machine user sub claim copying.

## eqlink k8s routing for agentq workers (subdomain-based, stable as of 2026-05-14)
eqlink sender now routes via Host header (not URL path). Works with k8s DNS automatically.

**How it works:**
  eqlink --queue agentq/supervisor --domain-suffix .svc.cluster.local --namespace agentq
  Incoming call to http://coder.agentq.svc.cluster.local/task
    -> strip .svc.cluster.local -> agentq.coder -> queue prefix agentq/coder
    -> receiver watches agentq/coder/inbox
    -> response on agentq/coder/response/exp=.../hex

**For local dev/testing:**
  --domain-suffix .localhost --namespace agentq
  Call http://coder.agentq.localhost/task -> queue agentq/coder/inbox

**k8s implication:** each worker is a k8s Service named 'coder' in namespace 'agentq'.
k8s DNS gives coder.agentq.svc.cluster.local for free. No manual queue name wiring.

**Constraint:** eqlink is request-response only. WebSocket and SSE are explicitly rejected (501).
agentq workers are request-response by design, so this is fine.

**Audit logging:** --audit-log flag on eqlink run emits structured JSON to stderr (slog).
Events: request_enqueued, request_handled, response_received. Correlation key: response_queue.

## Prosumer model: subprocess runner is the deliberate initial approach
**Decision:** agentq workers are subprocess runners (cmd: claude --print, ollama run, etc.).
agentq does NOT manage LLM credentials, implement OAuth flows, or touch Anthropic/provider auth.
Auth is entirely delegated to the CLI tool in the cmd field.

**Why:** Makes the prosumer model legitimate -- a Claude subscriber can run agentq using
their existing claude login session, no API key or billing setup required. One 'claude login'
on the host, all worker invocations use those credentials transparently.

**The pi-agent anti-pattern (what we avoid):** implementing a custom auth layer on top of
the provider's official tooling. We exec the official CLI; it handles everything.

**Credential modes (documented, not implemented by agentq):**
- Claude subscription: cmd: claude --print, credentials from claude login on host
- Anthropic API: cmd: claude --print, ANTHROPIC_API_KEY in environment
- Local/Ollama: cmd: agentq llm --addr http://ollama:11434, no credentials needed

**Who holds API keys (future question):** deliberately deferred. Prove the concept first.
This is sketchy in most orchestrators and we want to get it right, not fast.

## User-level invocation permissions — open design item
**What's missing:** per-user controls over which agents they can invoke.
Currently any authenticated user can submit a session that triggers any agent.

**Where it lives:** agentq API OPA policy (deploy/policy.rego / pkg/api/policy/).
input.user (JWT claims) and the agent config are both available at policy evaluation time.
The enforcement hook exists; the policy expression is not yet written.

**What the policy needs to express:**
  'user X (by role, group, or sub claim) may submit sessions that invoke agent Y'
Example: contractors can invoke researcher but not coder or deploy.

**Relationship to the two-layer privilege model:**
- App-level (queue ACLs, OPA at entroq gRPC): agent-to-agent invocation. Done.
- User-level (agentq API OPA policy): human-to-agent invocation. This item.
These are independent layers -- both are needed for the full story.

**Not a blocker for initial release** — omit and document as a known gap.
Design it properly before multi-user deployment.
Relates to: invocation-vs-action-privileges, agent-identity-framework (pillar 3).

## Invocation privileges vs. access/action privileges — agentq's structural expression
agentq naturally embodies a clean separation between two distinct privilege classes:

**Invocation privileges** — the right to call/dispatch to an agent.
Enforced structurally by queue ACLs (OPA at the entroq gRPC layer):
- Only the supervisor can INSERT to agent inboxes.
- Only humans (via the API) can INSERT to the supervisor inbox.
- Each agent can only INSERT back to the supervisor (response path).
Who can invoke whom is a platform-enforced property, not a convention.

**Access/action privileges** — what an agent can do when invoked.
Enforced by:
- Container image (which binaries/tools exist)
- Approval tokens / rubric (what requires human sign-off per task)
- Queue permissions for sub-dispatch (which tool nodes can this agent call)

**Why the separation matters:**
These are independent axes. The fact that the supervisor can invoke the coder says nothing
about what the coder can do. The fact that the coder has git access says nothing about
who can invoke the coder. Conflating them is a common failure mode in agent frameworks.

**How agentq connects them:**
The RFC 8693 delegation chain links invocation to authorization:
- Human submits → supervisor queue (human authorized this invocation)
- Supervisor invokes agent → agent token carries delegation back to human
- Agent actions authorized by approval token (traces to human's original intent)

Invocation provenance (who dispatched this) and action authorization (what is permitted)
are both traceable to the same human authorization event. This is the novel combination.

**Documentation note:** This is a key design insight for the blog post and docs.
Relates to: agent-identity-framework (pillar 3: agent invocation as a permission),
worker-model-evolution (queue-as-capability), security-posture-honest.

## Specialist prompt_file — status: WIRED
prompt_file IS wired through agentq run → exec worker (WithPromptFile option).
config/prompts/coder.txt, researcher.txt, reviewer.txt all exist.
This was previously noted as backlogged but was already implemented.

No action needed here.

## agentq's role in the broader system
**agentq's job:** specify and implement how HTTP worker services are structured to run alongside eqlink.
The 'agent protocol' — what a worker endpoint looks like, request/response schema, artifact format,
how approval requests are signaled, how tool use is declared in artifacts.

**entroq's job:** the mesh infrastructure — k8s operator, queue server, eqlink sidecar, OPA ABAC,
SA-based identity, named response queues, audit logging (eqlink work item in entroq repo).

**Division:**
- entroq provides the mesh and enforces capabilities (queue ACLs via OPA, eqlink as transport)
- agentq defines what runs inside the mesh: worker protocol, supervisor logic, session model,
  human review flow, rubric evaluation, artifact accumulation, delegation chain

**Container image as capability enforcement:**
For subprocess tool use within a worker, the container image is the right enforcement primitive.
What's installed = what can be called. No queue-based microservice needed for simple tools.
OPA governs queue dispatch; container image governs subprocess tool use. Complementary layers.

**Implication for agentq development:**
The main agentq deliverable for k8s is the worker protocol spec + a reference implementation.
Workers are simple HTTP services. eqlink handles the queue interaction; the worker just
implements the protocol. Any language, any framework, as long as it speaks the protocol.

## Auditability design — three separate concerns, each owned by the right layer
**Queue journals are NOT a reliable audit log.**
- eqmem journal is periodically snapshotted away; only current state survives.
- Postgres WAL is crash-recovery infrastructure, not audit history.
- Queue = transport, not history. Current state only.

**Layer 1 — What was allowed: OPA, enforced at gRPC layer (real-time)**
Capability enforcement at the moment of the queue operation. Not logged history.
This is a structural guarantee, not a forensic record.

**Layer 2 — What was dispatched (queue level): eqlink audit logging → Loki**
eqlink sees every claim (inbox) and every response insertion (named response queue).
Named response queues (/ns/svc/response/<id>) are the natural correlation key.
Logs are per-instance and 'disorderly' but everything connects up by key.
k8s logging infrastructure (Loki) handles aggregation; query by response queue name or session ID.
Covers: all queue-based dispatches including tool-node hops that bypass the supervisor.
Does NOT cover: subprocess/container tool use within an agent hop (never touches the queue).
Work item: add audit logging to eqlink (entroq side).

**Layer 3 — What the agent did within its hop: artifact trail**
Subprocess/container tool use is invisible to eqlink.
Options:
  - Prompt-enforced: require agents to emit structured 'tools used' preamble in artifact (cheap, imperfect)
  - Sidecar capture: logging sidecar intercepts subprocess calls, attaches as separate artifact (reliable, more infra)
If queue-based tool nodes are used instead of subprocess, they get Layer 2 coverage for free.

**Summary:**
- 'What was allowed' != 'what happened' -- both tracked separately and explicitly.
- eqlink audit logging closes the queue-level dispatch record gap cleanly.
- Remaining gap is strictly the within-agent subprocess path.
- Full auditability on the subprocess path requires either prompt discipline or sidecar capture.

## Worker model evolution: subprocess → HTTP microservice mesh
**Dev path (unchanged):** subprocess-based workers (cmd: claude --print), flat queue names, dev.sh.

**K8s production path (in progress):**
eqlink sidecar claims from /ns/svc/inbox, calls worker HTTP endpoint with task payload,
puts result back. Everything looks synchronous to the agent (eqlink provides the facade).
Agent is a one-shot prompt machine, not a session-managing harness.

**Three node types (all handled homogeneously by the queue):**
1. Deterministic — code formatter, test runner, API caller, DB query. Input in, output out, no LLM.
2. Agentic — LLM-driven, produces artifacts, may request approval or further dispatch.
3. Composite — LLM agent whose tools are implemented as other mesh nodes.

**Tool-as-node vs. container constraints — resolved:**
Queue-based tool nodes are the right primitive for: tools needing their own SA identity,
expensive/shared tools (GPU inference, licensed software), tools shared across many agent types.
Container constraints are the right primitive for: subprocess tool use (git, test runner, formatter,
file read). Do NOT make every function a microservice — that is the granularity anti-pattern.
Default: container constraints. Escalate to queue-based tool node only when justified.

**Queue-as-capability model (non-transferable):**
An agent can USE the queues it has been granted INSERT on, but cannot GRANT queue access to
other agents — only OPA admin can. Capabilities are static at deploy time and visible in the
OPA document. Transitive use (coder → researcher → git) is intentional and visible in the
communication graph, not a loophole: researcher's git access was explicitly granted.

**Auditability:** queue journals are NOT reliable audit logs (see auditability-design memory).
agentq must own dispatch logging explicitly at the supervisor level.
Tool use within an agent hop requires prompt-enforced artifact structure or sidecar capture.

**Capability declaration in agents.yaml → OPA data:**
Proposed: each agent definition lists which tool queues it can dispatch to.
agentq translates this to OPA data at deploy time. Keeps capability set self-contained.

## Queue naming migration (concrete backlog item)
**Current state:** flat queue names — 'supervisor', 'human_review', 'coder_queue', 'reviewer_queue', 'researcher_queue'

**Target convention (from entroq k8s-native design):**
  /agentq/supervisor/inbox
  /agentq/human_review/inbox
  /agentq/coder/inbox
  /agentq/reviewer/inbox
  /agentq/researcher/inbox

Response queues are callee-owned (pending entroq sender.go:256 change):
  /agentq/coder/response/<...>
  /agentq/reviewer/response/<...>
  etc.

**Why:** The /ns/svc/inbox convention is required for entroq's label-based ABAC permission model to apply cleanly. The SA auto-grant covers /ns/svc/ prefix (own namespace). Without the convention, the k8s OPA provider cannot auto-grant correctly.

**Scope of change:**
- config/agents.yaml: rename queue fields
- config/entroq-policy/data.json: update queue names in permission matrix
- internal queue references in supervisor worker code (wherever 'supervisor', 'human_review' are hardcoded or configured)
- docs and README examples

**Timing:** Do after k8s deployment path is built (Phase 4 of release plan). Dev path (dev.sh + --no-auth) can keep flat names until then — no user-visible breakage. Do not do this before the blog post; old names are fine for the initial release story.

**Dependency:** Confirm entroq sender.go:256 reply queue change has landed before wiring response queue paths in supervisor.

## K8s-first: where to remove bespoke surface
Mapping agentq concepts to k8s primitives:
- Agent worker → Deployment/Pod (scale, health, restart = k8s, not agentq)
- Agent instance → Pod instance (log streaming = kubectl/Loki, not agentq)
- Agent identity → ServiceAccount (platform-enforced by entroq label-based ABAC)
- Queue → /ns/svc/inbox paths in entroq (see queue naming migration)
- Session → no k8s analog — agentq owns this
- Human review → no k8s analog — agentq owns this
- Delegation chain → no k8s analog — agentq owns this, highlight it
- **Communication graph → eqctl mesh graph reads OPA doc, outputs DOT/Mermaid. Do not build this in agentq.**

**Remove bespoke UI for:**
- Worker process status (→ k9s, Lens, kubectl)
- Log tailing (→ Loki/Grafana or kubectl)
- Scaling controls (→ HPA + kubectl)
- Health/readiness (→ k8s probes)
- Agent communication graph visualization (→ eqctl mesh graph, free from entroq)

**Keep bespoke for:**
- Session submit / track / result
- Human review (approve/reject with context)
- Delegation chain visualization (genuinely novel, not in eqctl)
- Rubric management
- Artifact viewer

**Near-term implication:** web UI should link out to k8s/Grafana for worker-level concerns, not embed them. A static 'view logs →' link in the worker card beats a bespoke log viewer.

**Reply queue ownership (flag — entroq design in progress):**
sender.go:256 change makes callee own its response queues (/ns/svc/response/...).
When this lands, agentq supervisor must CLAIM from /agentq/coder/response/ etc., not a caller-rooted path.
Check this assumption when wiring the k8s deployment path.

## Security posture: real vs. honest gaps
**Real and demonstrable:**
- Delegation chain: decode agent JWT, see act claim tracing back to human token. Verifiable.
- OPA enforcement: drop/misconfigure policy → API rejects. Testable.
- Supervisor as mandatory waypoint: agent-to-agent direct communication architecturally impossible, not convention.
- Auth required by default; --no-auth restricts to localhost only.
- **Queue ACLs: enforced in Zitadel-backed deployment.** agentq serve --authz-url wires OPA to entroq's gRPC layer. data.json defines the permission matrix. Only gap: data.json REPLACE_WITH_*_SUB placeholders must be filled after Zitadel bootstrap.
- **Queue ACLs on k8s: fully closed by entroq label-based ABAC (in progress in entroq).** SA JWT as identity + OPA enforced permission model. Supervisor INSERT-everywhere/CLAIM-own-inbox; agents CLAIM-own-inbox/INSERT-to-supervisor-inbox only. Platform-enforced, not application-convention.

**Workload attestation:**
- Non-k8s: SPIFFE/SPIRE documented as path, not wired. A compromised process can impersonate any agent type.
- **k8s (in progress): effectively closed.** K8s SA JWT + operator-maintained SA→labels mapping in OPA is SPIFFE-lite. Coder pod IS the coder because the platform binds its SA. No application code required.

**Token exchange / identity composition (k8s path):**
- SA proves 'I am the coder process' (platform-enforced).
- Task payload carries 'I was authorized by human X' (RFC 8693 delegation chain).
- These compose cleanly: identity and authorization are separated. Cleaner than machine-user token delegation.
- Blog post framing: SA-based identity + delegation chains = both principal identity and authorization provenance are structural.

**Known gaps (document clearly, not hide):**
- Per-invocation scoping: agent tokens are per-type, not per-invocation. Same scope for one-liner fix vs. full refactor. RFC 9396 RAR is the path.
- Artifact signing absent.

**The honest claim:** structural > conventional for implemented things; k8s deployment closes the remaining attestation and ACL gaps via platform primitives. Non-k8s path has documented gaps with known remediation paths.

## Test and UX script structure for initial release signal
**API behavioral tests (automated):**
1. Auth enforcement: bad JWT → 401; no token → 401; --no-auth + remote IP → 403
2. Session lifecycle: submit → poll status → verify terminal state
3. Rubric escalation: submit prompt designed to trigger escalation → verify awaiting_review
4. Review flow: escalated session → approve via API → verify continuation
5. Delegation chain: decode agent JWT mid-session → verify act claim present and correct
6. Concurrent sessions: submit N sessions, verify no artifact cross-contamination
7. Queue claim bypass: hit entroq directly → document whether ACL stops it (expect: no, document gap)

**UX walkthrough scripts (manual):**
1. Zero to first session — clone, docker-compose up, submit, watch, retrieve result. Note every friction point.
2. Review flow — submit escalatable task, find in Review tab, approve/reject, verify outcome.
3. Mid-flight visibility — submit long-running session, try to understand current state from UI. Note gaps.
4. Multi-agent chain — submit task that chains two agents, verify artifact accumulation.
5. Failure modes — submit malformed input, kill worker mid-session, observe recovery or lack thereof.

**Priority:** UX scripts first (surface k8s-handoff opportunities and rough edges before codifying in tests).

## Dogfooding use case tiers
Three dimensions to stress independently:
1. Orchestration correctness (routing, chaining, escalation)
2. Security structure (delegation chains real? chokepoint structural?)
3. Operator experience (visibility, intervention, trust)

**Tier 1 — Core flow (must work):**
- Code review: submit diff → coder reviews → result. Tests auto-approve path, artifact accumulation.
- Write feature: submit spec → coder → human review. Tests escalation, approval_suffix, full review loop.
- Multi-agent chain: researcher + writer. Tests supervisor chaining, not just single dispatch.
- Self-referential: use agentq to work on agentq. Meta-dogfood.

**Tier 2 — Edge cases (reveal structural gaps):**
- 5 concurrent sessions → queue isolation, no artifact cross-contamination
- Session that should be rejected by rubric → rubric enforcement is real
- Submit → cancel mid-flight → cancel endpoint gap
- Crafted bad JWT → auth actually enforcing
- Hit entroq queue server directly (bypass API) → document Queue ACL gap

## The premortem findings
Security dealbreakers identified and fixed this session:
- human_token was persisted in session document indefinitely -- fixed: cleared after first successful token exchange
- auth was silently optional (just a WARNING log) -- fixed: now requires --jwks-url or explicit --no-auth flag; --no-auth restricts to localhost only
- SIGHUP credential reload added for Vault Agent compatibility (NewTokenExchangerFromFiles)

Remaining gaps (not blockers for 0.1 but important):
- Queue ACLs not yet enforced -- entroq has per-queue permission support; needs wiring
- approved_actions field is an unenforced JSON convention -- Macaroons are the fix (see below)
- No workload attestation (SPIFFE/SPIRE is the documented path, not implemented)
- Artifact signing absent
- Token lifetimes probably too long (IDP configuration, not code)
- No README yet

## The security positioning against prior art
Prior art surveyed: Kafka/AMQP/NATS queue ACLs, SPIFFE/SPIRE, BeyondProd, NIST 800-207, NIST CISA ZTA maturity model, object-capability theory (Mark Miller, POLA), Zanzibar/OpenFGA, Macaroons (Google 2014), RFC 8693, RFC 9396 (RAR), GNAP, MCP OAuth.

What agentq is genuinely ahead on:
- RFC 8693 delegation chains: implemented, central, rare in agent frameworks
- Supervisor as structural chokepoint: sees intent + outcome simultaneously; structural, not optional
- Queue topology as auditable communication graph (BeyondProd-style)
- OPA as declarative PEP at every API request

The novel combination claim: queue-mediated communication + delegation chains + structural chokepoint = every action traceable to a human authorization decision, communication graph auditable by design. This combination does not exist in LangGraph/AutoGen/CrewAI or similar.

## The supervisor privilege framing (important for the blog)
The supervisor having broad issuance authority is CORRECT, not a flaw. Something in the system must be the root of issuance -- this is exactly what the Kubernetes API server and a CA do. The security property is not "supervisor has no privilege" but "supervisor privilege is bounded, visible, auditable, and everything below it has minimal privilege." Most agent-to-agent frameworks have quietly regressed to 1990s-era ambient authority. agentq restores the baseline sanity that distributed systems worked out decades ago. That is the blog story: not "we invented something new" but "we correctly applied what the industry learned, which agent frameworks have been ignoring."

The interesting demo: agents still do emergent, capable work within this architecture. Security and capability are not at odds here.

## Macaroons -- the next implementation target
approved_actions is currently a plain JSON field with no cryptographic backing. An attacker who can write to the supervisor queue can forge elevated permissions. Macaroons (Google 2014) fix this.

A Macaroon is a bearer token built as an HMAC chain. Start: HMAC(root_key, identifier). Add caveat: HMAC(current_key, caveat_string). The chain is one-way: you can add caveats (attenuate) but cannot remove them. Verification: replay the HMAC chain with all caveats; final HMAC must match.

Applied to agentq: supervisor issues a Macaroon when dispatching with elevated permissions. Caveats: session=X, action=write_files, expires=T+5min. Exec worker presents it to the API before elevating permissions. API verifies (replays HMAC chain, checks caveats not violated). Without a valid Macaroon signed by the supervisor, no elevation regardless of task payload.

Third-party caveat property (killer feature): a Macaroon can contain a caveat that must be discharged by a third-party service. "This approval is valid only if the human review service confirms session X is approved." This makes human-in-the-loop cryptographically enforced rather than conventional.

## Queue ACLs
entroq already has per-queue permission support in the API. The work is wiring it: supervisor credential gets write-only on agent queues; each agent credential gets read-only on its own queue. This makes the queue-as-authorization-boundary claim structural rather than architectural.

