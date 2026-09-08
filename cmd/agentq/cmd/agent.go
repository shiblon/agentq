package cmd

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/shiblon/agentq/pkg/config"
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

var agentUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update an agent persona in the config",
	RunE:  runAgentUpdate,
}

func init() {
	rootCmd.AddCommand(agentCmd)
	agentCmd.AddCommand(agentAddCmd, agentListCmd, agentRemoveCmd, agentUpdateCmd)

	agentAddCmd.Flags().String("name", "", "Agent name, short lowercase (required)")
	agentAddCmd.Flags().String("queue", "", "Queue to claim from, e.g. agentq/coder/inbox (required)")
	agentAddCmd.Flags().String("description", "", "One-line description used by supervisor for routing (required)")
	agentAddCmd.Flags().String("prompt-file", "", "Path to system prompt file (optional)")
	agentAddCmd.MarkFlagRequired("name")
	agentAddCmd.MarkFlagRequired("queue")
	agentAddCmd.MarkFlagRequired("description")

	agentRemoveCmd.Flags().String("name", "", "Agent name to remove (required)")
	agentRemoveCmd.MarkFlagRequired("name")

	agentUpdateCmd.Flags().String("name", "", "Agent name to update (required)")
	agentUpdateCmd.Flags().String("queue", "", "New queue name")
	agentUpdateCmd.Flags().String("description", "", "New description")
	agentUpdateCmd.Flags().String("prompt-file", "", "New prompt file path")
	agentUpdateCmd.MarkFlagRequired("name")
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

	if err := cfg.Add(config.Agent{
		Name:        name,
		Queue:       queue,
		Description: desc,
		PromptFile:  promptFile,
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
	fmt.Fprintln(w, "NAME\tQUEUE\tLEGS\tDESCRIPTION")
	for _, a := range cfg.Agents {
		legs := "none"
		if len(a.Legs) > 0 {
			legs = strings.Join(a.Legs, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.Name, a.Queue, legs, a.Description)
	}
	return w.Flush()
}

func runAgentUpdate(cmd *cobra.Command, args []string) error {
	configFile := viper.GetString("config")
	name, _ := cmd.Flags().GetString("name")

	cfg, err := config.Load(configFile)
	if err != nil {
		return err
	}

	queue, _ := cmd.Flags().GetString("queue")
	desc, _ := cmd.Flags().GetString("description")
	promptFile, _ := cmd.Flags().GetString("prompt-file")

	if err := cfg.Update(name, config.Agent{
		Queue:       queue,
		Description: desc,
		PromptFile:  promptFile,
	}); err != nil {
		return err
	}

	if err := cfg.Save(configFile); err != nil {
		return err
	}

	fmt.Printf("updated agent %q\n", name)
	return nil
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
