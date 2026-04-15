package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/shiblon/agentq/pkg/llm"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var llmCmd = &cobra.Command{
	Use:   "llm",
	Short: "Send stdin to an LLM and print the response to stdout",
	Long: `Reads a prompt from stdin, sends it to an LLM, and writes the response
to stdout. Useful as the --cmd for agentq agent add when using a local model.

Example:
  agentq agent add --name=coder --queue=coder_queue \
    --description="Writes and edits code" \
    --prompt-file=config/prompts/coder.txt \
    --cmd="agentq llm --addr=http://localhost:11434 --model=qwen2.5-coder:7b"`,
	RunE: runLLM,
}

func init() {
	rootCmd.AddCommand(llmCmd)
	llmCmd.Flags().String("addr", "http://localhost:11434", "LLM base URL (env: AGENTQ_LLM_ADDR)")
	llmCmd.Flags().String("model", "qwen2.5:1.5b", "Model name (env: AGENTQ_LLM_MODEL)")
	viper.BindPFlag("llm_addr", llmCmd.Flags().Lookup("addr"))
	viper.BindPFlag("llm_model", llmCmd.Flags().Lookup("model"))
}

func runLLM(cmd *cobra.Command, args []string) error {
	addr := viper.GetString("llm_addr")
	model := viper.GetString("llm_model")

	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}

	client := llm.NewOllamaClient(llm.OllamaConfig{BaseURL: addr, Model: model})
	response, err := client.Complete(cmd.Context(), "", string(input))
	if err != nil {
		return fmt.Errorf("llm call: %w", err)
	}

	fmt.Print(response)
	return nil
}
