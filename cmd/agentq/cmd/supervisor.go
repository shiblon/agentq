package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/shiblon/agentq/pkg/approval"
	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/mcp"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/workers/supervisor"
	"github.com/shiblon/entroq"
	eqworker "github.com/shiblon/entroq/pkg/worker"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var supervisorCmd = &cobra.Command{
	Use:   "supervisor",
	Short: "Supervisor worker commands",
}

var supervisorServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the supervisor worker",
	Long: `Start the supervisor worker. It claims tasks from a single inbox queue,
handling both incoming user prompts and agent replies. Each turn it calls the
runner, which invokes the agent command with the dispatch_to_agent tool wired
to an MCP server backed by EntroQ.

Key management (choose one):
  --key-file path          load private key from a JWK file (agentq mcp keygen)
  --insecure-no-keys       generate an ephemeral key at startup; pair with
                           agentq mcp serve --insecure-skip-verification`,
	RunE: runSupervisorServe,
}

func init() {
	rootCmd.AddCommand(supervisorCmd)
	supervisorCmd.AddCommand(supervisorServeCmd)

	supervisorServeCmd.Flags().String("queue", "agentq/supervisor/inbox", "Supervisor inbox queue name")
	supervisorServeCmd.Flags().String("key-file", "", "Path to private JWK file for minting MCP session JWTs")
	supervisorServeCmd.Flags().Bool("insecure-no-keys", false, "Generate an ephemeral signing key at startup. Pair with --insecure-skip-verification on the MCP server.")
	supervisorServeCmd.Flags().String("issuer", "agentq", "Issuer claim placed in minted JWTs")
	supervisorServeCmd.Flags().String("runner-url", "", "Base URL of the runner microservice, e.g. http://runner:8082")
	supervisorServeCmd.Flags().String("mcp-addr", "", "Base URL of the MCP pool server (must have dispatch_to_agent registered)")
	supervisorServeCmd.Flags().StringSlice("grant-tool", []string{"dispatch_to_agent"}, "Tools the supervisor is granted; repeat or comma-separate")
	supervisorServeCmd.Flags().String("default-workdir", "", "Fallback workdir when session has no workspace configured")
	supervisorServeCmd.Flags().String("prompt", "", "System prompt for the supervisor (overrides built-in default)")
	supervisorServeCmd.Flags().String("prompt-file", "", "Path to a file containing the supervisor system prompt")
	supervisorServeCmd.Flags().Int("max-dispatches", 20, "Maximum number of agent dispatches per session (0 = unlimited)")
	supervisorServeCmd.Flags().String("provenance-key", "", "Base64 root key for verifying session provenance tokens (env: AGENTQ_PROVENANCE_KEY). Must match the key used by the API server.")
	supervisorServeCmd.Flags().String("provenance-key-file", "", "File containing the base64 provenance root key (for secret rotation via Vault Agent or similar)")

	_ = supervisorServeCmd.MarkFlagRequired("runner-url")
	_ = supervisorServeCmd.MarkFlagRequired("mcp-addr")
}

func runSupervisorServe(cmd *cobra.Command, _ []string) error {
	queue, _ := cmd.Flags().GetString("queue")
	keyFile, _ := cmd.Flags().GetString("key-file")
	noKeys, _ := cmd.Flags().GetBool("insecure-no-keys")
	issuer, _ := cmd.Flags().GetString("issuer")
	runnerURL, _ := cmd.Flags().GetString("runner-url")
	mcpAddr, _ := cmd.Flags().GetString("mcp-addr")
	grantTools, _ := cmd.Flags().GetStringSlice("grant-tool")
	defaultWorkdir, _ := cmd.Flags().GetString("default-workdir")
	prompt, _ := cmd.Flags().GetString("prompt")
	promptFile, _ := cmd.Flags().GetString("prompt-file")
	maxDispatches, _ := cmd.Flags().GetInt("max-dispatches")
	provKey, _ := cmd.Flags().GetString("provenance-key")
	provKeyFile, _ := cmd.Flags().GetString("provenance-key-file")

	if promptFile != "" {
		b, err := os.ReadFile(promptFile)
		if err != nil {
			return fmt.Errorf("read prompt file: %w", err)
		}
		prompt = string(b)
	}

	if keyFile == "" && !noKeys {
		return fmt.Errorf("one of --key-file or --insecure-no-keys is required")
	}
	if keyFile != "" && noKeys {
		return fmt.Errorf("--key-file and --insecure-no-keys are mutually exclusive")
	}

	var grants mcp.GrantSet
	for _, t := range grantTools {
		if t = strings.TrimSpace(t); t != "" {
			grants = append(grants, mcp.Grant{Tool: t})
		}
	}
	if err := rootAt(grants, "/agentq-startup-probe").Validate(); err != nil {
		return fmt.Errorf("supervisor grants: %w", err)
	}

	var (
		kp  *mcp.KeyPair
		err error
	)
	if noKeys {
		log.Printf("supervisor: --insecure-no-keys: generating ephemeral signing key")
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

	if provKeyFile != "" {
		b, err := os.ReadFile(provKeyFile)
		if err != nil {
			return fmt.Errorf("read provenance key file: %w", err)
		}
		provKey = string(b)
	}
	var provVerifier *approval.ProvenanceVerifier
	if provKey != "" {
		v, err := approval.NewProvenanceVerifier(provKey)
		if err != nil {
			return fmt.Errorf("create provenance verifier: %w", err)
		}
		provVerifier = v
		log.Printf("supervisor: session provenance verification enabled")
	} else {
		log.Printf("supervisor: session provenance verification disabled (set --provenance-key to enable)")
	}

	log.Printf("supervisor: claiming from %q, runner=%s, mcp=%s, grants=%s", queue, runnerURL, mcpAddr, strings.Join(grants.Tools(), ","))

	// Load agent roster and build system prompt.
	agentCfg, err := config.Load(viper.GetString("config"))
	if err != nil {
		log.Printf("supervisor: could not load agent config: %v (continuing without agent roster)", err)
	}
	var agentInfos []supervisor.AgentInfo
	for _, a := range agentCfg.Agents {
		agentInfos = append(agentInfos, supervisor.AgentInfo{Name: a.Name, Description: a.Description})
	}

	workerCfg := supervisor.Config{
		Issuer:             issuer,
		PrivKey:            kp.Private,
		MCPAddr:            mcpAddr,
		RunnerURL:          runnerURL,
		Grants:             grants,
		DefaultWorkdir:     defaultWorkdir,
		SystemPrompt:       supervisor.BuildSystemPrompt(prompt, agentInfos),
		MaxDispatches:      maxDispatches,
		ProvenanceVerifier: provVerifier,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	w := supervisor.New(workerCfg, eq)

	// SIGHUP reloads the signing key (no-op with --insecure-no-keys).
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
						log.Printf("supervisor: key reload failed: %v", err)
					} else {
						w.ReloadKey(key)
						log.Printf("supervisor: signing key reloaded")
					}
				}
			}
		}()
	}

	log.Printf("supervisor: starting on queue %q", queue)
	return eqworker.New(eq,
		eqworker.WithDoModify(func(ctx context.Context, task *entroq.Task, appTask models.Task, _ []*entroq.Doc) ([]entroq.ModifyArg, error) {
			return w.ProcessTask(ctx, task, appTask)
		}),
	).Run(ctx, eqworker.Watching(queue))
}
