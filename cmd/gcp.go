package cmd

import "github.com/spf13/cobra"

var (
	gcpAuthMode     string
	gcpClientID     string
	gcpClientSecret string
)

var gcpCmd = &cobra.Command{
	Use:   "gcp",
	Short: "GCP — discover and lock projects via Cloud Quotas API",
}

func init() {
	pf := gcpCmd.PersistentFlags()
	pf.StringVar(&gcpAuthMode, "auth", "loopback", "auth mode: loopback | adc")
	pf.StringVar(&gcpClientID, "client-id", "", "OAuth client ID (required for loopback)")
	pf.StringVar(&gcpClientSecret, "client-secret", "", "OAuth client secret (required for loopback)")
	rootCmd.AddCommand(gcpCmd)
}
