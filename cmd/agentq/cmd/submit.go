package cmd

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/agentq/pkg/workflow"
	"github.com/shiblon/entroq"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var submitCmd = &cobra.Command{
	Use:   "submit",
	Short: "Submit a prompt and wait for the supervisor's response",
	Long: `Submit a prompt to the supervisor and block until a response arrives.

Creates a new session, enqueues the prompt as the first supervisor turn, then
claims the result from the session's reply queue and prints the output.`,
	RunE: runSubmit,
}

func init() {
	rootCmd.AddCommand(submitCmd)
	submitCmd.Flags().String("prompt", "", "The user prompt to submit (required)")
	submitCmd.Flags().String("user", "cli", "User ID to associate with the session")
	submitCmd.Flags().String("continue-from", "", "Session ID to inherit artifacts from")
	submitCmd.Flags().Bool("compact", false, "Ask the supervisor to compact inherited artifacts on first turn")
	submitCmd.Flags().String("repo", "", "Workspace repo path for agents to work in")
	submitCmd.Flags().String("supervisor-queue", "agentq/supervisor/inbox", "Supervisor inbox queue")
	_ = submitCmd.MarkFlagRequired("prompt")
	viper.BindPFlag("submit_prompt", submitCmd.Flags().Lookup("prompt"))
	viper.BindPFlag("submit_user", submitCmd.Flags().Lookup("user"))
}

func runSubmit(cmd *cobra.Command, args []string) error {
	prompt, _ := cmd.Flags().GetString("prompt")
	userID, _ := cmd.Flags().GetString("user")
	continueFrom, _ := cmd.Flags().GetString("continue-from")
	compact, _ := cmd.Flags().GetBool("compact")
	repo, _ := cmd.Flags().GetString("repo")
	supervisorQueue, _ := cmd.Flags().GetString("supervisor-queue")

	ctx := cmd.Context()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	st := store.New(eq)
	result, err := workflow.SubmitSession(ctx, st, eq, workflow.SubmitRequest{
		UserID:          userID,
		Prompt:          prompt,
		ContinueFrom:    continueFrom,
		Repo:            repo,
		Compact:         compact,
		SupervisorQueue: supervisorQueue,
	})
	if err != nil {
		return err
	}

	replyQueue := models.UserReplyQueue(result.SessionID)
	log.Printf("session %s submitted; waiting on %s", result.SessionID, replyQueue)

	// Block until the supervisor posts a result.
	task, err := eq.Claim(ctx, entroq.From(replyQueue))
	if err != nil {
		return fmt.Errorf("wait for result: %w", err)
	}

	// Extract output from the result task.
	var appTask models.Task
	if err := json.Unmarshal(task.Value, &appTask); err != nil {
		return fmt.Errorf("decode result task: %w", err)
	}
	output, _ := appTask.Payload["output"].(string)

	// Clean up the reply task.
	if _, err := eq.Modify(ctx, task.Delete()); err != nil {
		log.Printf("warning: could not delete reply task: %v", err)
	}

	fmt.Println(output)
	return nil
}
