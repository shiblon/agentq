package cmd

import (
	"context"
	"fmt"
	"log"

	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/llm"
	"github.com/shiblon/agentq/pkg/models"
	"github.com/shiblon/agentq/pkg/workers/exec"
	"github.com/shiblon/agentq/pkg/workers/supervisor"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run an agent worker",
	Long: `Run a worker for the named agent. For the supervisor, starts the
orchestration worker. For any other agent, reads cmd/queue/approval_suffix
from agents.yaml and starts an exec worker that pipes tasks to that command.`,
	RunE: runAgent,
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().String("agent", "", "Agent name to run (required)")
	runCmd.MarkFlagRequired("agent")
	viper.BindPFlag("agent", runCmd.Flags().Lookup("agent"))
	runCmd.Flags().String("llm-addr", "", "Ollama base URL for the supervisor (env: AGENTQ_LLM_ADDR)")
	viper.BindPFlag("llm_addr", runCmd.Flags().Lookup("llm-addr"))
	runCmd.Flags().String("llm-model", "", "LLM model name for the supervisor (env: AGENTQ_LLM_MODEL)")
	viper.BindPFlag("llm_model", runCmd.Flags().Lookup("llm-model"))
}

func runAgent(cmd *cobra.Command, args []string) error {
	agentName := viper.GetString("agent")
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

	if agentName == "supervisor" {
		return runSupervisor(ctx, eq, cfg)
	}
	return runExec(ctx, eq, cfg, agentName)
}

func runSupervisor(ctx context.Context, eq *entroq.EntroQ, cfg *config.Config) error {
	llmAddr := viper.GetString("llm_addr")
	llmModel := viper.GetString("llm_model")

	var llmClient llm.Client
	if llmAddr != "" {
		llmClient = llm.NewOllamaClient(llm.OllamaConfig{BaseURL: llmAddr, Model: llmModel})
		log.Printf("supervisor: using LLM at %s model=%q", llmAddr, llmModel)
	} else {
		log.Printf("supervisor: no LLM configured, using keyword routing")
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
	if agent.ApprovalSuffix != "" {
		opts = append(opts, exec.WithApprovalSuffix(agent.ApprovalSuffix))
		log.Printf("%s: approval suffix %q", agentName, agent.ApprovalSuffix)
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
