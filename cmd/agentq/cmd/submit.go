package cmd

import (
	"context"
	"fmt"

	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/agentq/pkg/workflow"
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
	submitCmd.Flags().String("continue-from", "", "Session ID to inherit artifacts from")
	submitCmd.Flags().Bool("compact", false, "Ask the supervisor to compact inherited artifacts on first turn")
	submitCmd.Flags().String("repo", "", "Workspace repo path for agents to work in (e.g. github.com/shiblon/agentq)")
	submitCmd.MarkFlagRequired("prompt")
	viper.BindPFlag("prompt", submitCmd.Flags().Lookup("prompt"))
	viper.BindPFlag("user", submitCmd.Flags().Lookup("user"))
	viper.BindPFlag("continue_from", submitCmd.Flags().Lookup("continue-from"))
	viper.BindPFlag("compact", submitCmd.Flags().Lookup("compact"))
	viper.BindPFlag("submit_repo", submitCmd.Flags().Lookup("repo"))
}

func runSubmit(cmd *cobra.Command, args []string) error {
	prompt := viper.GetString("prompt")
	userID := viper.GetString("user")
	eqAddr := viper.GetString("eq_addr")
	continueFrom, _ := cmd.Flags().GetString("continue-from")
	compact, _ := cmd.Flags().GetBool("compact")
	repo := viper.GetString("submit_repo")

	ctx := context.Background()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	st := store.New(eq)
	result, err := workflow.SubmitSession(ctx, st, eq, userID, prompt, continueFrom, repo, compact)
	if err != nil {
		return err
	}
	_ = result // session ID already logged by workflow
	return nil
}
