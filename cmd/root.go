package cmd

import (
	"context"

	"github.com/spf13/cobra"
)

var dryRun bool

var rootCmd = &cobra.Command{
	Use:           "quota-locker",
	Short:         "Reduce all configurable cloud quotas to zero",
	Long:          "quota-locker drives GCP, AWS, and Azure APIs to lock down quotas (or the closest equivalent) across one or many accounts the caller can access.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "log planned changes without calling the API")
}

func Execute(ctx context.Context) error {
	return rootCmd.ExecuteContext(ctx)
}
