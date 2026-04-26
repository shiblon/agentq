# Prior Art: Zero Trust Architecture

## BeyondCorp (Google, 2014)

The original BeyondCorp paper established that network location should not be a
trust signal. "Inside the corporate network" is not a security boundary; every
request must be authenticated and authorized regardless of where it originates.

**The device inventory model:** BeyondCorp maintains a database of every device
that is allowed to access corporate resources. Requests from unknown devices are
rejected even if they come from the correct user. This is the device posture
component of modern zero trust.

**For agents:** the analogous concept is a workload inventory. Before an agent
can make requests, it should be registered (its binary hash, its deployment
configuration, its intended scopes). Requests from unregistered agent workloads
should be rejected. SPIFFE attestation is the mechanism for this in cloud-native
environments.

---

## NIST SP 800-207 (Zero Trust Architecture, 2020)

Covered in the workload identity file for its seven tenets. Key additions here:

**The ZTA deployment variations:**
1. Identity-governed: the identity provider is the PDP; access is controlled
   entirely through identity and group membership
2. Micro-segmented: network segments are isolated; access requires explicit
   paths through policy-controlled gateways
3. Software-defined perimeter: the network path itself is established
   dynamically based on identity

For agentq, a micro-segmented model maps most naturally: agents live in
separate network segments (or at least separate queue namespaces), and the
supervisor is the policy-controlled gateway between them. Communication between
agents doesn't go through the network -- it goes through the supervisor queue.

**The continuous verification principle:** ZTA requires that access decisions
are made dynamically, not once at login. A long-running agent task should have
its authorization re-evaluated periodically. Short-lived tokens (with actual
short lifetimes) are the mechanism -- when the token expires, the agent must
re-authenticate, which gives the system an opportunity to deny re-authentication
if circumstances have changed.

---

## The Jericho Forum and De-Perimeterization (2003-2009)

The Jericho Forum (now part of The Open Group) published the concept of
de-perimeterization before BeyondCorp made it mainstream: as organizations
moved to cloud, SaaS, and mobile, the network perimeter became irrelevant.
Security had to be pushed to the data layer, not the network layer.

**"Secure the data, not the network"** is the Jericho thesis. Applied to agents:
secure the artifact, not the channel. An artifact that carries its authorization
context with it (who produced it, under what delegation, with what scopes) is
safer than relying on the queue channel being trusted.

This is an argument for signed artifacts: the coder's output should carry a
signature that the supervisor can verify before deciding to act on it. A
compromised coder that produces a malicious artifact should be detectable because
its signature is either absent or from the wrong key.

**This is not yet in agentq.** Artifact signing would close a real gap:
currently the supervisor trusts artifact content because it trusts the queue
it came from. With signed artifacts, a compromised queue or man-in-the-middle
would produce unverifiable artifacts.

---

## CISA Zero Trust Maturity Model (2023)

The US Cybersecurity and Infrastructure Security Agency published a maturity
model for ZTA adoption with five pillars: Identity, Devices, Networks,
Applications & Workloads, Data.

**The maturity levels** (Traditional → Initial → Advanced → Optimal) are useful
for characterizing where agentq sits:

| Pillar | agentq current state | Target |
|--------|---------------------|--------|
| Identity | Initial: OIDC tokens, 8693 delegation | Advanced: per-task scoping, short lifetimes |
| Devices | Traditional: no workload attestation | Initial: SPIFFE/SPIRE |
| Networks | Traditional: no queue ACLs | Initial: queue-level ACLs |
| Applications | Initial: OPA policy, authn middleware | Advanced: signed artifacts |
| Data | Traditional: unencrypted artifact store | Initial: encryption at rest |

This framing is useful for the blog post: agentq is at Initial/Advanced on
Identity (genuinely ahead of most agent frameworks) and Traditional on Devices
and Networks. Being honest about the maturity level is more credible than
claiming full ZTA compliance.

---

## Service Mesh (Istio, Linkerd)

Service mesh systems handle mTLS between microservices, with certificates
managed automatically (similar to SPIRE but at the infrastructure layer rather
than the application layer).

**What service mesh adds:**
- Automatic mTLS between services without application-level changes
- Traffic policies (service A can only call service B on specific paths)
- Observability (all traffic between services is recorded)

**For agentq:** a service mesh handles the transport security that agentq
currently lacks (entroq is plaintext). More interestingly, mesh traffic policies
could enforce the communication graph: the supervisor service can reach coder
service, but coder service cannot reach deployer service directly. This is
structural enforcement of the "all communication through supervisor" property
without queue-level ACLs.

**The limitation:** service mesh enforces service-to-service policies, not
message-level policies. It doesn't know that a specific task payload carries
approved_actions=all; it only knows that service X called service Y. For
agentq's needs, queue-level ACLs are more expressive than mesh traffic policies
because they operate on the message, not just the connection.

---

## Synthesis: What Zero Trust Thinking Tells Us

The ZTA literature converges on a few properties that agentq should eventually
have but currently lacks:

**Short-lived, scoped tokens re-issued frequently.** Current agent tokens last
as long as the IDP's default (typically hours). A task takes minutes. Agent
tokens should expire in minutes. This requires either per-task token issuance
(the full RAR vision) or at minimum configuring very short token lifetimes in
the IDP.

**Workload attestation.** An agent saying "I am the coder" should be verifiable
from infrastructure, not self-reported. SPIFFE/SPIRE is the mechanism. Without
it, a compromised process can claim any agent identity.

**Signed artifacts.** Artifacts should carry the producing agent's signature.
The supervisor should verify signatures before acting on artifact content.
Without this, a compromised queue or process can inject artifacts.

**Encrypted artifact storage.** Session artifacts containing code, data, or
analysis are stored in the entroq document store in plaintext. At minimum,
sensitive artifact content should be encrypted at rest.

**The honest positioning:** agentq implements the identity and authorization
layer of ZTA reasonably well (OIDC, OPA, delegation chains). It is at
Traditional maturity on workload attestation, network segmentation, and data
protection. These are not blockers for a 0.1 release but should be on the
roadmap and acknowledged in the security documentation.
