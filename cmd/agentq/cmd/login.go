package cmd

import (
	"fmt"

	"github.com/shiblon/agentq/pkg/auth"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate via device flow and store credentials locally",
	Long: `Initiates an RFC 8628 device authorization flow against the configured
OIDC provider. You will be prompted to visit a URL and enter a code.
Credentials are saved to ~/.config/agentq/credentials.json.`,
	RunE: runLogin,
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().String("issuer", "", "OIDC issuer base URL (e.g. http://localhost:8080) (env: AGENTQ_ISSUER)")
	loginCmd.Flags().String("client-id", "", "Public OAuth client ID for device flow (env: AGENTQ_CLIENT_ID)")
	viper.BindPFlag("issuer", loginCmd.Flags().Lookup("issuer"))
	loginCmd.MarkFlagRequired("issuer")
	loginCmd.MarkFlagRequired("client-id")
}

func runLogin(cmd *cobra.Command, args []string) error {
	issuer := viper.GetString("issuer")
	clientID, _ := cmd.Flags().GetString("client-id")

	ctx := cmd.Context()
	client := auth.NewDeviceFlowClient(issuer, clientID)

	dar, err := client.Authorize(ctx)
	if err != nil {
		return fmt.Errorf("start device flow: %w", err)
	}

	verifyURL := dar.VerificationURIComplete
	if verifyURL == "" {
		verifyURL = dar.VerificationURI
	}
	fmt.Printf("\nOpen this URL in your browser:\n\n  %s\n", verifyURL)
	if dar.VerificationURIComplete == "" {
		fmt.Printf("\nEnter code: %s\n", dar.UserCode)
	}
	fmt.Printf("\nWaiting for authorization")

	creds, err := client.Poll(ctx, dar)
	if err != nil {
		return fmt.Errorf("device flow poll: %w", err)
	}

	if err := auth.SaveCredentials(creds); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	fmt.Printf("\nLogged in. Token expires at %s\n", creds.ExpiresAt.Format("15:04:05 2006-01-02"))
	return nil
}
