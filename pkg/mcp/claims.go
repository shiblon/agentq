package mcp

import (
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const (
	claimSession = "mcp_session"
	claimWorkdir = "mcp_workdir"
	claimTools   = "mcp_tools"

	defaultTokenTTL = time.Hour
)

// Claims is the signed configuration envelope carried in each MCP session JWT.
// It tells the MCP server which filesystem path to expose and which tools to
// permit. Any component holding the signing key may mint tokens; the package
// makes no assumptions about the caller.
type Claims struct {
	// Issuer identifies the component that minted this token (e.g. "agentq").
	// Must match the issuer expected by the MCP server on Parse.
	Issuer string

	// SessionID correlates this MCP session with the AgentQ task that spawned it.
	SessionID string

	// Workdir is the real filesystem path the MCP server exposes as /.
	// All file tool paths are resolved relative to this directory.
	// An empty string means no filesystem access is permitted for this session.
	Workdir string

	// ToolAllowlist names the tools this session may invoke.
	// An empty list permits no tools. Tools absent from this list are
	// hidden from tools/list and rejected at call time.
	ToolAllowlist []string

	// Expiry is when this token becomes invalid.
	// If zero when passed to Mint, a default TTL of one hour is used.
	Expiry time.Time
}

// Mint encodes c as a signed JWT using privKey (RS256).
// If c.Expiry is zero, the token expires in one hour from now.
func Mint(privKey jwk.Key, c Claims) (string, error) {
	expiry := c.Expiry
	if expiry.IsZero() {
		expiry = time.Now().Add(defaultTokenTTL)
	}

	tok, err := jwt.NewBuilder().
		Issuer(c.Issuer).
		Expiration(expiry).
		Claim(claimSession, c.SessionID).
		Claim(claimWorkdir, c.Workdir).
		Claim(claimTools, c.ToolAllowlist).
		Build()
	if err != nil {
		return "", fmt.Errorf("mcp: build token: %w", err)
	}

	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, privKey))
	if err != nil {
		return "", fmt.Errorf("mcp: sign token: %w", err)
	}

	return string(signed), nil
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

	private := tok.PrivateClaims()

	sessionID, err := requireString(private, claimSession)
	if err != nil {
		return nil, fmt.Errorf("mcp: parse token: %w", err)
	}

	workdir, err := requireString(private, claimWorkdir)
	if err != nil {
		return nil, fmt.Errorf("mcp: parse token: %w", err)
	}

	tools, err := requireStringSlice(private, claimTools)
	if err != nil {
		return nil, fmt.Errorf("mcp: parse token: %w", err)
	}

	return &Claims{
		Issuer:        tok.Issuer(),
		SessionID:     sessionID,
		Workdir:       workdir,
		ToolAllowlist: tools,
		Expiry:        tok.Expiration(),
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
