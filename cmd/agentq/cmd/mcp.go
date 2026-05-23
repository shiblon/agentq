package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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

var mcpKeygenCmd = &cobra.Command{
	Use:   "keygen",
	Short: "Generate an MCP signing key pair",
	Long: `Generate a signing key pair for MCP session JWTs.

Writes two files:
  private.jwk   private key for the AgentQ worker (--key-file flag), mode 0600
  public.jwks   public key set for the MCP server (--jwks-file flag)

The --algorithm flag controls the key type:
  ES256  ECDSA P-256 (default, recommended)
  RS256  RSA 2048 (for environments that require RSA)`,
	RunE: runMCPKeygen,
}

func init() {
	rootCmd.AddCommand(mcpCmd)
	mcpCmd.AddCommand(mcpServeCmd)
	mcpCmd.AddCommand(mcpKeygenCmd)

	mcpServeCmd.Flags().String("addr", ":8081", "TCP listen address")
	mcpServeCmd.Flags().String("jwks-file", "", "Path to JWKS JSON file containing token verification keys")
	mcpServeCmd.Flags().String("issuer", "agentq", "Expected iss claim in session tokens")
	mcpServeCmd.Flags().Bool("insecure-skip-verification", false, "Skip JWT signature verification. Claims are still parsed and dynamic per-session. Never use in production.")
	mcpServeCmd.Flags().Bool("dev-tools", false, "Enable development-only tools (e.g. echo). Never use in production.")
	mcpServeCmd.Flags().String("queue-namespace", "agentq", "Prefix for agent queue names (dispatch_to_agent constructs <namespace>/<agent>/inbox). Only used when --eq-addr is set.")
	mcpServeCmd.Flags().Int("max-dispatch-depth", 1, "Maximum dispatch nesting level (0 = unlimited). Default 1 prevents specialist agents from dispatching further.")

	mcpKeygenCmd.Flags().String("out-dir", ".", "Directory to write private.jwk and public.jwks")
	mcpKeygenCmd.Flags().String("algorithm", "ES256", "Signing algorithm: ES256 (ECDSA P-256, default) or RS256 (RSA 2048)")
}

func runMCPKeygen(cmd *cobra.Command, _ []string) error {
	outDir, _ := cmd.Flags().GetString("out-dir")
	algStr, _ := cmd.Flags().GetString("algorithm")

	if err := os.MkdirAll(outDir, 0700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	alg := mcp.Algorithm(algStr)
	kp, err := mcp.GenerateKeyPair(alg)
	if err != nil {
		return fmt.Errorf("generate key pair: %w", err)
	}

	privPath := filepath.Join(outDir, "private.jwk")
	pubPath := filepath.Join(outDir, "public.jwks")

	if err := mcp.WritePrivateKey(privPath, kp); err != nil {
		return err
	}
	if err := mcp.WritePublicKeySet(pubPath, kp); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "generated %s key pair:\n", algStr)
	fmt.Fprintf(os.Stderr, "  private key: %s  (keep secret; use with --key-file)\n", privPath)
	fmt.Fprintf(os.Stderr, "  public keys: %s  (share with MCP server; use with --jwks-file)\n", pubPath)
	return nil
}

func runMCPServe(cmd *cobra.Command, _ []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

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

	// Wire dispatch_to_agent via the eq connection (flag or AGENTQ_EQ_ADDR env var).
	eq, err := openEQ(ctx)
	if err != nil {
		return fmt.Errorf("connect to entroq: %w", err)
	}
	defer eq.Close()
	cfg.EQ = eq
	cfg.QueueNamespace, _ = cmd.Flags().GetString("queue-namespace")
	cfg.MaxDispatchDepth, _ = cmd.Flags().GetInt("max-dispatch-depth")
	log.Printf("mcp serve: orchestration tools enabled (namespace=%s, max-dispatch-depth=%d)", cfg.QueueNamespace, cfg.MaxDispatchDepth)

	srv, err := mcp.New(cfg)
	if err != nil {
		return fmt.Errorf("create mcp server: %w", err)
	}

	// SIGHUP reloads the public key set from --jwks-file (no-op in insecure mode).
	jwksFile, _ := cmd.Flags().GetString("jwks-file")
	if !skipVerification && jwksFile != "" {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGHUP)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-sigs:
					set, err := loadJWKSFile(jwksFile)
					if err != nil {
						log.Printf("mcp serve: key reload failed: %v", err)
					} else {
						srv.ReloadPublicKeys(set)
						log.Printf("mcp serve: public keys reloaded from %s", jwksFile)
					}
				}
			}
		}()
	}

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
