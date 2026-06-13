package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"kubeops.dev/quota-locker/pkg/auth"
	"kubeops.dev/quota-locker/pkg/locker"
)

var gcpProjectsCmd = &cobra.Command{
	Use:   "projects",
	Short: "List GCP projects accessible to the authenticated principal",
	RunE: func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		ts, err := auth.TokenSource(ctx, auth.Options{
			Mode:         gcpAuthMode,
			ClientID:     gcpClientID,
			ClientSecret: gcpClientSecret,
		})
		if err != nil {
			return fmt.Errorf("auth: %w", err)
		}
		ids, err := locker.ListAccessibleProjects(ctx, ts)
		if err != nil {
			return err
		}
		for _, id := range ids {
			fmt.Println(id)
		}
		return nil
	},
}

func init() {
	gcpCmd.AddCommand(gcpProjectsCmd)
}
