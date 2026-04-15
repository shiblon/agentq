# deploy/policy.rego -- default agentq API authorization policy.
#
# This policy is evaluated by an OPAAuthorizer (see pkg/api/middleware.go).
# Input shape:
#   input.method  -- HTTP method, e.g. "GET"
#   input.path    -- URL path segments, e.g. ["api", "v1", "sessions"]
#   input.user    -- JWT claims map, e.g. {"sub": "alice", "roles": ["operator"]}
#
# The default policy allows everything. Uncomment and adapt the examples
# below to restrict access once you have authentication in place.

package agentq.api

import rego.v1

# Default: allow all requests.
default allow := true

# --------------------------------------------------------------------------
# Example: require authentication for all write operations.
# --------------------------------------------------------------------------
# allow if {
#     input.method == "GET"
# }
# allow if {
#     input.method != "GET"
#     count(input.user) > 0   # any authenticated user
# }

# --------------------------------------------------------------------------
# Example: restrict destructive operations to the "admin" role.
# --------------------------------------------------------------------------
# allow if {
#     input.method in {"POST", "DELETE"}
#     "admin" in input.user.roles
# }

# --------------------------------------------------------------------------
# Example: allow operators to read everything and submit sessions,
#          but only admins can manage agents or process reviews.
# --------------------------------------------------------------------------
# allow if {
#     input.method == "GET"
#     "operator" in input.user.roles
# }
# allow if {
#     input.method == "POST"
#     input.path[2] == "sessions"
#     "operator" in input.user.roles
# }
# allow if {
#     "admin" in input.user.roles
# }
