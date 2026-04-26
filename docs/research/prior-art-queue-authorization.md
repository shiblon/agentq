# Prior Art: Queue-Based Authorization

## The core idea we're examining

Can a message queue's access control layer serve as an authorization boundary —
not just a delivery mechanism — such that the set of messages an entity can send
or receive defines the boundary of its authority?

---

## Kafka

Kafka's authorization model (via `kafka.security.authorizer.AclAuthorizer` or
the newer `StandardAuthorizer`) operates on topics with per-principal ACLs. The
operations are: `Read`, `Write`, `Create`, `Delete`, `Describe`, `Alter`, and
`ClusterAction`. ACLs can be assigned per-topic or via prefix wildcards.

**What this gives you:**
- Producer identity: only principals with `Write` on a topic can produce to it
- Consumer identity: only principals with `Read` on a topic can consume from it
- Consumer groups: `Read` on a consumer group is a separate permission from
  `Read` on the topic, allowing fine-grained control over who can join a group

**Competing-consumer pattern:** When multiple workers share a consumer group on
a topic, Kafka's partition assignment means each message goes to exactly one
consumer. Combine that with ACLs and you get: only authenticated members of the
authorized consumer group can receive a given message. This is close to the
agentq queue-as-authorization-boundary idea.

**Gaps relative to agentq's needs:**
- ACLs are static and administratively managed. Kafka has no native notion of
  "issue a short-lived credential that grants temporary read access to this
  topic." Dynamic scoping per-task would require external machinery.
- The consuming process can still forward a message anywhere after receiving it.
  Kafka's boundary is at receipt, not at subsequent action.
- No native concept of the supervisor/dispatcher role -- any producer with Write
  can send to any topic they have access to.

**Key reference:** Kafka KIP-11 (ACL support), Confluent's security
documentation, and the Kafka protocol specification's authentication section.

---

## AMQP (RabbitMQ)

RabbitMQ's permission model has three orthogonal operations per vhost:
`configure` (declare/delete), `write` (publish), `read` (consume/bind).
Permissions are granted per-user per-vhost with regex matching on resource
names.

**What this gives you:**
- A user with `write` permission on `coder.*` can publish to any queue or
  exchange matching that pattern, but cannot consume from them
- A user with `read` permission on `coder_queue` and nothing else can only
  consume from that queue
- This maps cleanly to: supervisor has `write` on all agent queues, each agent
  has `read` only on its own queue

**More expressive than Kafka for this use case** because the read/write split is
explicit and asymmetric by design. You can configure the supervisor to be
write-only on agent queues without being able to claim tasks itself.

**Gaps:**
- Still static ACLs, no dynamic scoping
- No cryptographic binding between the message content and the authorization
  decision (a message payload claiming "this was approved by the supervisor" is
  not verified)

---

## NATS (JetStream)

NATS has a capability-oriented credential system based on NKeys (Ed25519 key
pairs) and decentralized JWTs. Accounts are isolated by default -- subjects
in one account are invisible to another unless explicitly exported/imported.

**Most relevant property:** subject-level export/import with `allow` lists means
that Account A can export `agent.coder.>` and Account B can only import that
specific subject hierarchy. Neither account can see the other's subjects unless
explicitly permitted. This is closer to the "you don't even know this queue
exists" property mentioned in agentq's design notes.

**The operator/account/user hierarchy** mirrors agentq's needs reasonably well:
- Operator: the agentq platform itself
- Account: an agent type (coder, reviewer, etc.)
- User: a specific instance/invocation

JetStream (NATS's durable stream layer) adds competing-consumer semantics via
consumer groups, with the same auth model applied.

**Why this is interesting for agentq:** NATS's decentralized JWT model is the
closest existing system to "queue access is itself a credential" -- the JWT
encodes which subjects a user can publish/subscribe to, and that JWT is
cryptographically signed by the account operator. The supervisor would hold
account-operator-level keys; agents would hold user-level JWTs scoped to their
queue.

**Reference:** NATS security documentation, nkeys, decentralized JWT auth model.

---

## entroq itself

entroq (the queue backend underlying agentq) does not currently have a
per-queue authorization model. The gRPC layer uses `WithInsecure()` in the
current agentq deployment, meaning any process that can reach the entroq server
can claim from any queue.

**What would be needed:** entroq would need queue-level ACLs similar to
RabbitMQ's read/write split, tied to the caller's authenticated identity (mTLS
client certificate or JWT). The claim operation would require `read` permission;
the insert/modify operation would require `write`.

This is an extension point, not a blocker -- the architecture is correct, the
enforcement layer is missing.

---

## Synthesis

The competing-consumer + ACL pattern is well-established (Kafka, RabbitMQ). The
"queue membership as authorization boundary" idea is not new. What is less
explored:

1. **Asymmetric read/write as the core security primitive** -- the supervisor
   can dispatch (write) but not execute (read from agent queues). This maps to
   RabbitMQ's model but has not been articulated as an agent security pattern.

2. **Dynamic credential scoping per queue** -- issuing short-lived credentials
   that grant access to a specific queue for a specific task, then revoking them.
   NATS's JWT model gets closest to this; Kafka and AMQP don't have it natively.

3. **Cryptographic binding of authorization to message content** -- the claim
   "this message was sent by an authorized dispatcher" should be verifiable by
   the consumer without trusting the queue infrastructure. This is largely
   unsolved in existing queue systems and is an open design question for agentq.

**Actionable for agentq:**
- Add queue-level ACLs to entroq (or document this as a production requirement
  with RabbitMQ/NATS as the recommended backing store)
- The supervisor's credential should have write-only permission on agent queues
- Agent credentials should have read-only permission on their own queue
- The NATS JWT model is worth studying as a pattern for per-task credential
  scoping
