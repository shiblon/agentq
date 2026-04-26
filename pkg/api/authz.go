package api

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"strings"

	"github.com/open-policy-agent/opa/rego"
)

//go:embed policy/*.rego
var defaultPolicy embed.FS

// OPAAuthorizer evaluates access policy using an embedded OPA engine.
// Build with NewOPAAuthorizerFromPaths (production) or
// NewOPAAuthorizerFromModules (tests / programmatic policy).
type OPAAuthorizer struct {
	query rego.PreparedEvalQuery
}

// NewOPAAuthorizerFromPaths loads .rego files from the given filesystem paths
// and prepares the agentq.authz.allow query.
func NewOPAAuthorizerFromPaths(ctx context.Context, paths ...string) (*OPAAuthorizer, error) {
	r := rego.New(
		rego.Query("data.agentq.authz.allow"),
		rego.Load(paths, nil),
	)
	return prepareAuthorizer(ctx, r)
}

// NewOPAAuthorizerDefault builds an authorizer from the policy files embedded
// in the binary. Use NewOPAAuthorizerFromPaths to override with custom policy.
func NewOPAAuthorizerDefault(ctx context.Context) (*OPAAuthorizer, error) {
	entries, err := fs.ReadDir(defaultPolicy, "policy")
	if err != nil {
		return nil, fmt.Errorf("read embedded policy dir: %w", err)
	}
	opts := []func(*rego.Rego){rego.Query("data.agentq.authz.allow")}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".rego") {
			continue
		}
		src, err := fs.ReadFile(defaultPolicy, "policy/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded policy %s: %w", e.Name(), err)
		}
		opts = append(opts, rego.Module(e.Name(), string(src)))
	}
	return prepareAuthorizer(ctx, rego.New(opts...))
}

// NewOPAAuthorizerFromModules builds an authorizer from in-memory policy
// modules, keyed by an arbitrary filename used in error messages.
func NewOPAAuthorizerFromModules(ctx context.Context, modules map[string]string) (*OPAAuthorizer, error) {
	opts := []func(*rego.Rego){
		rego.Query("data.agentq.authz.allow"),
	}
	for name, src := range modules {
		opts = append(opts, rego.Module(name, src))
	}
	return prepareAuthorizer(ctx, rego.New(opts...))
}

func prepareAuthorizer(ctx context.Context, r *rego.Rego) (*OPAAuthorizer, error) {
	q, err := r.PrepareForEval(ctx)
	if err != nil {
		return nil, fmt.Errorf("prepare opa query: %w", err)
	}
	return &OPAAuthorizer{query: q}, nil
}

// Allow implements Authorizer. It builds an OPA input document from the
// request and any Claims stored in the context, then evaluates the policy.
func (a *OPAAuthorizer) Allow(ctx context.Context, r *http.Request) (bool, error) {
	input := buildOPAInput(r, ClaimsFromContext(ctx))
	rs, err := a.query.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return false, fmt.Errorf("opa eval: %w", err)
	}
	return rs.Allowed(), nil
}

// buildOPAInput constructs the input document passed to OPA on every request.
//
// Shape:
//
//	{
//	  "method":    "POST",
//	  "path":      ["api", "v1", "sessions"],
//	  "principal": {
//	    "sub":          "user_abc",
//	    "is_agent":     true,
//	    "delegated_by": "user_xyz"
//	  }
//	}
//
// principal is nil when authnMiddleware has not run (i.e. no valid token).
func buildOPAInput(r *http.Request, claims *Claims) map[string]any {
	path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")

	input := map[string]any{
		"method": r.Method,
		"path":   path,
	}

	if claims != nil {
		input["principal"] = map[string]any{
			"sub":          claims.Subject,
			"is_agent":     claims.IsAgent,
			"delegated_by": claims.DelegatedBy,
		}
	}

	return input
}
