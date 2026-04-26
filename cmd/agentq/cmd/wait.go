package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/shiblon/agentq/pkg/store"
	"github.com/spf13/cobra"
)

var waitCmd = &cobra.Command{
	Use:   "wait <session-id>",
	Short: "Block until a session reaches a terminal state",
	Long: `Polls a session until its status is completed, failed, or
awaiting_review, then exits. Exit code is 0 for completed, 1 for failed
or awaiting_review.

Useful for scripting:
  SESSION=$(agentq submit --prompt="..." 2>&1 | awk '/submitted/{print $3}')
  agentq wait $SESSION && agentq result $SESSION`,
	Args: cobra.ExactArgs(1),
	RunE: runWait,
}

func init() {
	rootCmd.AddCommand(waitCmd)
	waitCmd.Flags().Duration("timeout", 10*time.Minute, "Maximum time to wait before giving up")
	waitCmd.Flags().Duration("interval", 2*time.Second, "Poll interval")
	waitCmd.Flags().Bool("quiet", false, "Suppress status updates; only print final status")
}

func runWait(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	timeout, _ := cmd.Flags().GetDuration("timeout")
	interval, _ := cmd.Flags().GetDuration("interval")
	quiet, _ := cmd.Flags().GetBool("quiet")

	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	st := store.New(eq)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	lastStatus := ""
	for {
		session, err := st.GetSession(ctx, sessionID)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("timed out waiting for session %s", sessionID)
			}
			return fmt.Errorf("get session: %w", err)
		}

		if !quiet && session.Status != lastStatus {
			fmt.Printf("status: %s\n", session.Status)
			lastStatus = session.Status
		}

		switch session.Status {
		case "completed":
			return nil
		case "failed":
			return fmt.Errorf("session %s failed", sessionID)
		case "awaiting_review":
			return fmt.Errorf("session %s is awaiting human review (run: agentq review)", sessionID)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for session %s (last status: %s)", sessionID, session.Status)
		case <-ticker.C:
		}
	}
}
