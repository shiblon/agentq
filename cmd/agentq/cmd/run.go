package cmd

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/shiblon/agentq/pkg/auth"
	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/llm"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/workers/exec"
	"github.com/shiblon/agentq/pkg/workers/supervisor"
	"github.com/shiblon/agentq/pkg/workspace"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"golang.org/x/sync/errgroup"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run an agent worker",
	Long: `Run a worker for the named agent. For the supervisor, starts the
orchestration worker. For any other agent, reads cmd/queue/approval_suffix
from agents.yaml and starts an exec worker that pipes tasks to that command.

Use --all to start the supervisor and every agent in agents.yaml concurrently.`,
	RunE: runAgent,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().String("agent", "", "Agent name to run (mutually exclusive with --all)")
	runCmd.Flags().Bool("all", false, "Start the supervisor and all agents from the config file concurrently")
	viper.BindPFlag("agent", runCmd.Flags().Lookup("agent"))
	runCmd.Flags().String("llm-addr", "", "Ollama base URL for the supervisor (env: AGENTQ_LLM_ADDR)")
	viper.BindPFlag("llm_addr", runCmd.Flags().Lookup("llm-addr"))
	runCmd.Flags().String("llm-model", "", "LLM model name for the supervisor (env: AGENTQ_LLM_MODEL)")
	viper.BindPFlag("llm_model", runCmd.Flags().Lookup("llm-model"))
	runCmd.Flags().Bool("commit-work", false, "Git-commit and push target repo changes after each task")
	viper.BindPFlag("commit_work", runCmd.Flags().Lookup("commit-work"))
	runCmd.Flags().String("token-url", "", "OIDC token endpoint for delegated agent token exchange (env: AGENTQ_TOKEN_URL)")
	runCmd.Flags().String("client-id", "", "OAuth client ID for token exchange (env: AGENTQ_CLIENT_ID)")
	runCmd.Flags().String("client-secret", "", "OAuth client secret for token exchange (env: AGENTQ_CLIENT_SECRET)")
	runCmd.Flags().String("client-id-file", "", "File containing OAuth client ID (Vault Agent / secret rotation)")
	runCmd.Flags().String("client-secret-file", "", "File containing OAuth client secret (Vault Agent / secret rotation)")
	viper.BindPFlag("token_url", runCmd.Flags().Lookup("token-url"))
	viper.BindPFlag("client_id", runCmd.Flags().Lookup("client-id"))
	viper.BindPFlag("client_secret", runCmd.Flags().Lookup("client-secret"))
	viper.BindPFlag("client_id_file", runCmd.Flags().Lookup("client-id-file"))
	viper.BindPFlag("client_secret_file", runCmd.Flags().Lookup("client-secret-file"))
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

	eqAddr := viper.GetString("eq_addr")
	configFile := viper.GetString("config")
	ctx := cmd.Context()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	if all {
		return runAll(ctx, eq, cfg)
	}
	if agentName == "supervisor" {
		return runSupervisor(ctx, eq, cfg)
	}
	return runExec(ctx, eq, cfg, agentName)
}

// runAll starts the supervisor and every agent defined in cfg concurrently.
// All workers share the same context; the first to return a non-nil error
// cancels the rest.
func runAll(ctx context.Context, eq *entroq.EntroQ, cfg *config.Config) error {
	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error { return runSupervisor(ctx, eq, cfg) })

	for _, a := range cfg.Agents {
		g.Go(func() error { return runExec(ctx, eq, cfg, a.Name) })
	}

	log.Printf("run --all: supervisor + %d agent(s) started", len(cfg.Agents))
	return g.Wait()
}

func runSupervisor(ctx context.Context, eq *entroq.EntroQ, cfg *config.Config) error {
	llmAddr := viper.GetString("llm_addr")
	llmModel := viper.GetString("llm_model")
	tokenURL := viper.GetString("token_url")
	clientID := viper.GetString("client_id")
	clientSecret := viper.GetString("client_secret")
	clientIDFile := viper.GetString("client_id_file")
	clientSecretFile := viper.GetString("client_secret_file")

	var llmClient llm.Client
	if llmAddr != "" {
		llmClient = llm.NewOllamaClient(llm.OllamaConfig{BaseURL: llmAddr, Model: llmModel})
		log.Printf("supervisor: using LLM at %s model=%q", llmAddr, llmModel)
	} else {
		log.Printf("supervisor: no LLM configured, using keyword routing")
	}

	var exchanger *auth.TokenExchanger
	if tokenURL != "" {
		if clientIDFile != "" && clientSecretFile != "" {
			var err error
			exchanger, err = auth.NewTokenExchangerFromFiles(tokenURL, clientIDFile, clientSecretFile)
			if err != nil {
				return fmt.Errorf("load token exchanger credentials: %w", err)
			}
			log.Printf("supervisor: token exchange enabled via %s (file-based credentials; SIGHUP reloads)", tokenURL)
			// Reload credentials on SIGHUP for Vault Agent / secret rotation.
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGHUP)
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-sigs:
						if err := exchanger.Reload(); err != nil {
							log.Printf("supervisor: credential reload: %v", err)
						} else {
							log.Printf("supervisor: credentials reloaded")
						}
					}
				}
			}()
		} else if clientID != "" {
			exchanger = auth.NewTokenExchanger(tokenURL, clientID, clientSecret)
			log.Printf("supervisor: token exchange enabled via %s client=%s", tokenURL, clientID)
		} else {
			log.Printf("supervisor: --token-url set but no credentials provided (set --client-id or --client-id-file)")
		}
	} else {
		log.Printf("supervisor: token exchange disabled (set --token-url to enable)")
	}

	supCfg := &models.AgentConfig{
		Name:       "supervisor",
		InputQueue: "supervisor",
	}

	log.Printf("supervisor starting, claiming from queue %q", supCfg.InputQueue)

	for {
		if ctx.Err() != nil {
			return nil
		}
		task, err := eq.Claim(ctx, entroq.From(supCfg.InputQueue))
		if err != nil {
			if entroq.IsCanceled(err) {
				return nil
			}
			log.Printf("supervisor: claim error: %v", err)
			continue
		}

		opts := []supervisor.Option{
			supervisor.WithConfig(func(c *models.AgentConfig) { *c = *supCfg }),
			supervisor.WithAgents(cfg.Agents),
		}
		if llmClient != nil {
			opts = append(opts, supervisor.WithLLM(llmClient))
		}
		if cfg.Rubric != "" {
			opts = append(opts, supervisor.WithRubric(cfg.Rubric))
		}
		if exchanger != nil {
			opts = append(opts, supervisor.WithTokenExchanger(exchanger))
		}
		sup := supervisor.New(eq, opts...)

		mods, err := sup.ProcessTask(ctx, task)
		if err != nil {
			log.Printf("supervisor: process error (attempt %d): %v", task.Attempt+1, err)
			mods = []entroq.ModifyArg{retryMod(task, err.Error())}
		}
		if _, err := eq.Modify(ctx, mods...); err != nil {
			log.Printf("supervisor: modify error: %v", err)
		}
	}
}

func runExec(ctx context.Context, eq *entroq.EntroQ, cfg *config.Config, agentName string) error {
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
