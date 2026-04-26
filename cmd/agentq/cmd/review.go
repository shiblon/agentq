package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/spf13/cobra"
)

var reviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Interactively process tasks in the human_review queue",
	Long: `Claims tasks from the human_review queue one at a time, displays
the session context, and prompts for an approval decision.

Response options at the prompt:
  y        Approve - agent re-runs with full permissions
  n        Reject  - session is closed with a rejection note
  <text>   Provide input - agent re-runs with the supplied text as context`,
	RunE: runReview,
}

func init() {
	rootCmd.AddCommand(reviewCmd)
	reviewCmd.Flags().Bool("once", false, "Process a single review task and exit")
	reviewCmd.Flags().Bool("verbose", false, "Show full artifact content instead of summaries")
}

func runReview(cmd *cobra.Command, args []string) error {
	once, _ := cmd.Flags().GetBool("once")
	verbose, _ := cmd.Flags().GetBool("verbose")

	ctx := cmd.Context()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	st := store.New(eq)
	scanner := bufio.NewScanner(os.Stdin)

	for {
		if ctx.Err() != nil {
			return nil
		}

		fmt.Println("Waiting for review task...")
		task, err := eq.Claim(ctx, entroq.From("human_review"))
		if err != nil {
			if entroq.IsCanceled(err) {
				return nil
			}
			return fmt.Errorf("claim from human_review: %w", err)
		}

		var req models.HumanReviewRequest
		if err := json.Unmarshal(task.Value, &req); err != nil {
			fmt.Fprintf(os.Stderr, "warning: malformed review task %s: %v\n", task.ID, err)
			eq.Modify(ctx, task.Delete()) //nolint:errcheck
			continue
		}

		printReviewRequest(req, st, ctx, verbose)

		outcome, humanInput := promptReviewer(scanner)

		reply := models.NewReviewReply(req.SessionURI, outcome, humanInput, task.ID)
		replyBytes, err := json.Marshal(reply)
		if err != nil {
			return fmt.Errorf("marshal reply: %w", err)
		}

		if _, err := eq.Modify(ctx,
			task.Delete(),
			entroq.InsertingInto(req.ReplyQueue, entroq.WithRawValue(replyBytes)),
		); err != nil {
			return fmt.Errorf("post review reply: %w", err)
		}

		fmt.Printf("Reply posted (%s) -> %s\n\n", outcome, req.ReplyQueue)

		if once {
			return nil
		}
	}
}

func printReviewRequest(req models.HumanReviewRequest, st *store.Store, ctx context.Context, verbose bool) {
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("Session:  %s\n", req.SessionURI)
	fmt.Printf("Agent:    %s\n", req.RequestingAgent)
	fmt.Printf("Reason:   %s\n", req.Reason)

	if req.ContextSummary != "" {
		fmt.Println("\nContext (last agent output):")
		if verbose {
			fmt.Println(req.ContextSummary)
		} else {
			// Print first 300 chars when not verbose.
			s := req.ContextSummary
			if len([]rune(s)) > 300 {
				s = string([]rune(s)[:300]) + "...\n(use --verbose for full output)"
			}
			fmt.Println(s)
		}
	}

	// When verbose, also load and show the full session artifact list.
	if verbose {
		sessionID := strings.TrimPrefix(req.SessionURI, "doc:sessions/")
		if session, err := st.GetSession(ctx, sessionID); err == nil {
			fmt.Println("\nFull session artifacts:")
			for _, a := range session.Artifacts {
				origin := ""
				if a.OriginSessionID != "" {
					origin = fmt.Sprintf(" (from %s)", a.OriginSessionID)
				}
				fmt.Printf("  [%s/%s%s] %s\n", a.AgentName, a.Type, origin,
					truncate(a.Content, 200))
			}
		}
	}
	fmt.Println(strings.Repeat("-", 60))
}

func promptReviewer(scanner *bufio.Scanner) (outcome, humanInput string) {
	fmt.Print("\nApprove? [y=approve / n=reject / or type input to provide]: ")
	if !scanner.Scan() {
		return "rejected", ""
	}
	response := strings.TrimSpace(scanner.Text())
	switch strings.ToLower(response) {
	case "y", "yes":
		return "approved", ""
	case "n", "no":
		fmt.Print("Rejection reason (optional): ")
		if scanner.Scan() {
			return "rejected", strings.TrimSpace(scanner.Text())
		}
		return "rejected", ""
	default:
		return "input_provided", response
	}
}
