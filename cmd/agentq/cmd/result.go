package cmd

import (
	"context"
	"fmt"

	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/store"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var resultCmd = &cobra.Command{
	Use:   "result <session-id>",
	Short: "Print the final output from a completed session",
	Long: `Prints the content of the most recent specialist artifact from a session.
Supervisor dispatch and routing artifacts are skipped. Inherited (context)
artifacts are shown only if no fresh work exists.

Exits non-zero if the session is not yet complete.`,
	Args: cobra.ExactArgs(1),
	RunE: runResult,
}

func init() {
	rootCmd.AddCommand(resultCmd)
	resultCmd.Flags().Bool("all", false, "Print all specialist artifacts, not just the last one")
}

func runResult(cmd *cobra.Command, args []string) error {
	sessionID := args[0]
	eqAddr := viper.GetString("eq_addr")
	all, _ := cmd.Flags().GetBool("all")

	ctx := context.Background()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	st := store.New(eq)
	session, err := st.GetSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}

	switch session.Status {
	case "pending", "in_progress":
		return fmt.Errorf("session %s is still running (status: %s); use 'agentq wait' first", sessionID, session.Status)
	case "awaiting_review":
		return fmt.Errorf("session %s is awaiting human review (run: agentq review)", sessionID)
	}

	artifacts := specialistArtifacts(session)
	if len(artifacts) == 0 {
		fmt.Println("(no specialist output found)")
		return nil
	}

	if all {
		for i, a := range artifacts {
			if i > 0 {
				fmt.Println("\n---")
			}
			origin := ""
			if a.OriginSessionID != "" {
				origin = fmt.Sprintf(" [inherited from %s]", a.OriginSessionID)
			}
			fmt.Printf("[%s/%s%s]\n", a.AgentName, a.Type, origin)
			fmt.Println(a.Content)
		}
		return nil
	}

	// Default: print only the last fresh specialist artifact.
	// If there are no fresh ones, fall back to the last inherited one.
	last := lastSpecialistArtifact(artifacts)
	fmt.Println(last.Content)
	return nil
}

// specialistArtifacts returns non-supervisor artifacts in session order.
func specialistArtifacts(session *models.Session) []models.Artifact {
	var out []models.Artifact
	for _, a := range session.Artifacts {
		if a.AgentName == "supervisor" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// lastSpecialistArtifact returns the most recent fresh (non-inherited) artifact,
// falling back to the most recent inherited one if no fresh work exists.
func lastSpecialistArtifact(artifacts []models.Artifact) models.Artifact {
	// Walk backwards to find the last fresh artifact.
	for i := len(artifacts) - 1; i >= 0; i-- {
		if artifacts[i].OriginSessionID == "" {
			return artifacts[i]
		}
	}
	// All inherited -- return the last one.
	return artifacts[len(artifacts)-1]
}
