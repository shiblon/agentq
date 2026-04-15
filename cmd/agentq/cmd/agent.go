package cmd

import (
	"fmt"
	"log"
	"os"
	"text/tabwriter"

	"github.com/shiblon/agentq/pkg/config"
	"github.com/shiblon/agentq/pkg/workers/exec"
	"github.com/shiblon/entroq"
	"github.com/shiblon/entroq/pkg/backend/eqgrpc"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var agentCmd = &cobra.Command{
	Use:   "agent",
	Short: "Manage agent personas",
}

var agentAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add an agent persona to the config",
	RunE:  runAgentAdd,
}

var agentListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured agent personas",
	RunE:  runAgentList,
}

var agentRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove an agent persona from the config",
	RunE:  runAgentRemove,
}

var agentRunCmd = &cobra.Command{
	Use:   "run <name>",
	Short: "Start the worker for a named agent persona",
	Args:  cobra.ExactArgs(1),
	RunE:  runAgentRun,
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentAddCmd, agentListCmd, agentRemoveCmd, agentRunCmd)

	agentAddCmd.Flags().String("name", "", "Agent name, short lowercase (required)")
	agentAddCmd.Flags().String("queue", "", "Queue to claim from, e.g. coder_queue (required)")
	agentAddCmd.Flags().String("description", "", "One-line description used by supervisor for routing (required)")
	agentAddCmd.Flags().String("prompt-file", "", "Path to system prompt file (optional)")
	agentAddCmd.Flags().String("cmd", "", "Shell command to run for each task (optional, required for exec workers)")
	agentAddCmd.MarkFlagRequired("name")
	agentAddCmd.MarkFlagRequired("queue")
	agentAddCmd.MarkFlagRequired("description")

	agentRemoveCmd.Flags().String("name", "", "Agent name to remove (required)")
	agentRemoveCmd.MarkFlagRequired("name")
}

func runAgentAdd(cmd *cobra.Command, args []string) error {
	configFile := viper.GetString("config")
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}

	name, _ := cmd.Flags().GetString("name")
	queue, _ := cmd.Flags().GetString("queue")
	desc, _ := cmd.Flags().GetString("description")
	promptFile, _ := cmd.Flags().GetString("prompt-file")
	agentCmd, _ := cmd.Flags().GetString("cmd")

	if err := cfg.Add(config.Agent{
		Name:        name,
		Queue:       queue,
		Description: desc,
		PromptFile:  promptFile,
		Cmd:         agentCmd,
	}); err != nil {
		return err
	}

	if err := cfg.Save(configFile); err != nil {
		return err
	}

	fmt.Printf("added agent %q -> queue %q\n", name, queue)
	return nil
}

func runAgentList(cmd *cobra.Command, args []string) error {
	configFile := viper.GetString("config")
	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}

	if len(cfg.Agents) == 0 {
		fmt.Println("no agents configured (use 'agentq agent add' to add one)")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tQUEUE\tDESCRIPTION\tCMD")
	for _, a := range cfg.Agents {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, a.Queue, a.Description, a.Cmd)
	}
	return w.Flush()
}

func runAgentRemove(cmd *cobra.Command, args []string) error {
	configFile := viper.GetString("config")
	name, _ := cmd.Flags().GetString("name")

	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}
	if err := cfg.Remove(name); err != nil {
		return err
	}
	if err := cfg.Save(configFile); err != nil {
		return err
	}

	fmt.Printf("removed agent %q\n", name)
	return nil
}

func runAgentRun(cmd *cobra.Command, args []string) error {
	name := args[0]
	configFile := viper.GetString("config")
	eqAddr := viper.GetString("eq_addr")

	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}

	a, ok := cfg.Get(name)
	if !ok {
		return fmt.Errorf("agent %q not found in %s", name, configFile)
	}
	if a.Cmd == "" {
		return fmt.Errorf("agent %q has no cmd configured", name)
	}

	ctx := cmd.Context()
	eq, err := entroq.New(ctx, eqgrpc.Opener(eqAddr, eqgrpc.WithInsecure()))
	if err != nil {
		return fmt.Errorf("connect to eq at %s: %w", eqAddr, err)
	}
	defer eq.Close()

	var opts []exec.Option
	if a.PromptFile != "" {
		opts = append(opts, exec.WithPromptFile(a.PromptFile))
		log.Printf("agent %s: prompt file %q", name, a.PromptFile)
	}

	w := exec.New(name, a.Cmd, eq, opts...)
	log.Printf("agent %s: claiming from %q, cmd=%q", name, a.Queue, a.Cmd)

	for {
		if ctx.Err() != nil {
			return nil
		}
		task, err := eq.Claim(ctx, entroq.From(a.Queue))
		if err != nil {
			if entroq.IsCanceled(err) {
				return nil
			}
			log.Printf("agent %s: claim error: %v", name, err)
			continue
		}
		mods, err := w.ProcessTask(ctx, task)
		if err != nil {
			log.Printf("agent %s: process error (attempt %d): %v", name, task.Attempt+1, err)
			mods = []entroq.ModifyArg{retryMod(task, err.Error())}
		}
		if _, err := eq.Modify(ctx, mods...); err != nil {
			log.Printf("agent %s: modify error: %v", name, err)
		}
	}
}
