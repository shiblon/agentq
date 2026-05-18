package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/spf13/cobra"
)

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "MCP server commands",
}

var mcpServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the MCP pool server",
	Long: `Start the long-running MCP pool server.

Each SSE connection is configured by a signed JWT passed as ?token= in the URL.
The JWT carries the tool allowlist and filesystem working directory for that session.

The --jwks-file flag should point to a JWKS JSON file containing the public key(s)
used to verify session tokens. The matching private key is held by the component
that mints tokens (typically the AgentQ worker).`,
	RunE: runMCPServe,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.AddCommand(mcpServeCmd)

	mcpServeCmd.Flags().String("addr", ":8081", "TCP listen address")
	mcpServeCmd.Flags().String("jwks-file", "", "Path to JWKS JSON file containing token verification keys")
	mcpServeCmd.Flags().String("issuer", "agentq", "Expected iss claim in session tokens")
	mcpServeCmd.Flags().Bool("insecure-skip-verification", false, "Skip JWT signature verification. Claims are still parsed and dynamic per-session. Never use in production.")
	mcpServeCmd.Flags().Bool("dev-tools", false, "Enable development-only tools (e.g. echo). Never use in production.")
}

func runMCPServe(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	cfg := mcp.Config{Addr: addr}

	skipVerification, _ := cmd.Flags().GetBool("insecure-skip-verification")
	devTools, _ := cmd.Flags().GetBool("dev-tools")
	if devTools {
		cfg.DevTools = true
		fmt.Fprintln(os.Stderr, "WARNING: --dev-tools is set; development-only tools are enabled. Do not use in production.")
	}

	if skipVerification {
		cfg.InsecureSkipVerification = true
		fmt.Fprintln(os.Stderr, "WARNING: --insecure-skip-verification is set; JWT signatures are not checked. Do not use in production.")
	} else {
		jwksFile, _ := cmd.Flags().GetString("jwks-file")
		if jwksFile == "" {
			return fmt.Errorf("--jwks-file is required (or use --insecure-skip-verification for dev)")
		}
		issuer, _ := cmd.Flags().GetString("issuer")
		pubKeys, err := loadJWKSFile(jwksFile)
		if err != nil {
			return fmt.Errorf("load jwks: %w", err)
		}
		cfg.PublicKeys = pubKeys
		cfg.Issuer = issuer
	}

	srv, err := mcp.New(cfg)
	if err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	fmt.Fprintf(os.Stderr, "agentq mcp serve: listening on %s\n", addr)
	return srv.Start(ctx)
}

// loadJWKSFile reads a JWKS JSON file and returns the parsed key set.
func loadJWKSFile(path string) (jwk.Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	set, err := jwk.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse jwks %s: %w", path, err)
	}
	return set, nil
}
