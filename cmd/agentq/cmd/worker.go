package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	agentqworker "github.com/shiblon/agentq/pkg/workers/agentq"
	"github.com/shiblon/entroq"
	eqworker "github.com/shiblon/entroq/pkg/worker"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var workerCmd = &cobra.Command{
	Use:   "worker",
	Short: "AgentQ worker commands",
}

var workerServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start an AgentQ worker for the named agent",
	Long: `Start an AgentQ worker that claims tasks from the agent's inbox queue,
mints MCP session JWTs, dispatches to the runner, and posts results back to
the supervisor queue.

Key management (choose one):
  --key-file path          load private key from a JWK file (agentq mcp keygen)
  --insecure-no-keys       generate an ephemeral key at startup; pair with
                           agentq mcp serve --insecure-skip-verification`,
	RunE: runWorkerServe,
}

func init() {
	rootCmd.AddCommand(workerCmd)
	workerCmd.AddCommand(workerServeCmd)

	workerServeCmd.Flags().String("agent", "", "Agent name to run (must exist in agents.yaml)")
	workerServeCmd.Flags().String("key-file", "", "Path to private JWK file for minting MCP session JWTs")
	workerServeCmd.Flags().Bool("insecure-no-keys", false, "Generate an ephemeral signing key at startup. Pair with --insecure-skip-verification on the MCP server.")
	workerServeCmd.Flags().String("reply-queue", "agentq/supervisor/inbox", "Queue to post results to")
	workerServeCmd.Flags().String("issuer", "agentq", "Issuer claim placed in minted JWTs")

	_ = workerServeCmd.MarkFlagRequired("agent")
}

func runWorkerServe(cmd *cobra.Command, _ []string) error {
	agentName, _ := cmd.Flags().GetString("agent")
	keyFile, _ := cmd.Flags().GetString("key-file")
	noKeys, _ := cmd.Flags().GetBool("insecure-no-keys")
	replyQueue, _ := cmd.Flags().GetString("reply-queue")
	issuer, _ := cmd.Flags().GetString("issuer")

	if keyFile == "" && !noKeys {
		return fmt.Errorf("one of --key-file or --insecure-no-keys is required")
	}
	if keyFile != "" && noKeys {
		return fmt.Errorf("--key-file and --insecure-no-keys are mutually exclusive")
	}

	configFile := viper.GetString("config")
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	agent, ok := cfg.Get(agentName)
	if !ok {
		return fmt.Errorf("agent %q not found in agents.yaml", agentName)
	}
	if agent.RunnerURL == "" {
		return fmt.Errorf("agent %q has no runner_url in agents.yaml", agentName)
	}
	if agent.MCPAddr == "" {
		return fmt.Errorf("agent %q has no mcp_addr in agents.yaml", agentName)
	}
	if agent.Queue == "" {
		return fmt.Errorf("agent %q has no queue in agents.yaml", agentName)
	}

	var kp *mcp.KeyPair
	if noKeys {
		log.Printf("worker %s: --insecure-no-keys: generating ephemeral signing key", agentName)
		kp, err = mcp.GenerateEphemeralKey()
		if err != nil {
			return fmt.Errorf("generate ephemeral key: %w", err)
		}
	} else {
		key, err := mcp.LoadPrivateKey(keyFile)
		if err != nil {
			return fmt.Errorf("load key: %w", err)
		}
		kp = &mcp.KeyPair{Private: key}
	}

	// Validate the ceiling at startup rather than on the first task. Grants
	// are checked against a placeholder root so a scopeless grant, which is
	// rooted at the task workdir later, still prices correctly here.
	if err := rootAt(agent.Grants, "/agentq-startup-probe").Validate(); err != nil {
		return fmt.Errorf("agent %q grants: %w", agentName, err)
	}

	var systemPrompt string
	if agent.PromptFile != "" {
		b, err := os.ReadFile(agent.PromptFile)
		if err != nil {
			return fmt.Errorf("read prompt file %q for agent %q: %w", agent.PromptFile, agentName, err)
		}
		systemPrompt = string(b)
		log.Printf("worker %s: loaded system prompt from %s", agentName, agent.PromptFile)
	}

	log.Printf("worker %s: claiming from %q, grants=%s, runner=%s",
		agentName, agent.Queue, strings.Join(agent.Grants.Tools(), ","), agent.RunnerURL)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	workerCfg := agentqworker.Config{
		Name:         agentName,
		Description:  agent.Description,
		Grants:       agent.Grants,
		PrivKey:      kp.Private,
		Issuer:       issuer,
		MCPAddr:      agent.MCPAddr,
		RunnerURL:    agent.RunnerURL,
		ReplyQueue:   replyQueue,
		SystemPrompt: systemPrompt,
	}
	w := agentqworker.New(workerCfg, eq)

	// SIGHUP reloads the signing key from --key-file (no-op with --insecure-no-keys).
	if keyFile != "" {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGHUP)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-sigs:
					key, err := mcp.LoadPrivateKey(keyFile)
					if err != nil {
						log.Printf("worker %s: key reload failed: %v", agentName, err)
					} else {
						w.ReloadKey(key)
						log.Printf("worker %s: signing key reloaded", agentName)
					}
				}
			}
		}()
	}

	log.Printf("worker %s: starting on queue %q", agentName, agent.Queue)
	return eqworker.New(eq,
		eqworker.WithDoModify(func(ctx context.Context, task *entroq.Task, appTask models.Task, _ []*entroq.Doc) ([]entroq.ModifyArg, error) {
			return w.ProcessTask(ctx, task, appTask)
		}),
	).Run(ctx, eqworker.Watching(agent.Queue))
}

// rootAt fills in a root for any grant that did not name one, so a grant set
// can be validated before any task supplies a workdir.
func rootAt(grants mcp.GrantSet, root string) mcp.GrantSet {
	out := make(mcp.GrantSet, len(grants))
	for i, g := range grants {
		if g.Scope.Root == "" {
			g.Scope.Root = root
		}
		out[i] = g
	}
	return out
}
