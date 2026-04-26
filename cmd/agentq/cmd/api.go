package cmd

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	agentqapi "github.com/shiblon/agentq/pkg/api"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var apiCmd = &cobra.Command{
	Use:   "api",
	Short: "Run the agentq HTTP API server",
	Long: `Starts an HTTP server exposing a REST API for managing sessions,
agents, and review tasks. The API is the backend for the web UI.

Endpoints:
  GET    /api/v1/health
  GET    /api/v1/config
  GET    /api/v1/sessions[?status=&limit=]
  POST   /api/v1/sessions
  POST   /api/v1/sessions/{id}/cancel
  GET    /api/v1/sessions/{id}
  GET    /api/v1/sessions/{id}/chain
  GET    /api/v1/sessions/{id}/result
  GET    /api/v1/agents
  POST   /api/v1/agents
  DELETE /api/v1/agents/{name}
  GET    /api/v1/queues
  GET    /api/v1/review
  POST   /api/v1/review/{task_id}/approve
  POST   /api/v1/review/{task_id}/reject`,
	RunE: runAPI,
}

func init() {
	rootCmd.AddCommand(apiCmd)
	apiCmd.Flags().String("addr", ":8080", "Address to listen on (env: AGENTQ_API_ADDR)")
	apiCmd.Flags().String("static-dir", "", "Serve web UI static files from this directory (e.g. web/dist)")
	apiCmd.Flags().String("jwks-url", "", "JWKS endpoint URL for JWT validation (e.g. http://localhost:8080/oauth/v2/keys)")
	apiCmd.Flags().String("issuer", "", "Expected JWT issuer (iss claim); must match jwks-url host")
	apiCmd.Flags().String("policy-dir", "", "Directory containing .rego policy files; defaults to embedded policy")
	apiCmd.Flags().String("oidc-client-id", "", "Browser PKCE client ID forwarded to the web UI (env: AGENTQ_OIDC_CLIENT_ID)")
	apiCmd.Flags().Bool("no-auth", false, "Disable authentication (local development only; binds to localhost)")
	apiCmd.Flags().String("provenance-key", "", "Base64 root key for session provenance tokens (env: AGENTQ_PROVENANCE_KEY)")
	apiCmd.Flags().String("provenance-key-file", "", "File containing base64 root key for provenance tokens (Vault Agent / secret rotation)")
	viper.BindPFlag("api_addr", apiCmd.Flags().Lookup("addr"))
	viper.BindPFlag("api_static_dir", apiCmd.Flags().Lookup("static-dir"))
	viper.BindPFlag("api_jwks_url", apiCmd.Flags().Lookup("jwks-url"))
	viper.BindPFlag("api_issuer", apiCmd.Flags().Lookup("issuer"))
	viper.BindPFlag("api_policy_dir", apiCmd.Flags().Lookup("policy-dir"))
	viper.BindPFlag("api_oidc_client_id", apiCmd.Flags().Lookup("oidc-client-id"))
	viper.BindPFlag("api_no_auth", apiCmd.Flags().Lookup("no-auth"))
	viper.BindPFlag("api_provenance_key", apiCmd.Flags().Lookup("provenance-key"))
	viper.BindPFlag("api_provenance_key_file", apiCmd.Flags().Lookup("provenance-key-file"))
}

func runAPI(cmd *cobra.Command, args []string) error {
	addr := viper.GetString("api_addr")
	configFile := viper.GetString("config")
	staticDir := viper.GetString("api_static_dir")
	jwksURL := viper.GetString("api_jwks_url")
	issuer := viper.GetString("api_issuer")
	policyDir := viper.GetString("api_policy_dir")
	oidcClientID := viper.GetString("api_oidc_client_id")
	noAuth := viper.GetBool("api_no_auth")

	if jwksURL == "" && !noAuth {
		return fmt.Errorf("authentication is required: set --jwks-url or pass --no-auth to explicitly disable (local development only)")
	}

	ctx := cmd.Context()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	var opts []agentqapi.Option

	if staticDir != "" {
		opts = append(opts, agentqapi.WithStaticDir(staticDir))
		log.Printf("serving web UI from %s", staticDir)
	}

	if issuer != "" {
		opts = append(opts, agentqapi.WithIssuer(issuer))
	}
	if oidcClientID != "" {
		opts = append(opts, agentqapi.WithOIDCClientID(oidcClientID))
	}

	if jwksURL != "" {
		if issuer == "" {
			return fmt.Errorf("--issuer is required when --jwks-url is set")
		}
		v := agentqapi.NewJWKSValidator(jwksURL, issuer)
		opts = append(opts, agentqapi.WithJWKSValidator(v))
		log.Printf("JWT validation enabled: jwks=%s issuer=%s", jwksURL, issuer)

		var authorizer *agentqapi.OPAAuthorizer
		if policyDir != "" {
			authorizer, err = agentqapi.NewOPAAuthorizerFromPaths(ctx, policyDir)
			if err != nil {
				return fmt.Errorf("load opa policy from %s: %w", policyDir, err)
			}
			log.Printf("OPA policy loaded from %s", policyDir)
		} else {
			authorizer, err = agentqapi.NewOPAAuthorizerDefault(ctx)
			if err != nil {
				return fmt.Errorf("load embedded opa policy: %w", err)
			}
			log.Printf("OPA policy: using embedded default")
		}
		opts = append(opts, agentqapi.WithAuthorizer(authorizer))
	} else {
		// --no-auth was explicitly set; restrict to loopback so the open
		// server is never accidentally reachable from the network.
		if addr == ":8080" {
			addr = "127.0.0.1:8080"
		}
		log.Printf("WARNING: authentication disabled (--no-auth); listening on %s only", addr)
	}

	provKeyFile := viper.GetString("api_provenance_key_file")
	provKey := viper.GetString("api_provenance_key")
	if provKeyFile != "" {
		b, err := os.ReadFile(provKeyFile)
		if err != nil {
			return fmt.Errorf("read provenance key file: %w", err)
		}
		provKey = strings.TrimSpace(string(b))
	}
	if provKey != "" {
		pi, err := agentqapi.NewProvenanceIssuerOption(ctx, provKey)
		if err != nil {
			return fmt.Errorf("create provenance issuer: %w", err)
		}
		opts = append(opts, pi)
		log.Printf("session provenance tokens enabled")
	} else {
		log.Printf("session provenance tokens disabled (set --provenance-key to enable)")
	}

	srv := agentqapi.New(eq, configFile, opts...)
	log.Printf("api server listening on %s", addr)
	return http.ListenAndServe(addr, srv.Handler())
}
