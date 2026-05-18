package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/shiblon/agentq/pkg/runner"
	"github.com/spf13/cobra"
)

var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Runner commands",
}

var runnerServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the runner microservice",
	Long: `Start the runner HTTP microservice.

The runner receives tasks (JWT + transcript) via POST /, invokes the configured
agent command with an MCP connection, and returns the agent's output.

The command is fixed at startup -- the runner is not a general-purpose executor.
To run a different agent type, deploy a different runner with a different --command.`,
	RunE: runRunnerServe,
}

func init() {
	rootCmd.AddCommand(runnerCmd)
	runnerCmd.AddCommand(runnerServeCmd)

	runnerServeCmd.Flags().String("addr", ":8082", "TCP listen address")
	runnerServeCmd.Flags().String("command", "claude", "Agent binary to invoke")
	runnerServeCmd.Flags().StringSlice("args", []string{"--print"}, "Fixed arguments passed to command before --mcp-config")
	runnerServeCmd.Flags().String("mcp-addr", "http://localhost:8081", "Base URL of the MCP pool server")
}

func runRunnerServe(cmd *cobra.Command, _ []string) error {
	addr, _ := cmd.Flags().GetString("addr")
	command, _ := cmd.Flags().GetString("command")
	args, _ := cmd.Flags().GetStringSlice("args")
	mcpAddr, _ := cmd.Flags().GetString("mcp-addr")

	r := runner.New(runner.Config{
		Command: command,
		Args:    args,
		MCPAddr: mcpAddr,
	})

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	srv := &http.Server{Handler: r}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	fmt.Fprintf(os.Stderr, "agentq runner serve: listening on %s (command: %s)\n", ln.Addr(), command)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case <-ctx.Done():
		return srv.Shutdown(context.Background())
	case err := <-errc:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("serve: %w", err)
	}
}
