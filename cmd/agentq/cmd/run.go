package cmd

import (
	"context"
	"fmt"
	"log"

	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/llm"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/workers/mock"
	"github.com/shiblon/agentq/pkg/workers/supervisor"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run an agent worker",
	RunE:  runAgent,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().String("agent", "", "Agent name (supervisor, coder, reviewer, researcher)")
	runCmd.MarkFlagRequired("agent")
	viper.BindPFlag("agent", runCmd.Flags().Lookup("agent"))
	runCmd.Flags().String("llm-addr", "", "Ollama base URL, e.g. http://localhost:11434 (env: AGENTQ_LLM_ADDR)")
	viper.BindPFlag("llm_addr", runCmd.Flags().Lookup("llm-addr"))
	runCmd.Flags().String("llm-model", "", "LLM model name, e.g. qwen2.5:0.5b (env: AGENTQ_LLM_MODEL)")
	viper.BindPFlag("llm_model", runCmd.Flags().Lookup("llm-model"))
}

func runAgent(cmd *cobra.Command, args []string) error {
	agentName := viper.GetString("agent")
	eqAddr := viper.GetString("eq_addr")
	llmAddr := viper.GetString("llm_addr")
	llmModel := viper.GetString("llm_model")
	configFile := viper.GetString("config")

	ctx := cmd.Context()

	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	cfg, agents, err := loadSupervisorConfig(agentName, configFile)
	if err != nil {
		return err
	}

	var llmClient llm.Client
	if llmAddr != "" {
		llmClient = llm.NewOllamaClient(llm.OllamaConfig{BaseURL: llmAddr, Model: llmModel})
		log.Printf("agent %s: using LLM at %s model=%q", agentName, llmAddr, llmModel)
	} else {
		log.Printf("agent %s: no LLM configured, using keyword routing", agentName)
	}

	log.Printf("agent %s starting, claiming from queue %q", agentName, cfg.InputQueue)

	return claimLoop(ctx, eq, cfg, agentName, llmClient, agents)
}

// loadSupervisorConfig returns an AgentConfig and the full agent list for the
// supervisor, loaded from the config file. For non-supervisor agents it falls
// back to the hardcoded mock configs (they don't need the agent list).
func loadSupervisorConfig(name, configFile string) (*models.AgentConfig, []config.Agent, error) {
	if name != "supervisor" {
		cfg := hardcodedConfig(name)
		if cfg == nil {
			return nil, nil, fmt.Errorf("unknown agent: %q", name)
		}
		return cfg, nil, nil
	}

	cfg, err := config.Load(configFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load agents config: %w", err)
	}

	supCfg := &models.AgentConfig{
		Name:       "supervisor",
		InputQueue: "supervisor",
	}
	return supCfg, cfg.Agents, nil
}

// hardcodedConfig returns a mock AgentConfig for non-supervisor built-in agents.
func hardcodedConfig(name string) *models.AgentConfig {
	switch name {
	case "coder":
		return models.CoderAgent()
	case "reviewer":
		return models.ReviewerAgent()
	case "researcher":
		return models.ResearcherAgent()
	default:
		return nil
	}
}

// claimLoop runs a simple claim-process-modify loop for the given agent.
func claimLoop(ctx context.Context, eq *entroq.EntroQ, cfg *models.AgentConfig, agentName string, llmClient llm.Client, agents []config.Agent) error {
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		task, err := eq.Claim(ctx, entroq.From(cfg.InputQueue))
		if err != nil {
			if entroq.IsCanceled(err) {
				return nil
			}
			log.Printf("agent %s: claim error: %v", agentName, err)
			continue
		}

		mods, err := dispatch(ctx, eq, cfg, agentName, llmClient, agents, task)
		if err != nil {
			log.Printf("agent %s: process error: %v", agentName, err)
			// On error, just release (delete without doing anything else).
			mods = []entroq.ModifyArg{task.Delete()}
		}

		if _, err := eq.Modify(ctx, mods...); err != nil {
			log.Printf("agent %s: modify error: %v", agentName, err)
		}
	}
}

// dispatch routes a claimed task to the appropriate handler.
func dispatch(ctx context.Context, eq *entroq.EntroQ, cfg *models.AgentConfig, agentName string, llmClient llm.Client, agents []config.Agent, task *entroq.Task) ([]entroq.ModifyArg, error) {
	switch agentName {
	case "supervisor":
		opts := []supervisor.Option{
			supervisor.WithConfig(func(c *models.AgentConfig) { *c = *cfg }),
			supervisor.WithAgents(agents),
		}
		if llmClient != nil {
			opts = append(opts, supervisor.WithLLM(llmClient))
		}
		sup := supervisor.New(eq, opts...)
		return sup.ProcessTask(ctx, task)
	default:
		w := mock.New(agentName, eq)
		return w.ProcessTask(ctx, task)
	}
}
