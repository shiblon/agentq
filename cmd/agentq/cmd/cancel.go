package cmd

import (
	"fmt"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var cancelCmd = &cobra.Command{
	Use:   "cancel",
	Short: "Cancel a running session",
	Long: `Marks the session as cancelled. The supervisor will drop any queued
tasks for the session on its next claim; in-flight agent work may still
complete but no further dispatch will occur.`,
	RunE: runCancel,
}

func init() {
	rootCmd.AddCommand(cancelCmd)
	cancelCmd.Flags().String("session", "", "Session ID to cancel (required)")
	cancelCmd.MarkFlagRequired("session")
}

func runCancel(cmd *cobra.Command, args []string) error {
	sessionID, _ := cmd.Flags().GetString("session")
	eqAddr := viper.GetString("eq_addr")

	ctx := cmd.Context()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	st := store.New(eq)
	if err := st.UpdateSession(ctx, sessionID, func(s *models.Session) error {
		switch s.Status {
		case "completed", "cancelled":
			return fmt.Errorf("session already %s", s.Status)
		}
		s.Status = "cancelled"
		return nil
	}); err != nil {
		return fmt.Errorf("cancel session %s: %w", sessionID, err)
	}

	fmt.Printf("Session %s cancelled.\n", sessionID)
	return nil
}
