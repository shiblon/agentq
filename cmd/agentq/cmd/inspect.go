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
	inspectCmd.Flags().Bool("chain", false, "Follow parent_session_id links and print the full chain")
}

func runInspect(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	eqAddr := viper.GetString("eq_addr")
	chain, _ := cmd.Flags().GetBool("chain")

	ctx := context.Background()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	st := store.New(eq)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if !chain {
		session, err := st.GetSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("get session: %w", err)
		}
		return enc.Encode(session)
	}

	// Walk the chain from oldest ancestor to current, collecting sessions.
	var sessions []any
	id := sessionID
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		s, err := st.GetSession(ctx, id)
		if err != nil {
			return fmt.Errorf("get session %s: %w", id, err)
		}
		sessions = append([]any{s}, sessions...) // prepend so oldest is first
		id = s.ParentSessionID
	}

	fmt.Fprintf(os.Stdout, "// chain of %d session(s), oldest first\n", len(sessions))
	return enc.Encode(sessions)
}
