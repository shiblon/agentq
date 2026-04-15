// Package cmd holds the subcommands for agentq.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:   "agentq",
	Short: "agentq - multi-agent task queue prototype",
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	cobra.OnInitialize(initConfig)
	rootCmd.PersistentFlags().String("eq-addr", "localhost:37706", "entroq gRPC server address")
	rootCmd.PersistentFlags().String("config", "agents.yaml", "Path to agents config file (env: AGENTQ_CONFIG)")
	viper.BindPFlag("eq_addr", rootCmd.PersistentFlags().Lookup("eq-addr"))
	viper.BindPFlag("config", rootCmd.PersistentFlags().Lookup("config"))
}

func initConfig() {
	viper.SetEnvPrefix("AGENTQ")
	viper.AutomaticEnv()
}
