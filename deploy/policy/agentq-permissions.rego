# Default permissions for agentq.
#
# Humans: permitted to perform any action.
# Agents: permitted to submit tasks and read sessions, but not to approve
#         reviews or perform administrative actions.
#
# Override this package in a separate .rego file to narrow or extend access.
package agentq.permissions

import rego.v1

# Humans are always permitted (coarse default; tighten per-route as needed).
permitted if {
	not input.principal.is_agent
}

# Agents may submit sessions and read their own session state.
permitted if {
	input.principal.is_agent
	allowed_agent_methods[input.method]
	allowed_agent_paths
}

allowed_agent_methods := {"GET", "POST"}

# Agents are restricted to session and queue endpoints.
allowed_agent_paths if {
	input.path[0] == "api"
	input.path[1] == "v1"
	allowed_agent_resources[input.path[2]]
}

allowed_agent_resources := {"sessions", "queues"}
