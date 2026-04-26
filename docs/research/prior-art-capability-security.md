# Prior Art: Capability-Based Security

## The core idea

Capability-based security (object-capability model, "ocap") holds that the
*right to access a resource* should be represented as an *unforgeable token*
that you must possess to act on that resource. You cannot access what you
cannot name. Naming is authority.

This is in contrast to access control list (ACL) models, where a central
authority checks whether identity X is allowed to access resource Y. In ACL
systems, identity is the key; in capability systems, possession is the key.

---

## The Object-Capability Model (Mark Miller, E language)

Mark Miller's work, developed through the E programming language and later
Caja, establishes the theoretical foundation:

**Core properties (the "ocap invariants"):**
1. A computation can only affect the world through capabilities it holds
2. Capabilities can only be obtained by creation, by receiving them from
   another computation, or by being created with them
3. Capabilities can be attenuated (limited) when passed to others -- you can
   give someone a read-only version of something you have read-write access to

**The confused deputy problem** (which ocap solves directly): a privileged
component is tricked into using its authority on behalf of an unprivileged
caller. In ACL systems, the deputy checks the caller's identity; if it doesn't,
the caller gets the deputy's authority. In ocap systems, the caller must provide
the capability itself -- the deputy acts on what it receives, so if the caller
doesn't have the capability, they can't cause the deputy to act on it.

**Why this matters for agentq:**
The supervisor-as-dispatcher has this confused deputy problem latently. If an
agent can send a message to the supervisor that says "dispatch me to the
deployer queue with approved_actions=all", the supervisor might comply -- the
agent is exploiting the supervisor's authority to reach the deployer. In a
capability model, the supervisor would only dispatch to a queue for which it has
been handed a capability, and the capability to reach the deployer would not be
in the supervisor's possession unless it was issued by an authorized principal
for that specific task.

**The "rights amplification" problem** (relevant to emergent permissions): when
two capabilities are combined, the result should not exceed the authority of
either component. In practice this is hard to enforce because composition is
the whole point of building systems. Ocap theory provides a framework for
*thinking* about this but doesn't eliminate it.

**Reference:** Mark Miller's PhD thesis "Robust Composition: Towards a Unified
Approach to Access Control and Concurrency Control" (2006). The E language.
DARPA CRASH program research on ocap for security.

---

## Principle of Least Authority (POLA)

POLA is the operational expression of capability theory: each component should
hold only the authority needed to do its job, no more.

**The key insight beyond least privilege:** least *privilege* is about identity
("this user is not an admin"). Least *authority* is about capability ("this
process literally cannot reach the database because it doesn't hold the
database connection capability"). The distinction matters because privilege can
be escalated through confused deputy attacks; authority cannot be escalated if
you don't hold the capability.

**Ambient authority** is what POLA opposes: when a process can access resources
simply by being the right identity in the right context, rather than by holding
a specific capability. Traditional Unix file permissions, network ACLs, and
OAuth scopes are all forms of ambient authority -- you are allowed to access
because of who you are, not because of what you hold.

**For agentq:** agents currently operate with ambient authority to some extent.
The coder has a token that grants write access to the workspace repo; it can
use that authority at any time, for any task. The per-task vision (from NIST
tenet 3) would replace this with held authority: the coder receives a
capability for "write access to files matching this task's scope" alongside the
task payload, and that capability expires when the task is done.

---

## Capability Systems in Practice

**SeL4:** formally verified microkernel where all resource access is mediated by
capabilities. Foundational work on practical capability systems. Academic but
influential.

**Google Zanzibar (2019):** Google's global authorization system, now the basis
for several open-source projects (SpiceDB, OpenFGA, Warrant). Zanzibar is not
ocap in the strict sense but represents a highly sophisticated ACL system with
computed relationships ("user X has permission Y on resource Z because they are
a member of group G which has role R on organization O"). The key property:
permission checks are based on *relationship graphs*, not flat ACL lists. This
makes "emergent permissions" analysis possible -- you can query the graph to ask
"what is the union of all paths through which user X could reach resource Z?"

**OpenFGA (CNCF):** open-source Zanzibar implementation. Used by Okta, among
others. The authorization model is expressed as a schema over entities and
their relationships. This is worth examining for agentq because the
agent/session/task relationship graph is exactly the kind of thing Zanzibar-
style authorization excels at reasoning about.

**Reference:** Google Zanzibar paper (USENIX ATEC 2019), OpenFGA documentation.

---

## Capability Theory Applied to LLM Agents (recent, 2024)

Several papers have applied capability/ocap thinking specifically to LLM agents:

- **"Privilege-Separated Execution for LLM Agents"** (various, 2024): proposes
  running LLM agent tool calls in a restricted execution environment with
  explicitly granted capabilities. The LLM cannot access tools it hasn't been
  handed a capability for. Close to POLA applied to tool use.

- **"Controlling LLM Tool Use via Capability Scoping"**: similar framing, with
  the capability as a first-class object in the agent's context. The agent must
  present the capability when calling a tool; without it, the tool is not
  callable even if it technically exists.

These are research proposals more than deployed systems, but they directly
address question 1 from the premortem: "how do we ensure an agent can only do
what's needed at any given time?" The answer is: by giving it only the
capabilities for the current task, not a standing identity with broad scopes.

---

## Synthesis

Capability theory is the most rigorous intellectual framework for the problems
agentq is trying to address. The key concepts:

- **Confused deputy:** the supervisor has it; queue-level ACLs partially
  mitigate it; true capability passing would eliminate it
- **POLA:** current agentq has POLA at the agent identity level; lacks it at
  the task/invocation level
- **Ambient authority:** current tokens are ambient; per-task capabilities are
  the goal
- **Rights amplification / emergent permissions:** capability theory frames the
  problem clearly but doesn't solve it; composition is fundamentally hard

**The most actionable insight from capability theory for agentq:**
The `approved_actions` payload field is a weak capability. It's a claim ("the
supervisor approved these actions") not a cryptographically enforced capability
(a token that only works for these actions, that expires after this task). Replacing
it with a real capability -- a short-lived, scoped JWT or Macaroon that the agent
must present to act -- would be a meaningful step toward the ocap model.

**Macaroons** (Google, 2014) are worth examining here: bearer tokens with
embedded caveats ("this token is only valid for write operations on path X,
before time T, from IP Y"). They can be attenuated -- the supervisor issues a
full macaroon to itself, then attenuates it before handing it to the coder. The
coder's macaroon literally cannot authorize more than the supervisor specified.
This is attenuation in the ocap sense and directly addresses the confused deputy
problem.

**Reference:** "Macaroons: Cookies with Contextual Caveats for Decentralized
Authorization in the Cloud" (Google, 2014). The libmacaroons library.
