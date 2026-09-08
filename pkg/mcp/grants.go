package mcp

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Grant is one permission in a capability token: a tool, plus the slice of
// that tool's argument space the session may use it on. Legs are a property
// of the pair, not of the tool: reading a mount with known provenance costs
// less than reading a tree an attacker can write to, and the same verb does
// both.
type Grant struct {
	// Tool is the MCP tool name this grant permits.
	Tool string `json:"tool"`

	// Scope narrows the arguments the tool may be called with. Only the
	// fields meaningful to Tool are read; legsFor rejects a grant whose
	// scope leaves the legs ambiguous.
	Scope Scope `json:"scope,omitempty"`
}

// Scope constrains a grant's argument space. Each tool reads the fields that
// apply to it and legsFor errors on the rest, so an unconstrained grant of an
// argument-dependent tool is refused rather than guessed at.
type Scope struct {
	// Root is the filesystem root a path-taking tool is confined to. Paths
	// the agent supplies are resolved beneath it, as the agent's "/".
	Root string `json:"root,omitempty"`

	// Trusted marks content under Root as having known provenance: written
	// by the operator rather than by anything the session could influence.
	// Reading it does not cost the untrusted leg.
	//
	// Mint refuses a token where a trusted Root is also writable, since that
	// would let a session launder attacker content into the trusted zone and
	// read it back for free.
	Trusted bool `json:"trusted,omitempty"`

	// Branches globs the destinations a pushing tool may write to.
	// Empty means the destination is unconstrained, which is refused.
	Branches []string `json:"branches,omitempty"`
}

// readingTools return content the session did not necessarily author. Whether
// that costs the untrusted leg depends on the provenance of the root, which is
// why these are priced per grant rather than per tool.
//
// git is present here as separate verbs rather than one tool with a
// subcommand scope, because factoring the verbs statically achieves the same
// thing: git difftool has no tool, so it cannot be named in a grant, and
// legsFor refuses anything it has no rule for.
var readingTools = []string{
	"read_file", "list_directory", "grep", "find_files", "go_vet",
	"git_status", "git_diff", "git_log",
}

// writingTools change the tree beneath their root without taking anything in.
var writingTools = []string{
	"write_file", "create_directory", "go_fmt",
	"git_add", "git_commit",
}

// legsFor computes what a grant costs. It returns an error when the scope
// does not constrain the tool enough for the answer to be the same for every
// call the grant permits, which is the property that lets Mint decide.
func legsFor(g Grant) (LegSet, error) {
	switch {
	case slices.Contains(readingTools, g.Tool):
		if g.Scope.Root == "" {
			return 0, fmt.Errorf("grant for %q needs a scope root", g.Tool)
		}
		if g.Scope.Trusted {
			return Legs(Private), nil
		}
		return Legs(Untrusted, Private), nil

	case slices.Contains(writingTools, g.Tool):
		if g.Scope.Root == "" {
			return 0, fmt.Errorf("grant for %q needs a scope root", g.Tool)
		}
		if g.Scope.Trusted {
			return 0, fmt.Errorf("grant for %q cannot write a trusted root: content there must stay operator-authored", g.Tool)
		}
		return Legs(Private, Mutate), nil

	case g.Tool == "git_push":
		if len(g.Scope.Branches) == 0 {
			return 0, fmt.Errorf("grant for %q needs branch patterns: an unconstrained destination has no fixed cost", g.Tool)
		}
		return Legs(Private, Mutate), nil

	case g.Tool == "dispatch_to_agent":
		return Legs(Private, Mutate), nil

	case g.Tool == "echo":
		return Legs(), nil

	default:
		return 0, fmt.Errorf("no leg rule for tool %q: add one to legsFor before it can be granted", g.Tool)
	}
}

// GrantSet is the full set of permissions in a token.
type GrantSet []Grant

// TotalLegs is the union of what every grant costs. This is what the
// per-session cap applies to: two narrow grants can together exceed what
// either does alone.
func (gs GrantSet) TotalLegs() (LegSet, error) {
	var total LegSet
	for _, g := range gs {
		legs, err := legsFor(g)
		if err != nil {
			return 0, err
		}
		total |= legs
	}
	return total, nil
}

// Validate reports whether this set may be signed. It enforces the session
// cap on the union, and refuses any set where a trusted root overlaps a
// writable one, which would otherwise let a session launder content into the
// trusted zone and read it back without paying the untrusted leg.
func (gs GrantSet) Validate() error {
	total, err := gs.TotalLegs()
	if err != nil {
		return err
	}
	if err := total.Valid(); err != nil {
		return fmt.Errorf("grants together grant %w", err)
	}

	var trusted, writable []string
	for _, g := range gs {
		legs, err := legsFor(g)
		if err != nil {
			return err
		}
		switch {
		case g.Scope.Trusted && g.Scope.Root != "":
			trusted = append(trusted, g.Scope.Root)
		case legs.Has(Mutate) && g.Scope.Root != "":
			writable = append(writable, g.Scope.Root)
		}
	}
	for _, tr := range trusted {
		for _, w := range writable {
			if pathsOverlap(tr, w) {
				return fmt.Errorf("trusted root %q is writable via %q: content there would no longer be operator-authored", tr, w)
			}
		}
	}
	return nil
}

// Find returns the grant for tool whose root contains the requested path, or
// the first grant for tool when it takes no path. Callers use this rather
// than the tool name alone, because the same tool may be granted twice with
// different scopes and different costs.
func (gs GrantSet) Find(tool, requested string) (Grant, bool) {
	var fallback Grant
	var haveFallback bool
	for _, g := range gs {
		if g.Tool != tool {
			continue
		}
		if requested == "" || g.Scope.Root == "" {
			if !haveFallback {
				fallback, haveFallback = g, true
			}
			continue
		}
		if _, err := chrootPath(g.Scope.Root, requested); err == nil {
			return g, true
		}
	}
	return fallback, haveFallback
}

// Tools returns the distinct tool names this set grants, in order.
func (gs GrantSet) Tools() []string {
	var names []string
	for _, g := range gs {
		if !slices.Contains(names, g.Tool) {
			names = append(names, g.Tool)
		}
	}
	return names
}

// pathsOverlap reports whether either path contains the other.
func pathsOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+string(filepath.Separator)) ||
		strings.HasPrefix(b, a+string(filepath.Separator))
}
