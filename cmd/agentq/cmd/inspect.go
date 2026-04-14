package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <session-id>",
	Short: "Print the current state of a session",
	Args:  cobra.ExactArgs(1),
	RunE:  runInspect,
}

func init() {
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	eqAddr := viper.GetString("eq_addr")

	ctx := context.Background()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	session, err := store.New(eq).GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(session)
}
