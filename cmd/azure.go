package cmd

import "github.com/spf13/cobra"

var (
	azureTenantID     string
	azureUseCLI       bool
	azureClientID     string
	azureClientSecret string
)

var azureCmd = &cobra.Command{
	Use:   "azure",
	Short: "Azure — lock subscriptions with a deny-all Policy assignment",
	Long: `Azure Quota API is request-based and many quotas reject 0, so quota-locker
locks an Azure subscription by:

  1. Creating a subscription-scoped custom Policy definition with a single
     'deny' rule that matches every resource type.
  2. Assigning that definition to the subscription.

Existing resources keep running; new creates/updates are blocked.

Default auth is the Entra ID OAuth 2.0 device code flow. Pass --cli to use
the credentials cached by 'az login' instead.`,
}

func init() {
	pf := azureCmd.PersistentFlags()
	pf.StringVar(&azureTenantID, "tenant", "", "Entra ID tenant ID or domain (required unless --cli)")
	pf.BoolVar(&azureUseCLI, "cli", false, "use credentials cached by 'az login' instead of device code")
	pf.StringVar(&azureClientID, "client-id", "", "service-principal app ID (alternative to device/CLI)")
	pf.StringVar(&azureClientSecret, "client-secret", "", "service-principal secret (paired with --client-id)")
	rootCmd.AddCommand(azureCmd)
}
