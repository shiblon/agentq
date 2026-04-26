# agentq Security Positioning

## What we're claiming

We are not claiming to have invented new security primitives. We are claiming
that agentq assembles existing, well-understood primitives in a combination
that:

1. Is not present in existing agent orchestration frameworks
2. Makes agent security properties *structural* rather than *conventional*
3. Points at a credible path toward genuinely verifiable agent authorization

The Bitcoin analogy: no new cryptography, but the combination enables something
that wasn't achievable before.

---

## Where agentq is genuinely ahead

**RFC 8693 delegation chains are implemented and central.**
Most agent frameworks have no concept of a delegation chain from human to
agent. agentq's architecture makes this first-class: the supervisor exchanges
the human's token for agent tokens that carry the delegation context. Every
agent action is traceable back to a human authorization decision. This is
not common.

**The supervisor as structural chokepoint for intent.**
The queue architecture forces all agent-to-agent communication through the
supervisor. This is not just a reliability property -- it means the supervisor
sees both intent (original prompt) and outcome (artifacts) simultaneously,
creating the structural possibility of detecting intent-vs-outcome drift. In
conversational agent frameworks, there is no mandatory waypoint; actions
accumulate without any component having the full picture.

**Queue topology as an auditable communication graph.**
Because agents can only receive work from the supervisor via their specific
queue, the set of possible agent interactions is defined by the queue topology.
This is a form of BeyondProd's "declared communication graph" -- you can
audit who talks to whom from the queue configuration, not from reading
application code.

**OPA as the policy enforcement point.**
The Authorizer interface wires in OPA at every API request. The policy is
declarative, auditable, and replaceable without code changes. The human/agent
distinction in the `act` claim is enforced at the policy layer.

---

## Where agentq is honest about gaps

**Per-task scoping is not implemented.**
Agent tokens are per-agent-type, not per-invocation. The coder has the same
scopes whether it's making a one-line fix or a full refactor. RFC 9396 (RAR)
is the path to per-task scoping; IDPs don't widely support it yet. The
`approved_actions` payload field is a weak precursor -- a convention, not an
enforcement mechanism.

**Queue ACLs are not enforced.**
The queue-as-authorization-boundary is an architectural property but not yet
a security enforcement. Any process that can reach the entroq server can claim
from any queue. Enforcing queue ACLs (supervisor can write to agent queues but
not read from them; each agent can only read from its own queue) requires
work at the entroq layer. This is a real gap.

**Workload attestation is absent.**
An agent claiming to be the coder is taken at its word. SPIFFE/SPIRE is the
path to verifiable workload identity; it is documented as a production
integration but not implemented. Without attestation, a compromised process
can impersonate any agent.

**Artifact signing is absent.**
Artifact content is trusted because it came from the correct queue. A
compromised queue or process could inject unsigned artifacts. Signed artifacts
(each agent signs its output, the supervisor verifies before acting) would
close this gap. Not yet implemented.

**The supervisor is still a high-privilege component.**
The supervisor can dispatch to all agent queues and perform token exchange on
behalf of any agent. Its privilege is issuance privilege (it can vouch for
agents) rather than action privilege (it does not directly execute agent
work), which is a meaningful distinction. But it remains the highest-privilege
component in the system, and the queue ACL gap means its write-only constraint
is not yet enforced.

---

## The novel combination

The combination that is worth articulating:

> **Queue-mediated communication + delegation chains + structural chokepoint
> creates an agent architecture where every action is traceable to a human
> authorization decision, and the communication graph between agents is
> auditable by design.**

This combination does not exist in LangGraph, AutoGen, CrewAI, or other
orchestration frameworks as of the time of writing. Those frameworks focus on
capability and reliability; security is an afterthought.

The academic claim: agentq implements an early form of the object-capability
model applied to agent orchestration. The queue is the capability. The
supervisor is the capability issuer. Agents can only act on work they receive
through the queue, and they can only receive work through authorized channels.
The attenuation property (the capability theory term for issuing a restricted
version) is approximated by `approved_actions` and will be fully implemented
when per-task token scoping via RAR is available.

---

## What would strengthen the claims

In priority order:

1. **Queue ACL enforcement** -- makes the communication graph claim structural
   rather than aspirational. Relatively tractable (entroq extension or NATS
   migration).

2. **Short token lifetimes** -- configure IDP to issue agent tokens with
   5-10 minute lifetimes. No code change, just IDP configuration. Makes
   the "per-task effectively" argument more credible.

3. **Audience claims on agent tokens** -- restrict each agent's token to the
   specific API endpoint it needs. One IDP configuration change.

4. **SIGHUP credential rotation** -- implemented (Vault Agent compatibility).

5. **Artifact signing** -- meaningful but complex; a pre-1.0 milestone, not a
   0.1 requirement.

6. **SPIFFE/SPIRE documentation** -- describe the production integration path
   even if it's not in the default docker-compose. This makes workload
   attestation a documented option, not a gap.

---

## What to look up before finalizing the blog post

These are gaps in my knowledge that need verification:

- Whether any recent (2024-2025) agent framework has engaged seriously with
  delegation chains or capability-based security. Search: "LLM agent
  authorization RFC 8693" and "agent orchestration capability security."

- The CNCF TAG Security whitepaper on AI/ML workloads -- may have specific
  guidance on agent security that post-dates this document.

- Whether OpenFGA / SpiceDB has been applied to agent authorization in any
  published work. The relationship graph model maps well to
  agent/session/task.

- Any academic work from 2024 on "privilege-separated LLM agents" -- the
  capability theory applications to LLMs may have advanced significantly.
