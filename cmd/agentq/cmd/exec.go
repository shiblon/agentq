package cmd

import (
	"log"

	"github.com/shiblon/agentq/pkg/workers/exec"
	"github.com/shiblon/entroq"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var execCmd = &cobra.Command{
	Use:   "exec",
	Short: "Run an agent worker that delegates to an external CLI",
	Long: `Claims tasks from a queue, builds a prompt from session context,
pipes it to an external command via stdin, and appends stdout as an artifact.

Example:
  agentq exec --agent coder --queue coder_queue \
    --prompt-file config/prompts/coder.txt \
    --cmd "claude --print --dangerously-skip-permissions"

Note: when using "claude --print" as the command, pass --dangerously-skip-permissions
so that Claude does not pause to ask for tool-use approval mid-task.`,
	RunE: runExecWorker,
}

func init() {
	rootCmd.AddCommand(execCmd)
	execCmd.Flags().String("agent", "", "Agent name, used for artifact attribution (required)")
	execCmd.MarkFlagRequired("agent")
	execCmd.Flags().String("queue", "", "Queue to claim tasks from (required)")
	execCmd.MarkFlagRequired("queue")
	execCmd.Flags().String("cmd", "", "Shell command to run for each task, receives prompt on stdin (required)")
	execCmd.MarkFlagRequired("cmd")
	execCmd.Flags().String("prompt-file", "", "Path to system prompt file prepended to session context (env: AGENTQ_PROMPT_FILE)")
	execCmd.Flags().String("reply-queue", "supervisor", "Queue to post results to (env: AGENTQ_REPLY_QUEUE)")
	execCmd.Flags().String("approval-flag", "", "Suffix appended to cmd when the supervisor grants approval, e.g. --dangerously-skip-permissions")

	viper.BindPFlag("exec_agent", execCmd.Flags().Lookup("agent"))
	viper.BindPFlag("exec_queue", execCmd.Flags().Lookup("queue"))
	viper.BindPFlag("exec_cmd", execCmd.Flags().Lookup("cmd"))
	viper.BindPFlag("prompt_file", execCmd.Flags().Lookup("prompt-file"))
	viper.BindPFlag("reply_queue", execCmd.Flags().Lookup("reply-queue"))
	viper.BindPFlag("approval_flag", execCmd.Flags().Lookup("approval-flag"))
}

func runExecWorker(cmd *cobra.Command, args []string) error {
	agentName := viper.GetString("exec_agent")
	queue := viper.GetString("exec_queue")
	shellCmd := viper.GetString("exec_cmd")
	promptFile := viper.GetString("prompt_file")
	replyQueue := viper.GetString("reply_queue")
	approvalFlag := viper.GetString("approval_flag")

	ctx := cmd.Context()

	eq, err := openEQ(ctx)
	if err != nil {
		return err
	}
	defer eq.Close()

	opts := []exec.Option{exec.WithReplyQueue(replyQueue)}
	if promptFile != "" {
		opts = append(opts, exec.WithPromptFile(promptFile))
		log.Printf("exec %s: prompt file %q", agentName, promptFile)
	}
	if approvalFlag != "" {
		opts = append(opts, exec.WithApprovalSuffix(approvalFlag))
		log.Printf("exec %s: approval flag %q", agentName, approvalFlag)
	}

	w := exec.New(agentName, shellCmd, eq, opts...)
	log.Printf("exec %s: claiming from %q, reply to %q, cmd=%q", agentName, queue, replyQueue, shellCmd)

	for {
		if ctx.Err() != nil {
			return nil
		}
		task, err := eq.Claim(ctx, entroq.From(queue))
		if err != nil {
			if entroq.IsCanceled(err) {
				return nil
			}
			log.Printf("exec %s: claim error: %v", agentName, err)
			continue
		}
		mods, err := w.ProcessTask(ctx, task)
		if err != nil {
			log.Printf("exec %s: process error (attempt %d): %v", agentName, task.Attempt+1, err)
			mods = []entroq.ModifyArg{retryMod(task, err.Error())}
		}
		if _, err := eq.Modify(ctx, mods...); err != nil {
			log.Printf("exec %s: modify error: %v", agentName, err)
		}
	}
}
