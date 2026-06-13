package cmd

import "github.com/spf13/cobra"

var (
	awsProfile     string
	awsRegion      string
	awsSSOStartURL string
	awsSSORegion   string
)

var awsCmd = &cobra.Command{
	Use:   "aws",
	Short: "AWS — lock accounts via deny-all SCP or IAM policy",
	Long: `AWS Service Quotas API does NOT support decreasing quotas, so quota-locker
locks an AWS account by applying a deny-all policy instead. Two methods are
supported:

  --method=scp       (default) attach a deny-all SCP to the target account
                     via AWS Organizations. The caller must run in (or have
                     trust into) the management account, and SCPs must be
                     enabled on the org.

  --method=iam-deny  attach a deny-all permission policy to every IAM user
                     and role in the account. Highly destructive — will lock
                     out everyone, including the caller, unless --exclude-arn
                     patterns are supplied.

Device auth uses AWS IAM Identity Center: pass --sso-start-url plus the
SSO account/role you want quota-locker to assume.`,
}

func init() {
	pf := awsCmd.PersistentFlags()
	pf.StringVar(&awsProfile, "profile", "", "AWS shared-config profile to use (overrides AWS_PROFILE)")
	pf.StringVar(&awsRegion, "region", "us-east-1", "AWS region for Organizations/IAM API calls")
	pf.StringVar(&awsSSOStartURL, "sso-start-url", "", "IAM Identity Center start URL — triggers device-code flow")
	pf.StringVar(&awsSSORegion, "sso-region", "us-east-1", "IAM Identity Center region (used with --sso-start-url)")
	rootCmd.AddCommand(awsCmd)
}
