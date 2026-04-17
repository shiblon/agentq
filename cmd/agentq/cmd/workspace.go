package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shiblon/agentq/pkg/workspace"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var workspaceCmd = &cobra.Command{
	Use:   "workspace",
	Short: "Workspace management commands",
}

var workspaceInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialise a workspace: create root dir and clone or pull the self repo",
	Long: `Creates the workspace root directory and clones the self (agent-settings) repo
into it if not already present, then pulls to bring it up to date.

The workspace root and self path are read from --workspace-root / --workspace-self
(or the AGENTQ_WORKSPACE / AGENTQ_SELF environment variables).

Example:
  agentq workspace init \
    --workspace-root ~/code/src \
    --workspace-self github.com/shiblon/agent-settings
`,
	RunE: runWorkspaceInit,
}

func init() {
	rootCmd.AddCommand(workspaceCmd)
	workspaceCmd.AddCommand(workspaceInitCmd)

	workspaceInitCmd.Flags().String("workspace-root", "", "Workspace root directory (env: AGENTQ_WORKSPACE)")
	workspaceInitCmd.Flags().String("workspace-self", "", "Self repo path relative to root (env: AGENTQ_SELF)")
	workspaceInitCmd.Flags().String("self-remote", "", "Remote URL for cloning self repo (default: https://<self>)")
	viper.BindPFlag("workspace_root", workspaceInitCmd.Flags().Lookup("workspace-root"))
	viper.BindPFlag("workspace_self", workspaceInitCmd.Flags().Lookup("workspace-self"))
	viper.BindPFlag("workspace_self_remote", workspaceInitCmd.Flags().Lookup("self-remote"))
}

func runWorkspaceInit(cmd *cobra.Command, args []string) error {
	root := viper.GetString("workspace_root")
	if root == "" {
		root = os.Getenv("AGENTQ_WORKSPACE")
	}
	if root == "" {
		return fmt.Errorf("workspace root required: use --workspace-root or set AGENTQ_WORKSPACE")
	}
	self := viper.GetString("workspace_self")
	if self == "" {
		self = os.Getenv("AGENTQ_SELF")
	}
	if self == "" {
		return fmt.Errorf("self repo path required: use --workspace-self or set AGENTQ_SELF")
	}
	selfRemote := viper.GetString("workspace_self_remote")

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0755); err != nil {
		return fmt.Errorf("create workspace root %q: %w", absRoot, err)
	}
	fmt.Printf("workspace root: %s\n", absRoot)

	ws, err := workspace.New(absRoot, self)
	if err != nil {
		return err
	}

	ctx := context.Background()
	fmt.Printf("self repo:      %s\n", ws.SelfDir())
	if err := ws.PullSelf(ctx, selfRemote); err != nil {
		return fmt.Errorf("prepare self repo: %w", err)
	}
	fmt.Println("workspace ready.")
	return nil
}
