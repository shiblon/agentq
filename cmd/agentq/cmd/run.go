package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/shiblon/agentq/pkg/approval"
	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/workers/exec"
	"github.com/shiblon/agentq/pkg/workspace"
	"github.com/shiblon/entroq"
	"golang.org/x/sync/errgroup"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run a legacy exec-worker agent",
	Long: `Run a legacy exec-worker agent from agents.yaml.

For the supervisor or new runner-based agents, use the dedicated subcommands:
  agentq supervisor serve   start the supervisor worker
  agentq worker serve       start a runner-based leaf-agent worker

Use --all to start every exec-worker agent in agents.yaml concurrently.
The supervisor is NOT started by --all; run it separately with
'agentq supervisor serve'.`,
	RunE: runAgent,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().String("agent", "", "Agent name to run (mutually exclusive with --all)")
	runCmd.Flags().Bool("all", false, "Start all exec-worker agents from agents.yaml concurrently")
	viper.BindPFlag("agent", runCmd.Flags().Lookup("agent"))
	runCmd.Flags().Bool("commit-work", false, "Git-commit and push target repo changes after each task")
	viper.BindPFlag("commit_work", runCmd.Flags().Lookup("commit-work"))
	runCmd.Flags().String("approval-key", "", "Base64 root key for signing/verifying approval tokens (env: AGENTQ_APPROVAL_KEY)")
	runCmd.Flags().String("approval-key-file", "", "File containing base64 root key for approval tokens (Vault Agent / secret rotation)")
	viper.BindPFlag("approval_key", runCmd.Flags().Lookup("approval-key"))
	viper.BindPFlag("approval_key_file", runCmd.Flags().Lookup("approval-key-file"))
}

func runAgent(cmd *cobra.Command, args []string) error {
	all, _ := cmd.Flags().GetBool("all")
	agentName, _ := cmd.Flags().GetString("agent")

	if !all && agentName == "" {
		return fmt.Errorf("one of --agent or --all is required")
	}
	if all && agentName != "" {
		return fmt.Errorf("--agent and --all are mutually exclusive")
	}
	if agentName == "supervisor" {
		return fmt.Errorf("the supervisor is no longer started with 'agentq run'; use 'agentq supervisor serve' instead")
	}

	configFile := viper.GetString("config")
	ctx := cmd.Context()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	apKey, err := resolveApprovalKey()
	if err != nil {
		return fmt.Errorf("resolve approval key: %w", err)
	}
	if apKey == "" {
		log.Printf("approval tokens disabled (set --approval-key or --approval-key-file to enable)")
	}

	if all {
		return runAll(ctx, eq, cfg, apKey)
	}
	return runExec(ctx, eq, cfg, agentName, apKey)
}

// resolveApprovalKey reads the approval key from flag or file.
func resolveApprovalKey() (string, error) {
	if f := viper.GetString("approval_key_file"); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("read approval key file %q: %w", f, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return viper.GetString("approval_key"), nil
}

// runAll starts every exec-worker agent defined in cfg concurrently.
// The supervisor must be started separately with 'agentq supervisor serve'.
func runAll(ctx context.Context, eq *entroq.EntroQ, cfg *config.Config, apKey string) error {
	g, ctx := errgroup.WithContext(ctx)
	for _, a := range cfg.Agents {
		g.Go(func() error { return runExec(ctx, eq, cfg, a.Name, apKey) })
	}
	log.Printf("run --all: %d agent(s) started (supervisor not included)", len(cfg.Agents))
	return g.Wait()
}

func runExec(ctx context.Context, eq *entroq.EntroQ, cfg *config.Config, agentName, apKey string) error {
	agent, ok := cfg.Get(agentName)
	if !ok {
		return fmt.Errorf("agent %q not found in config; define it in agents.yaml", agentName)
	}
	if agent.Cmd == "" {
		return fmt.Errorf("agent %q has no cmd defined in agents.yaml", agentName)
	}
	if agent.Queue == "" {
		return fmt.Errorf("agent %q has no queue defined in agents.yaml", agentName)
	}

	opts := []exec.Option{}
	if agent.PromptFile != "" {
		opts = append(opts, exec.WithPromptFile(agent.PromptFile))
		log.Printf("%s: prompt file %q", agentName, agent.PromptFile)
	}
	if agent.ApprovalSuffix != "" {
		opts = append(opts, exec.WithApprovalSuffix(agent.ApprovalSuffix))
		log.Printf("%s: approval suffix %q", agentName, agent.ApprovalSuffix)
	}
	if apKey != "" {
		verifier, err := approval.NewVerifierFromBase64(apKey)
		if err != nil {
			return fmt.Errorf("%s: create approval verifier: %w", agentName, err)
		}
		opts = append(opts, exec.WithVerifier(verifier))
		log.Printf("%s: approval token verification enabled", agentName)
	}

	ws := cfg.ResolvedWorkspace()
	if ws.Root != "" && ws.Self != "" {
		w, err := workspace.New(ws.Root, ws.Self)
		if err != nil {
			return fmt.Errorf("init workspace: %w", err)
		}
		opts = append(opts, exec.WithWorkspace(w))
		log.Printf("%s: workspace root=%q self=%q", agentName, ws.Root, ws.Self)
		if viper.GetBool("commit_work") {
			opts = append(opts, exec.WithCommitWork(true))
			log.Printf("%s: commit-work enabled", agentName)
		}
	}

	w := exec.New(agentName, agent.Cmd, eq, opts...)
	log.Printf("%s: claiming from %q, cmd=%q", agentName, agent.Queue, agent.Cmd)

	for {
		if ctx.Err() != nil {
			return nil
		}
		task, err := eq.Claim(ctx, entroq.From(agent.Queue))
		if err != nil {
			if entroq.IsCanceled(err) {
				return nil
			}
			log.Printf("%s: claim error: %v", agentName, err)
			continue
		}
		mods, err := w.ProcessTask(ctx, task)
		if err != nil {
			log.Printf("%s: process error (attempt %d): %v", agentName, task.Attempt+1, err)
			mods = []entroq.ModifyArg{retryMod(task, err.Error())}
		}
		if _, err := eq.Modify(ctx, mods...); err != nil {
			log.Printf("%s: modify error: %v", agentName, err)
		}
	}
}
