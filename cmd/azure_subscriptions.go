package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	azurepkg "kubeops.dev/quota-locker/pkg/azure"
)

var azureSubscriptionsCmd = &cobra.Command{
	Use:     "subscriptions",
	Aliases: []string{"subs"},
	Short:   "List Azure subscriptions accessible to the authenticated principal",
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cred, err := azurepkg.Credential(ctx, azurepkg.AuthOptions{
			Tenant:       azureTenantID,
			UseCLI:       azureUseCLI,
			ClientID:     azureClientID,
			ClientSecret: azureClientSecret,
		})
		if err != nil {
			return err
		}
		subs, err := azurepkg.ListSubscriptions(ctx, cred)
		if err != nil {
			return err
		}
		for _, s := range subs {
			fmt.Printf("%s\t%s\n", s.ID, s.Name)
		}
		return nil
	},
}

func init() {
	azureCmd.AddCommand(azureSubscriptionsCmd)
}
