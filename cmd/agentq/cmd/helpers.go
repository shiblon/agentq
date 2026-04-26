package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/viper"
)

// truncate returns s truncated to at most n runes with "..." appended if cut.
// Newlines are replaced with spaces so the result is safe for single-line display.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// eqToken resolves the bearer token for entroq connections.
// --eq-token-file takes precedence over --eq-token.
// Returns empty string if neither is set (unauthenticated mode).
func eqToken() (string, error) {
	if f := viper.GetString("eq_token_file"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("read eq token file %q: %w", f, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return viper.GetString("eq_token"), nil
}

// openEQ creates an entroq client using the global --eq-addr and --eq-token
// flags. All commands that talk to the queue server should use this instead of
// constructing their own opener.
func openEQ(ctx context.Context) (*entroq.EntroQ, error) {
	addr := viper.GetString("eq_addr")
	opts := []eqgrpc.Option{eqgrpc.WithInsecure()}
	tok, err := eqToken()
	if err != nil {
		return nil, fmt.Errorf("resolve eq token: %w", err)
	}
	if tok != "" {
		opts = append(opts, eqgrpc.WithBearerToken(tok))
	} else {
		log.Printf("entroq: no token configured (set --eq-token or AGENTQ_EQ_TOKEN for queue authorization)")
	}
	eq, err := entroq.New(ctx, eqgrpc.Opener(addr, opts...))
	if err != nil {
		return nil, fmt.Errorf("connect to entroq at %s: %w", addr, err)
	}
	return eq, nil
}
