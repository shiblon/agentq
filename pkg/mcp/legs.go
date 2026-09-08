package mcp

import (
	"fmt"
	"slices"
	"strings"
)

// Leg is one of the three properties from the Agents Rule of Two. A session
// holding all three has no bound on what a prompt injection can accomplish:
// it can be told what to do, has something worth taking, and has a way to act
// on both. Sessions are therefore capped at two.
type Leg uint8

const (
	// Untrusted means the tool returns content nobody on our side authored:
	// file bodies, diffs, commit messages, search hits, web pages.
	Untrusted Leg = 1 << iota

	// Private means the tool reads state the session is trusted to see but
	// an attacker is not, which is anything under the session workdir.
	Private

	// Mutate means the tool changes state or communicates outside the
	// session. Writing a file and pushing a branch are both this leg; how
	// far each reaches is bounded by scope, not by legs.
	Mutate
)

// MaxLegs is the most a single tool or session may carry.
const MaxLegs = 2

var legNames = map[Leg]string{
	Untrusted: "untrusted",
	Private:   "private",
	Mutate:    "mutate",
}

// legOrder fixes the order legs are rendered in, so a LegSet has one spelling.
var legOrder = []Leg{Untrusted, Private, Mutate}

// LegSet is a set of Legs.
type LegSet uint8

// Legs builds a LegSet from individual legs.
func Legs(legs ...Leg) LegSet {
	var s LegSet
	for _, l := range legs {
		s |= LegSet(l)
	}
	return s
}

// Has reports whether s includes l.
func (s LegSet) Has(l Leg) bool { return s&LegSet(l) != 0 }

// Count returns how many legs are in s.
func (s LegSet) Count() int {
	n := 0
	for _, l := range legOrder {
		if s.Has(l) {
			n++
		}
	}
	return n
}

// Contains reports whether every leg in other is also in s. This is the
// containment test used at mint time: a child may never hold a leg its parent
// lacks, and a tool may only run in a session that covers everything it needs.
func (s LegSet) Contains(other LegSet) bool { return s&other == other }

// Names returns the canonical names of the legs in s, in fixed order.
func (s LegSet) Names() []string {
	names := make([]string, 0, MaxLegs+1)
	for _, l := range legOrder {
		if s.Has(l) {
			names = append(names, legNames[l])
		}
	}
	return names
}

// String renders s as a comma-separated list, or "none" when empty.
func (s LegSet) String() string {
	if s == 0 {
		return "none"
	}
	return strings.Join(s.Names(), ",")
}

// Valid reports an error if s carries more legs than a session may hold.
func (s LegSet) Valid() error {
	if n := s.Count(); n > MaxLegs {
		return fmt.Errorf("%d legs (%s) exceeds the maximum of %d", n, s, MaxLegs)
	}
	return nil
}

// ParseLegs builds a LegSet from canonical names. Unknown or repeated names
// are an error, so a typo in configuration fails loudly rather than silently
// granting less than intended.
func ParseLegs(names []string) (LegSet, error) {
	var s LegSet
	for _, raw := range names {
		name := strings.ToLower(strings.TrimSpace(raw))
		idx := slices.IndexFunc(legOrder, func(l Leg) bool { return legNames[l] == name })
		if idx < 0 {
			return 0, fmt.Errorf("unknown leg %q (want %s)", raw, strings.Join(allLegNames(), ", "))
		}
		l := legOrder[idx]
		if s.Has(l) {
			return 0, fmt.Errorf("leg %q listed twice", name)
		}
		s |= LegSet(l)
	}
	return s, nil
}

func allLegNames() []string {
	names := make([]string, 0, len(legOrder))
	for _, l := range legOrder {
		names = append(names, legNames[l])
	}
	return names
}
