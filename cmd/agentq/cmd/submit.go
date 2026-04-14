package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var submitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Create a session and enqueue a task for the supervisor",
	RunE:  runSubmit,
}

func init() {
	rootCmd.AddCommand(submitCmd)
	submitCmd.Flags().String("prompt", "", "The user prompt to submit (required)")
	submitCmd.Flags().String("user", "cli", "User ID to associate with the session")
	submitCmd.MarkFlagRequired("prompt")
	viper.BindPFlag("prompt", submitCmd.Flags().Lookup("prompt"))
	viper.BindPFlag("user", submitCmd.Flags().Lookup("user"))
}

func runSubmit(cmd *cobra.Command, args []string) error {
	prompt := viper.GetString("prompt")
	userID := viper.GetString("user")
	eqAddr := viper.GetString("eq_addr")

	ctx := context.Background()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	return submitSession(ctx, eq, userID, prompt)
}

// submitSession creates a session doc and enqueues a supervisor task for it.
// Exported so it can be called from tests or other entry points.
func submitSession(ctx context.Context, eq *entroq.EntroQ, userID, prompt string) error {
	st := store.New(eq)

	session := models.NewSession(userID, prompt)
	if err := st.PutSession(ctx, session); err != nil {
		return fmt.Errorf("create session: %w", err)
	}

	sessionURI := store.SessionURI(session.ID)
	task := models.NewTask("supervisor", sessionURI, nil)
	taskBytes, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}

	if _, err := eq.Modify(ctx, entroq.InsertingInto("supervisor", entroq.WithRawValue(taskBytes))); err != nil {
		return fmt.Errorf("enqueue supervisor task: %w", err)
	}

	log.Printf("submitted session %s -> %s", session.ID, sessionURI)
	return nil
}
