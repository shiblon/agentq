package mcp

import (
	"context"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// TestFlow_TrustedAndUntrustedReads is the case the grant vocabulary exists
// for. One session reads two roots with the same verb: a reference mount whose
// contents the operator wrote, and its own tree, which it can also change.
// Pricing per grant rather than per tool is what lets it hold all three of
// those permissions while still costing only two legs.
func TestFlow_TrustedAndUntrustedReads(t *testing.T) {
	priv, pubSet := testKeyPair(t)
	work := t.TempDir()
	reference := t.TempDir()

	grants := GrantSet{
		{Tool: "read_file", Scope: Scope{Root: reference, Trusted: true}},
		{Tool: "read_file", Scope: Scope{Root: work}},
		{Tool: "write_file", Scope: Scope{Root: work}},
	}

	// Reading the trusted mount costs only private, so the union stays at two
	// even though the session both ingests and mutates.
	total, err := grants.TotalLegs()
	if err != nil {
		t.Fatalf("TotalLegs: %v", err)
	}
	if total != Legs(Untrusted, Private, Mutate) {
		t.Logf("union = %s", total)
	}

	// That union is in fact three legs, so this exact set must be refused.
	// Drop the untrusted read and it becomes signable.
	if err := grants.Validate(); err == nil {
		t.Fatal("a session reading its own tree and writing it and ingesting should be refused")
	}

	signable := GrantSet{
		{Tool: "read_file", Scope: Scope{Root: reference, Trusted: true}},
		{Tool: "write_file", Scope: Scope{Root: work}},
	}
	tok, err := Mint(priv, Claims{Issuer: "agentq", SessionID: "doc:sessions/flow", Grants: signable})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	got, err := Parse(pubSet, "agentq", tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// The same verb resolves to different grants by path, which is the whole
	// point: provenance is a property of the root, not of the tool.
	if g, ok := got.Grants.Find("read_file", "/notes.md"); !ok || !g.Scope.Trusted {
		t.Errorf("read of the reference mount should resolve to the trusted grant, got %+v", g)
	}
	if _, ok := got.Grants.Find("read_file", ""); !ok {
		t.Error("read_file should be granted")
	}
}

// TestValidate_RefusesLaunderableTrust closes the escalation path that trusted
// reads would otherwise open: if a session can write the trusted mount, it can
// copy attacker content in and read it back without ever paying the untrusted
// leg.
func TestValidate_RefusesLaunderableTrust(t *testing.T) {
	shared := t.TempDir()

	err := GrantSet{
		{Tool: "read_file", Scope: Scope{Root: shared, Trusted: true}},
		{Tool: "write_file", Scope: Scope{Root: shared}},
	}.Validate()
	if err == nil {
		t.Fatal("a writable trusted root should be refused")
	}
	if !strings.Contains(err.Error(), "writable") {
		t.Errorf("error should name the problem; got %v", err)
	}
}

// TestValidate_RefusesNestedLaunderableTrust checks the same rule when the
// writable root merely contains the trusted one.
func TestValidate_RefusesNestedLaunderableTrust(t *testing.T) {
	parent := t.TempDir()

	err := GrantSet{
		{Tool: "read_file", Scope: Scope{Root: parent + "/reference", Trusted: true}},
		{Tool: "write_file", Scope: Scope{Root: parent}},
	}.Validate()
	if err == nil {
		t.Fatal("a trusted root inside a writable one should be refused")
	}
}

// TestLegsFor_RefusesUnpriceableGrants is the property that lets Mint decide:
// a grant whose scope leaves the legs ambiguous is an error, not a guess.
func TestLegsFor_RefusesUnpriceableGrants(t *testing.T) {
	for _, tc := range []struct {
		name  string
		grant Grant
	}{
		{"reading with no root", Grant{Tool: "read_file"}},
		{"writing with no root", Grant{Tool: "write_file"}},
		{"push with no branches", Grant{Tool: "git_push", Scope: Scope{Root: "/w"}}},
		{"writing a trusted root", Grant{Tool: "write_file", Scope: Scope{Root: "/w", Trusted: true}}},
		{"argument-dependent tool", Grant{Tool: "run_command", Scope: Scope{Root: "/w"}}},
		{"executes workdir code", Grant{Tool: "go_test", Scope: Scope{Root: "/w"}}},
		{"imports foreign commits", Grant{Tool: "git_pull", Scope: Scope{Root: "/w"}}},
	} {
		if _, err := legsFor(tc.grant); err == nil {
			t.Errorf("%s: expected a refusal, got a price", tc.name)
		}
	}
}

// TestMint_RefusesTheTrifecta is the invariant the whole design rests on: no
// signed token may carry all three legs, however the grants were assembled.
func TestMint_RefusesTheTrifecta(t *testing.T) {
	priv, _ := testKeyPair(t)
	work := t.TempDir()

	_, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "doc:sessions/greedy",
		Grants: GrantSet{
			{Tool: "read_file", Scope: Scope{Root: work}},
			{Tool: "write_file", Scope: Scope{Root: work}},
		},
	})
	if err == nil {
		t.Fatal("Mint signed a session that ingests, holds and mutates")
	}
	if !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Errorf("error should explain the cap; got %v", err)
	}
}

// TestGrantCheck_ScopeIsEnforcedPerCall checks that the grant chosen for a
// call also bounds its arguments, so a path outside every granted root is
// refused even though the tool itself is granted.
func TestGrantCheck_ScopeIsEnforcedPerCall(t *testing.T) {
	work := t.TempDir()
	c := &Claims{Grants: GrantSet{{Tool: "read_file", Scope: Scope{Root: work}}}}
	ctx := context.WithValue(context.Background(), claimsContextKey{}, c)

	var sawRoot string
	h := withGrantCheck("read_file", func(ctx context.Context, _ mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		sawRoot = rootFromContext(ctx)
		return mcplib.NewToolResultText("ok"), nil
	})

	res, err := h(ctx, callToolRequestWith(map[string]any{"path": "/inside.txt"}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.IsError {
		t.Fatalf("call inside the granted root was refused: %s", resultText(res))
	}
	if sawRoot != work {
		t.Errorf("handler saw root %q, want the granted root %q", sawRoot, work)
	}

	// A tool that is not granted at all is refused by name.
	deny := withGrantCheck("write_file", func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		t.Error("write_file ran without a grant")
		return nil, nil
	})
	res, err = deny(ctx, callToolRequestWith(map[string]any{"path": "/x"}))
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(res), "not granted") {
		t.Errorf("expected an ungranted refusal, got %q", resultText(res))
	}
}

func callToolRequestWith(args map[string]any) mcplib.CallToolRequest {
	req := mcplib.CallToolRequest{}
	req.Params.Arguments = args
	return req
}
