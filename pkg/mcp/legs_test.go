package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	mcplib "github.com/mark3labs/mcp-go/mcp"
)

// TestFlow_ConfigToEnforcement walks the whole path a capability takes: leg
// names as they appear in agents.yaml, through minting, back out of a parsed
// token, into what tools/list shows, and finally into what a call is allowed
// to do. Nothing anywhere in it names a tool.
func TestFlow_ConfigToEnforcement(t *testing.T) {
	priv, pubSet := testKeyPair(t)

	// 1. A reviewer's ceiling, as written in configuration.
	ceiling, err := ParseLegs([]string{"untrusted", "private"})
	if err != nil {
		t.Fatalf("ParseLegs: %v", err)
	}

	// 2. Minting refuses anything over the cap, so this is the one place the
	//    rule of two is enforced rather than described.
	tok, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "doc:sessions/flow",
		Workdir:   t.TempDir(),
		Legs:      ceiling,
	})
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}

	// 3. The server recovers the same legs from the signed token.
	got, err := Parse(pubSet, "agentq", tok)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Legs != ceiling {
		t.Fatalf("round-tripped legs = %s, want %s", got.Legs, ceiling)
	}

	// 4. The usable tool set is derived from those legs, never enumerated.
	usable := GrantableWith(got.Legs)
	for _, want := range []string{"read_file", "grep", "git_log"} {
		if !slices.Contains(usable, want) {
			t.Errorf("%q should be usable with %s; got %v", want, got.Legs, usable)
		}
	}
	for _, unwanted := range []string{"write_file", "git_push", "git_commit"} {
		if slices.Contains(usable, unwanted) {
			t.Errorf("%q should not be usable with %s", unwanted, got.Legs)
		}
	}

	// 5. A call is checked against the same rule that decided visibility.
	ctx := context.WithValue(context.Background(), claimsContextKey{}, got)
	called := false
	handler := withLegCheck("write_file", func(context.Context, mcplib.CallToolRequest) (*mcplib.CallToolResult, error) {
		called = true
		return mcplib.NewToolResultText("ran"), nil
	})
	res, err := handler(ctx, mcplib.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if called {
		t.Error("write_file ran in a session without the mutate leg")
	}
	if msg := resultText(res); !strings.Contains(msg, "needs private,mutate") {
		t.Errorf("refusal should name the legs required; got %q", msg)
	}
}

// TestMint_RefusesTheTrifecta is the invariant the whole design rests on: no
// signed token may carry all three legs, whatever the caller assembled.
func TestMint_RefusesTheTrifecta(t *testing.T) {
	priv, _ := testKeyPair(t)

	_, err := Mint(priv, Claims{
		Issuer:    "agentq",
		SessionID: "doc:sessions/greedy",
		Workdir:   t.TempDir(),
		Legs:      Legs(Untrusted, Private, Mutate),
	})
	if err == nil {
		t.Fatal("Mint signed a token granting all three legs")
	}
	if !strings.Contains(err.Error(), "exceeds the maximum") {
		t.Errorf("error should explain the cap; got %v", err)
	}
}

// TestIsolatedToolsAreUnregistered checks that the tools carrying every leg
// stay out of the served set. They are kept in the tree deliberately: each
// becomes grantable once the runner can drop a leg for it.
func TestIsolatedToolsAreUnregistered(t *testing.T) {
	registered := make(map[string]bool)
	for _, tool := range AllTools() {
		registered[tool.Tool.Name] = true
	}
	for _, name := range isolatedTools {
		if registered[name] {
			t.Errorf("%q is registered but no session may hold its legs", name)
		}
		legs, ok := LegsFor(name)
		if !ok {
			t.Errorf("%q is in isolatedTools but has no leg tag", name)
			continue
		}
		if err := legs.Valid(); err == nil {
			t.Errorf("%q is in isolatedTools but its legs (%s) fit the cap", name, legs)
		}
	}
}

// TestEveryRegisteredToolIsTagged is the forcing function: adding a tool
// without saying what it costs fails here rather than at first call.
func TestEveryRegisteredToolIsTagged(t *testing.T) {
	if err := validateToolLegs(AllTools()); err != nil {
		t.Error(err)
	}
	if err := validateToolLegs(AllDevTools()); err != nil {
		t.Error(err)
	}
}

func TestLegSet_ContainsIsAttenuationOnly(t *testing.T) {
	parent := Legs(Untrusted, Private)

	for _, child := range []LegSet{Legs(), Legs(Private), Legs(Untrusted), parent} {
		if !parent.Contains(child) {
			t.Errorf("%s should be reachable by narrowing %s", child, parent)
		}
	}
	for _, child := range []LegSet{Legs(Mutate), Legs(Private, Mutate)} {
		if parent.Contains(child) {
			t.Errorf("%s must not be reachable by narrowing %s", child, parent)
		}
	}
}

func TestParseLegs_Errors(t *testing.T) {
	for _, names := range [][]string{
		{"untrusted", "untrusted"},
		{"mutant"},
		{""},
	} {
		if _, err := ParseLegs(names); err == nil {
			t.Errorf("ParseLegs(%v) should have failed", names)
		}
	}
}
