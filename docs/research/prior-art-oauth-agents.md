# Prior Art: OAuth, Token Exchange, and Agent Authorization

## The baseline: what OAuth/OIDC already gives us

OAuth 2.0 and OIDC were designed for delegated human authorization ("the user
authorizes this app to act on their behalf"). They work well for that. Agents
strain the model in specific ways worth understanding precisely.

---

## RFC 8693: Token Exchange

**What it does:** allows a client to exchange one token for another, where the
new token may have a different subject, different scopes, or a different
audience. The `act` claim in the resulting token records the delegation chain.

**How agentq uses it:** the supervisor presents the human's access token and
exchanges it for a delegated agent token. The agent token has `sub` = human,
`act.sub` = supervisor, meaning "the supervisor is acting on behalf of this
human." Each dispatched agent inherits this delegation context.

**What 8693 does well:**
- Establishes the delegation chain cryptographically
- The `may_act` claim allows the IDP to pre-authorize which principals can
  exchange on behalf of which subjects -- the supervisor can only exchange if
  the IDP policy allows it
- The `audience` restriction (`aud`) can limit the exchanged token to specific
  services -- a coder token can be restricted to the workspace API

**What 8693 does not do:**
- Per-task scoping: the exchanged token has fixed scopes determined by the
  IDP's client configuration, not by the specific task
- Attenuation: the supervisor cannot issue a token with *fewer* scopes than
  the IDP grants the agent's client; it can only exchange for what the IDP
  allows
- Short-lived enough by default: most IDP configurations issue tokens valid
  for hours; per-task tokens should probably expire in minutes

**The core gap:** 8693 is per-agent, not per-invocation. Every time the
supervisor dispatches the coder, the coder gets the same token regardless of
what it's being asked to do.

---

## RFC 9396: Rich Authorization Requests (RAR)

**What it does:** allows an OAuth client to specify a structured authorization
request -- not just scopes ("read", "write") but rich objects describing exactly
what it needs ("write access to file X in repository Y for the purpose of
implementing task Z").

**Why this is directly relevant:** RAR is the mechanism for per-task scoping
in the OAuth model. Instead of requesting `scope=repo:write`, the supervisor
would request:

```json
{
  "type": "agentq_task",
  "task_id": "abc123",
  "agent": "coder",
  "allowed_paths": ["src/foo.go", "src/bar.go"],
  "expires_in": 300
}
```

The IDP evaluates this against policy and issues a token encoding these specific
authorizations. The coder's token then *cannot* write to files outside the
allowed paths, by token design, not just by convention.

**Current adoption:** RAR is a 2023 RFC and not yet widely implemented. Keycloak
has partial support. Zitadel does not yet implement RAR fully. This is a near-
future capability, not a today capability.

**Implication for agentq:** the architecture should be designed so RAR can be
plugged in when IDPs support it. The `approved_actions` field in the task
payload is a weak precursor to this -- it's a convention, not an enforcement
mechanism. When RAR is available, `approved_actions` becomes the input to a RAR
request rather than a flag passed to the subprocess.

---

## OAuth 2.0 for Machine-to-Machine (Client Credentials Flow)

The client credentials flow (`grant_type=client_credentials`) is the correct
OAuth flow for agents: a client authenticates with its own credentials (client
ID + secret or mTLS client certificate) and receives an access token for its
own scopes, not delegated from a user.

**This is what Zitadel machine users use.** A machine user is an OAuth client
that authenticates via client credentials and gets tokens scoped to its
configured roles/scopes.

**What this gives you:**
- Each agent type is a distinct OAuth client with distinct scopes
- Tokens are issued to agents without a human being present
- Scopes are controlled by IDP policy per client

**What it doesn't give you:**
- Delegation chain (no `act` claim -- you need 8693 for that)
- Per-task scoping (scopes are fixed per client configuration)

For background agents (sessions submitted without an active human) this is the
right flow. For human-initiated sessions, 8693 delegation is better because it
preserves the human's identity in the audit trail.

---

## GNAP (Grant Negotiation and Authorization Protocol)

GNAP (draft RFC, working group active as of 2024) is a proposed successor to
OAuth 2.0 designed to address its limitations:
- Richer interaction models (the client and AS can negotiate)
- First-class support for multiple access tokens in one request
- Better support for delegated authorization and split trust

**Most relevant property for agents:** GNAP's "split authorization" model
allows a client to request tokens for multiple resources with different scopes
in a single interaction. A supervisor could request, in one GNAP interaction:
"I need a token for the workspace API with write access to session branch X,
AND a token for the artifact store with write access to session prefix Y." The
AS evaluates both requests against policy simultaneously.

**Adoption status:** GNAP is still draft and not production-ready in any major
IDP as of 2024. Worth watching, not worth building on yet.

---

## The Model Context Protocol (MCP) Authorization (Anthropic, 2024)

MCP is Anthropic's protocol for connecting LLMs to tools. The 2024 spec added
an OAuth-based authorization layer: MCP servers can require OAuth tokens, and
the MCP client (Claude or another LLM host) handles the authorization flow.

**What this solves:** tool-level authorization. An MCP server that wraps a
filesystem API can require a token with `files:write` scope before allowing
writes. The LLM's tool call is gated by the token it has.

**The gap relative to agentq:** MCP authorization is at the tool invocation
level, not at the agent communication level. It says nothing about which agents
can dispatch to which other agents, or about the delegation chain from human
to agent to tool. It's a layer above what agentq addresses, not a replacement.

**The interesting combination:** an agentq exec worker that invokes Claude via
MCP could use the agent's delegated token to authenticate to MCP servers --
preserving the human's identity all the way down to the tool call. The chain:
human → supervisor (via 8693) → exec worker (via agent token) → MCP server
(via agent token presented to MCP OAuth). This is fully traceable.

---

## Synthesis

The OAuth ecosystem gives agentq most of what it needs for today:
- 8693 for delegation chains (implemented)
- Client credentials for background agents (available, not yet a separate path)
- Audience restrictions for token scoping to specific services (available, not
  yet configured)

The gaps that are real but addressable near-term:
- Token lifetime: agent tokens should be much shorter-lived than typical
  OAuth defaults (minutes, not hours)
- Audience claims: tokens should specify which queue/service they are valid for
- The MCP integration path for tool-level authorization

The gap that is real and not near-term:
- Per-task scoping via RAR -- the architecture supports it, the IDPs don't yet

**The honest claim agentq can make:** the delegation chain from human to
supervisor to agent is implemented and cryptographically traceable via RFC 8693.
The per-task scoping gap is real and acknowledged; RAR is the path to closing
it. The architecture is designed to plug RAR in when IDPs support it.
