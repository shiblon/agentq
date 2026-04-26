# Prior Art: Workload Identity

## The core question

How does a process (not a human) prove who it is, in a way that is
cryptographically verifiable, does not require a pre-shared secret, and
supports rotation and revocation?

---

## SPIFFE / SPIRE

**SPIFFE** (Secure Production Identity Framework for Everyone) is the CNCF
standard for workload identity. Core idea: every workload gets a SPIFFE
Verifiable Identity Document (SVID), which is an X.509 certificate or JWT
encoding a URI of the form `spiffe://<trust-domain>/<path>`.

The path encodes the workload's identity: `spiffe://agentq.example/agent/coder`
would be the coder agent's identity. The certificate is short-lived (hours, not
years) and automatically rotated by the SPIRE agent running on the same node.

**SPIRE** is the reference implementation. It has:
- A server that acts as a CA and issues SVIDs
- An agent (daemonset/sidecar) on each node that attests workload identity and
  fetches SVIDs on their behalf
- Attestation plugins: the agent proves "this process is the coder container in
  the agentq namespace" via Kubernetes service account tokens, Docker metadata,
  or AWS/GCP instance metadata

**What this solves for agentq:**
- Secret zero: the SPIRE agent does node attestation using infrastructure
  credentials (k8s SA token, instance identity), so the workload itself never
  needs a pre-shared secret
- Automatic rotation: SVIDs expire and are reissued without process restart
- The SPIFFE ID is the authoritative identity used in mTLS and JWT assertions

**The connection to queue authorization:** If each agent's queue subscriber uses
its SVID for mTLS when connecting to the queue broker, the broker can enforce
ACLs based on SPIFFE ID. A SPIFFE-aware Kafka or NATS would then give you
queue-level ACLs tied to verified workload identity -- not just "this process
claims to be the coder" but "the SPIRE agent on this node attested this process
as the coder."

**What SPIFFE does not solve:**
- It is about identity, not authorization. SPIFFE tells you who a workload is;
  it says nothing about what it is allowed to do. OPA or a similar policy engine
  is still needed.
- It has no concept of dynamic scoping -- an SVID asserts a fixed identity, not
  a task-specific capability.
- The trust domain model assumes you control the infrastructure. For agentq
  deployed on user machines or mixed clouds, SPIRE setup is non-trivial.

**Reference:** SPIFFE spec (spiffe.io), CNCF SPIRE project, CNCF TAG Security
paper "Cloud Native Security Whitepaper."

---

## Google BeyondProd (2019)

BeyondProd is Google's internal model for service-to-service security in
production, documented in a 2019 paper (Google Security Blog / USENIX).

**Core claims:**
1. No inherent trust between services on the same network (no "trusted internal
   network" assumption)
2. Service identity is based on code identity, not network location
3. Every RPC is authenticated and authorized end-to-end
4. Mutual TLS everywhere, with certificates tied to service identity

**What's most relevant to agentq:**

The "code identity" concept: a service's identity is derived from what code it
is running (binary hash + deployment metadata), not what IP address it has.
This maps to the SPIFFE attestation model but Google implements it via their
internal job scheduler (Borg) which knows exactly what binary each service is
running.

The "service access policy" model: each service declares, in code, what other
services it is allowed to call. These declarations are reviewed and enforced.
This is a static declaration of the communication graph -- you can audit it, you
can diff it, you can detect when a new service-to-service dependency appears.

**The implicit claim for agentq:** if agents can only communicate through the
supervisor (by queue design), then the communication graph is implicitly
declared by the queue topology. The supervisor calls coder, reviewer, researcher
-- that's it. No coder-to-reviewer direct communication. This makes agentq's
communication graph auditable without a separate declaration step.

**What BeyondProd doesn't address:**
- Dynamic agents (new agent types added at runtime)
- AI-specific concerns (prompt injection, unexpected tool use)
- Per-task scoping -- BeyondProd is still per-service, not per-request

**Reference:** "BeyondProd: A New Approach to Cloud-Native Security" (2019),
Google Cloud Next talk on BeyondProd, the related "BeyondCorp" papers for the
human-identity equivalent.

---

## NIST SP 800-207 (Zero Trust Architecture)

The NIST ZTA document defines zero trust as: never trust, always verify, at
every access decision, regardless of network location.

**The seven tenets most relevant to agentq:**
1. All data sources and computing services are resources (agents are resources)
2. All communication is secured regardless of network location (queue traffic
   needs TLS/mTLS)
3. Access to individual resources is granted per-session (this is the per-task
   scoping goal)
4. Access is determined by dynamic policy including observable state of identity,
   application, and requesting asset
5. The enterprise monitors and measures integrity of all owned and associated
   assets
6. Authentication and authorization are dynamic and strictly enforced
7. The enterprise collects information about assets, network infrastructure, and
   communications to improve security posture

**Tenet 3 is the most interesting and hardest:** "per-session" in NIST terms
maps to "per-task" in agentq's terms. The supervisor granting a token for
"this dispatch of the coder to implement feature X" rather than "the coder agent
in general" is exactly tenet 3. Current agentq issues per-agent tokens, not
per-task tokens. OAuth RAR (see that file) is the mechanism for making tenet 3
practical.

**The Policy Decision Point / Policy Enforcement Point model:** NIST 800-207's
architecture has a PDP (makes the decision) and a PEP (enforces it at the
resource). In agentq: OPA is the PDP, the queue ACL layer is one PEP, the
agent's runtime permission check is another. These are wired in conceptually but
the PEP at the queue layer is not enforced yet.

**Reference:** NIST SP 800-207, NIST NCCoE ZTA implementation guide.

---

## Synthesis

SPIFFE/SPIRE solves the "how does a workload prove its identity" problem
elegantly and is infrastructure-level -- it doesn't require application-level
changes. BeyondProd demonstrates that a service's communication graph is itself
a security artifact worth declaring and auditing. NIST 800-207 provides the
framework for thinking about per-session (per-task) access decisions.

**Together they point at a cleaner version of agentq's security model:**
- Agent identity via SPIFFE SVIDs (no pre-shared secrets, automatic rotation)
- Queue ACLs enforced against SPIFFE IDs (structural, not application-level)
- Communication graph auditable from queue topology
- Per-task tokens via OAuth RAR for tenet 3 compliance

**What this implies agentq should shore up:**
- Document the SPIFFE/SPIRE integration path as the production identity story
  (replaces Zitadel machine users for infrastructure deployments, though Zitadel
  remains valid for simpler deployments)
- The queue ACL enforcement layer (currently absent in entroq) is a first-class
  security requirement, not an optional hardening step
- Per-task scoping is an open gap that no existing system makes easy; be honest
  about this rather than implying it's solved
