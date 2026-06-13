package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	awspkg "kubeops.dev/quota-locker/pkg/aws"
)

var (
	awsLockAccounts   []string
	awsLockAll        bool
	awsLockMethod     string
	awsLockYes        bool
	awsLockExclude    []string
	awsLockRoleName   string
	awsLockRoleExtID  string
	awsLockRoleSess   string
)

var awsLockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Lock AWS account(s) with a deny-all SCP or IAM policy",
	RunE:  runAWSLock,
}

func init() {
	f := awsLockCmd.Flags()
	f.StringSliceVar(&awsLockAccounts, "account", nil, "AWS account ID to lock (repeatable)")
	f.BoolVar(&awsLockAll, "all", false, "lock every account discoverable via AWS Organizations")
	f.StringVar(&awsLockMethod, "method", "scp", "lock method: scp | iam-deny")
	f.BoolVar(&awsLockYes, "yes", false, "skip the interactive confirmation prompt")
	f.StringSliceVar(&awsLockExclude, "exclude-arn", nil, "IAM ARN substring to exclude from --method=iam-deny (repeatable; e.g. role/Admin)")
	f.StringVar(&awsLockRoleName, "assume-role-name", "OrganizationAccountAccessRole", "IAM role name assumed in each non-caller account for --method=iam-deny (empty disables cross-account)")
	f.StringVar(&awsLockRoleExtID, "assume-role-external-id", "", "ExternalId to pass to sts:AssumeRole when fanning out")
	f.StringVar(&awsLockRoleSess, "assume-role-session", "quota-locker", "session name used when assuming cross-account roles")
	awsCmd.AddCommand(awsLockCmd)
}

func runAWSLock(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	if !awsLockAll && len(awsLockAccounts) == 0 {
		return fmt.Errorf("provide --account at least once, or pass --all")
	}
	switch awsLockMethod {
	case "scp", "iam-deny":
	default:
		return fmt.Errorf("--method must be scp or iam-deny (got %q)", awsLockMethod)
	}

	cfg, err := awspkg.LoadConfig(ctx, awspkg.AuthOptions{
		Profile:     awsProfile,
		Region:      awsRegion,
		SSOStartURL: awsSSOStartURL,
		SSORegion:   awsSSORegion,
	})
	if err != nil {
		return err
	}

	targets := awsLockAccounts
	if awsLockAll {
		fmt.Fprintln(os.Stderr, "discovering accounts via AWS Organizations...")
		accs, err := awspkg.ListAccounts(ctx, cfg)
		if err != nil {
			return fmt.Errorf("list accounts: %w", err)
		}
		for _, a := range accs {
			targets = append(targets, a.ID)
		}
		fmt.Fprintf(os.Stderr, "found %d account(s)\n", len(targets))
	}
	if len(targets) == 0 {
		return fmt.Errorf("no accounts to lock")
	}

	if !awsLockYes && !dryRun {
		if !awsConfirm(targets, awsLockMethod) {
			return fmt.Errorf("aborted")
		}
	}

	l := &awspkg.Locker{
		Config:            cfg,
		Method:            awsLockMethod,
		DryRun:            dryRun,
		ExcludeARNs:       awsLockExclude,
		AssumeRoleName:    awsLockRoleName,
		AssumeRoleExtID:   awsLockRoleExtID,
		AssumeRoleSession: awsLockRoleSess,
	}
	return l.LockAccounts(ctx, targets)
}

func awsConfirm(accounts []string, method string) bool {
	fmt.Fprintf(os.Stderr, "\nAbout to apply method=%s deny-all across %d AWS account(s):\n", method, len(accounts))
	for _, a := range accounts {
		fmt.Fprintf(os.Stderr, "  - %s\n", a)
	}
	if method == "iam-deny" {
		fmt.Fprintln(os.Stderr, "\nWARNING: --method=iam-deny attaches a Deny-* policy to EVERY IAM user/role.")
		fmt.Fprintln(os.Stderr, "         You will lock yourself out unless --exclude-arn matches your principal.")
	}
	fmt.Fprint(os.Stderr, "\nType 'lock' to proceed: ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line) == "lock"
}
