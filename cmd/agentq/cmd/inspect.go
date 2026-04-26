package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/spf13/cobra"
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
	inspectCmd.Flags().Bool("short", false, "Print a compact artifact timeline instead of full JSON")
}

func runInspect(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	chain, _ := cmd.Flags().GetBool("chain")
	short, _ := cmd.Flags().GetBool("short")

	ctx := context.Background()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	st := store.New(eq)

	if !chain {
		session, err := st.GetSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("get session: %w", err)
		}
		if short {
			printShortSession(session)
			return nil
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(session)
	}

	// Walk the chain from oldest ancestor to current, collecting sessions.
	var sessions []*models.Session
	id := sessionID
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		s, err := st.GetSession(ctx, id)
		if err != nil {
			return fmt.Errorf("get session %s: %w", id, err)
		}
		sessions = append([]*models.Session{s}, sessions...) // prepend: oldest first
		id = s.ParentSessionID
	}

	if short {
		for i, s := range sessions {
			if i > 0 {
				fmt.Println()
			}
			printShortSession(s)
		}
		return nil
	}

	// JSON chain output.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	fmt.Fprintf(os.Stdout, "// chain of %d session(s), oldest first\n", len(sessions))
	// Re-encode as []any to satisfy the encoder without a type change.
	var asAny []any
	for _, s := range sessions {
		asAny = append(asAny, s)
	}
	return enc.Encode(asAny)
}

// printShortSession prints a compact, human-readable artifact timeline.
func printShortSession(s *models.Session) {
	parent := ""
	if s.ParentSessionID != "" {
		parent = fmt.Sprintf(" (continues %s)", s.ParentSessionID)
	}
	fmt.Printf("session %s  [%s]%s\n", s.ID, s.Status, parent)
	fmt.Printf("  prompt: %s\n", truncate(s.Prompt, 80))

	if len(s.Artifacts) == 0 {
		fmt.Println("  (no artifacts)")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, a := range s.Artifacts {
		age := formatAge(time.Since(a.CreatedAt))
		origin := ""
		if a.OriginSessionID != "" {
			origin = " [inherited]"
		}
		snippet := truncate(a.Content, 60)
		fmt.Fprintf(w, "  %s\t[%s/%s%s]\t%s\n", age, a.AgentName, a.Type, origin, snippet)
	}
	w.Flush()
}
