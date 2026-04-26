package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var sessionsCmd = &cobra.Command{
	Use:   "sessions",
	Short: "Manage and list sessions",
}

var sessionsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List sessions with their current status",
	RunE:  runSessionsList,
}

func init() {
	rootCmd.AddCommand(sessionsCmd)
	sessionsCmd.AddCommand(sessionsListCmd)

	sessionsListCmd.Flags().String("status", "", "Filter by status: pending, in_progress, awaiting_review, completed, failed")
	sessionsListCmd.Flags().Int("limit", 50, "Maximum number of sessions to fetch")
}

func runSessionsList(cmd *cobra.Command, args []string) error {
	eqAddr := viper.GetString("eq_addr")
	statusFilter, _ := cmd.Flags().GetString("status")
	limit, _ := cmd.Flags().GetInt("limit")

	ctx := context.Background()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	st := store.New(eq)
	sessions, err := st.ListSessions(ctx, limit)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	// Filter by status if requested.
	if statusFilter != "" {
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.Status == statusFilter {
				filtered = append(filtered, s)
			}
		}
		sessions = filtered
	}

	if len(sessions) == 0 {
		fmt.Println("no sessions found")
		return nil
	}

	// Sort newest-updated first.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SESSION ID\tSTATUS\tAGO\tPROMPT")
	for _, s := range sessions {
		ago := formatAge(time.Since(s.UpdatedAt))
		prompt := truncate(s.Prompt, 50)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.ID, s.Status, ago, prompt)
	}
	return w.Flush()
}

// formatAge returns a human-readable duration string for a "time ago" display.
func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

