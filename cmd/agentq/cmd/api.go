package cmd

import (
	"fmt"
	"log"
	"net/http"

	agentqapi "github.com/shiblon/agentq/pkg/api"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
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
  GET    /api/v1/sessions[?status=&limit=]
  POST   /api/v1/sessions
  GET    /api/v1/sessions/{id}
  GET    /api/v1/sessions/{id}/chain
  GET    /api/v1/sessions/{id}/result
  GET    /api/v1/agents
  POST   /api/v1/agents
  DELETE /api/v1/agents/{name}
  GET    /api/v1/queues
  GET    /api/v1/review`,
	RunE: runAPI,
}

func init() {
	rootCmd.AddCommand(apiCmd)
	apiCmd.Flags().String("addr", ":8080", "Address to listen on (env: AGENTQ_API_ADDR)")
	viper.BindPFlag("api_addr", apiCmd.Flags().Lookup("addr"))
}

func runAPI(cmd *cobra.Command, args []string) error {
	addr := viper.GetString("api_addr")
	eqAddr := viper.GetString("eq_addr")
	configFile := viper.GetString("config")

	ctx := cmd.Context()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	srv := agentqapi.New(eq, configFile)
	log.Printf("api server listening on %s", addr)
	return http.ListenAndServe(addr, srv.Handler())
}
