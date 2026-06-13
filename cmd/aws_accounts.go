package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	awspkg "kubeops.dev/quota-locker/pkg/aws"
)

var awsAccountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List AWS accounts the caller can see (Org member or single)",
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		cfg, err := awspkg.LoadConfig(ctx, awspkg.AuthOptions{
			Profile:     awsProfile,
			Region:      awsRegion,
			SSOStartURL: awsSSOStartURL,
			SSORegion:   awsSSORegion,
		})
		if err != nil {
			return err
		}
		accounts, err := awspkg.ListAccounts(ctx, cfg)
		if err != nil {
			return err
		}
		for _, a := range accounts {
			fmt.Printf("%s\t%s\n", a.ID, a.Name)
		}
		return nil
	},
}

func init() {
	awsCmd.AddCommand(awsAccountsCmd)
}
