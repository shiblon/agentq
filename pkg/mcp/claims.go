package mcp

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	claimSession = "mcp_session"
	claimGrants  = "mcp_grants"
	claimReplyTo = "mcp_reply_to"
	claimDepth   = "mcp_depth"

	defaultTokenTTL = time.Hour
)

// Claims is the signed configuration envelope carried in each MCP session JWT.
// It tells the MCP server which filesystem path to expose and which of the
// three rule-of-two legs the session may exercise. Any component holding the
// signing key may mint tokens; the package makes no assumptions about the
// caller.
type Claims struct {
	// Issuer identifies the component that minted this token (e.g. "agentq").
	// Must match the issuer expected by the MCP server on Parse.
	Issuer string

	// SessionID correlates this MCP session with the AgentQ task that spawned it.
	SessionID string

	// Grants is what this session may do: each entry pairs a tool with the
	// slice of its argument space the session may use it on. Roots live in
	// grant scopes rather than session-wide, so the same verb can read a
	// trusted mount and an untrusted tree at different cost.
	//
	// Empty permits nothing. Mint refuses a set whose legs together exceed
	// MaxLegs, or where a trusted root is also writable.
	Grants GrantSet

	// ReplyTo is the EntroQ queue where dispatch_to_agent should route agent
	// results. Typically the supervisor's own inbox. Required in any JWT that
	// grants dispatch_to_agent; absent or empty means dispatch_to_agent will
	// reject the call. Optional in all other contexts.
	ReplyTo string

	// Depth is the current dispatch nesting level. The supervisor mints tokens
	// at depth 0; each dispatch_to_agent call increments it in the child task's
	// JWT. Used by the MCP server to enforce MaxDispatchDepth.
	Depth int

	// Expiry is when this token becomes invalid.
	// If zero when passed to Mint, a default TTL of one hour is used.
	Expiry time.Time
}

// Mint encodes c as a signed JWT using privKey.
// If c.Expiry is zero, the token expires in one hour from now.
//
// Mint refuses to sign a token carrying more than MaxLegs. This is the only
// place authority is granted, so the cap is enforced here rather than trusted
// to whoever assembled the Claims.
func Mint(privKey jwk.Key, c Claims) (string, error) {
	if err := c.Grants.Validate(); err != nil {
		return "", fmt.Errorf("mcp: refusing to mint: %w", err)
	}
	expiry := c.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(defaultTokenTTL)
	}

	b := jwt.NewBuilder().
		Issuer(c.Issuer).
		Expiration(expiry).
		Claim(claimSession, c.SessionID).
		Claim(claimGrants, c.Grants).
		Claim(claimDepth, c.Depth)
	if c.ReplyTo != "" {
		b = b.Claim(claimReplyTo, c.ReplyTo)
	}
	tok, err := b.Build()
	if err != nil {
		return "", fmt.Errorf("mcp: build token: %w", err)
	}

	// Read the signing algorithm from the key's metadata so Mint works
	// with any key type (ES256, RS256, etc.) without hardcoding the algorithm.
	sigAlg, ok := privKey.Algorithm().(jwa.SignatureAlgorithm)
	if !ok {
		return "", fmt.Errorf("mcp: key has no signing algorithm set")
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(sigAlg, privKey))
	if err != nil {
		return "", fmt.Errorf("mcp: sign token: %w", err)
	}

	return string(signed), nil
}

// ParseInsecure decodes the Claims from raw without verifying the JWT signature
// or checking expiry. For development and testing only -- never use in production.
// The JWT still must be structurally valid and contain the expected claims.
// Uses jwt.ParseInsecure from lestrrat-go/jwx, which skips both verification
// and validation, then extracts claims via the same path as Parse.
func ParseInsecure(raw string) (*Claims, error) {
	tok, err := jwt.ParseInsecure([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("mcp: parse insecure token: %w", err)
	}
	return extractClaims(tok)
}

// Parse validates raw against keySet, checks that iss matches issuer and the
// token has not expired, and returns the embedded Claims.
// Returns an error if any expected claim is absent or the wrong type.
func Parse(keySet jwk.Set, issuer, raw string) (*Claims, error) {
	tok, err := jwt.Parse([]byte(raw),
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
		jwt.WithIssuer(issuer),
	)
	if err != nil {
		return nil, fmt.Errorf("mcp: parse token: %w", err)
	}
	return extractClaims(tok)
}

// extractClaims pulls the agentq-specific private claims from a parsed JWT token.
// Called by both Parse and ParseInsecure after their respective validation steps.
func extractClaims(tok jwt.Token) (*Claims, error) {
	private := tok.PrivateClaims()

	sessionID, err := requireString(private, claimSession)
	if err != nil {
		return nil, fmt.Errorf("mcp: extract claims: %w", err)
	}

	grants, err := grantsFromClaim(private[claimGrants])
	if err != nil {
		return nil, fmt.Errorf("mcp: extract claims: %w", err)
	}
	if err := grants.Validate(); err != nil {
		return nil, fmt.Errorf("mcp: token is not valid: %w", err)
	}

	// ReplyTo is optional; absent means empty string.
	replyTo, _ := private[claimReplyTo].(string)

	// Depth is optional; absent or non-numeric means 0.
	var depth int
	if v, ok := private[claimDepth]; ok {
		if f, ok := v.(float64); ok { // JSON numbers unmarshal as float64
			depth = int(f)
		}
	}

	return &Claims{
		Issuer:    tok.Issuer(),
		SessionID: sessionID,
		Grants:    grants,
		ReplyTo:   replyTo,
		Depth:     depth,
		Expiry:    tok.Expiration(),
	}, nil
}

// requireString extracts a string claim from private, returning an error if
// the key is absent or the value is not a string.
func requireString(private map[string]any, key string) (string, error) {
	v, ok := private[key]
	if !ok {
		return "", fmt.Errorf("missing claim %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("claim %q: expected string, got %T", key, v)
	}
	return s, nil
}

// requireStringSlice extracts a []string claim from private, returning an
// error if the key is absent or any element is not a string.
func requireStringSlice(private map[string]any, key string) ([]string, error) {
	v, ok := private[key]
	if !ok {
		return nil, fmt.Errorf("missing claim %q", key)
	}
	// JWT JSON unmarshal produces []interface{}, not []string.
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("claim %q: expected array, got %T", key, v)
	}
	result := make([]string, len(arr))
	for i, elem := range arr {
		s, ok := elem.(string)
		if !ok {
			return nil, fmt.Errorf("claim %q: element %d: expected string, got %T", key, i, elem)
		}
		result[i] = s
	}
	return result, nil
}

// grantsFromClaim decodes the grants claim, which travels as JSON so the
// scope vocabulary can grow without a new claim key per field.
func grantsFromClaim(v any) (GrantSet, error) {
	if v == nil {
		return nil, fmt.Errorf("missing claim %q", claimGrants)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("claim %q: %w", claimGrants, err)
	}
	var gs GrantSet
	if err := json.Unmarshal(raw, &gs); err != nil {
		return nil, fmt.Errorf("claim %q: %w", claimGrants, err)
	}
	return gs, nil
}
