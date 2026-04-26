# Core authorization policy for agentq.
#
# The allow rule combines principal identity (from authn) with the
# permissions package, which maps principals to allowed operations.
# To customize access control, override rules in agentq-permissions.rego.
package agentq.authz

import rego.v1

import data.agentq.permissions

# deny collects reasons a request should be rejected.
# Any non-empty deny set blocks the request regardless of allow.
deny contains "no authenticated principal" if {
	not input.principal
}

deny contains "agents may not approve or reject reviews" if {
	input.principal.is_agent
	input.path[2] == "review"
	input.method == "POST"
}

default allow := false

allow if {
	count(deny) == 0
	permissions.permitted
}
