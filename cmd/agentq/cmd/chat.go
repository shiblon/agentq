package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/agentq/pkg/workflow"
	"github.com/shiblon/entroq"
	"github.com/spf13/cobra"
)

var chatCmd = &cobra.Command{
	Use:   "chat",
	Short: "Start an interactive conversation with the supervisor",
	Long: `Start a turn-based REPL with the supervisor.

A single session is created on the first turn. All subsequent messages are
submitted as follow-ups to that session; the supervisor maintains conversation
context via its transcript artifact. Replies (including async agent results)
are claimed continuously from the session reply queue and printed as they arrive.

Type 'quit' or press Ctrl-D to exit.`,
	RunE: runChat,
}

func init() {
	rootCmd.AddCommand(chatCmd)
	chatCmd.Flags().String("user", "cli", "User ID to associate with the session")
	chatCmd.Flags().String("repo", "", "Workspace repo path for agents to work in")
	chatCmd.Flags().String("supervisor-queue", "agentq/supervisor/inbox", "Supervisor inbox queue")
}

func runChat(cmd *cobra.Command, _ []string) error {
	userID, _ := cmd.Flags().GetString("user")
	repo, _ := cmd.Flags().GetString("repo")
	supervisorQueue, _ := cmd.Flags().GetString("supervisor-queue")

	ctx := cmd.Context()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	st := store.New(eq)

	fmt.Fprintln(os.Stderr, "agentq chat — Ctrl-D or 'quit' to exit")

	var sessionID string
	var replyQueue string

	scanner := bufio.NewScanner(os.Stdin)

	for {
		fmt.Fprint(os.Stderr, "\n> ")
		if !scanner.Scan() {
			break
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "quit" || input == "exit" {
			break
		}

		if sessionID == "" {
			// First turn: create the session and start the reply drain goroutine.
			result, err := workflow.SubmitSession(ctx, st, eq, workflow.SubmitRequest{
				UserID:          userID,
				Prompt:          input,
				Messages:        []models.Message{models.TextMessage("user", input)},
				Repo:            repo,
				SupervisorQueue: supervisorQueue,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				continue
			}
			sessionID = result.SessionID
			replyQueue = models.UserReplyQueue(sessionID)

			// Drain the reply queue in the background, printing each result.
			go drainReplies(ctx, eq, replyQueue)
		} else {
			if err := workflow.SubmitFollowUp(ctx, eq, sessionID, input, supervisorQueue); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
			}
		}
	}

	fmt.Fprintln(os.Stderr, "\nBye.")
	return nil
}

func drainReplies(ctx context.Context, eq *entroq.EntroQ, replyQueue string) {
	for {
		task, err := eq.Claim(ctx, entroq.From(replyQueue))
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("reply queue error: %v", err)
			return
		}

		var appTask models.Task
		if err := json.Unmarshal(task.Value, &appTask); err != nil {
			log.Printf("decode reply: %v", err)
			if _, err := eq.Modify(ctx, task.Delete()); err != nil {
				log.Printf("delete bad reply: %v", err)
			}
			continue
		}

		output, _ := appTask.Payload["output"].(string)
		fromAgent, _ := appTask.Payload["from_agent"].(string)

		if _, err := eq.Modify(ctx, task.Delete()); err != nil {
			log.Printf("delete reply: %v", err)
		}

		fmt.Fprintf(os.Stderr, "[%s]\n", fromAgent)
		fmt.Println(output)
		fmt.Fprint(os.Stderr, "\n> ")
	}
}
