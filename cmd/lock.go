package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"kubeops.dev/quota-locker/pkg/auth"
	"kubeops.dev/quota-locker/pkg/locker"
)

var (
	gcpLockProjects    []string
	gcpLockAllProjects bool
	gcpLockYes         bool
	gcpLockConcurrency int
)

var gcpLockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Set every eligible GCP quota to 0 for one or more projects",
	Long: `lock walks every enabled service in each target project and creates a
QuotaPreference of 0 for every QuotaInfo it discovers. Both quota-decrease
safety checks (below-usage and percentage-too-high) are explicitly waived.

This is destructive. Use --dry-run first.`,
	RunE: runGCPLock,
}

func init() {
	f := gcpLockCmd.Flags()
	f.StringSliceVar(&gcpLockProjects, "project", nil, "project ID to lock (repeatable)")
	f.BoolVar(&gcpLockAllProjects, "all", false, "lock every project the caller can access")
	f.BoolVar(&gcpLockYes, "yes", false, "skip the interactive confirmation prompt")
	f.IntVar(&gcpLockConcurrency, "concurrency", 4, "parallel QuotaPreference writes per project")
	gcpCmd.AddCommand(gcpLockCmd)
}

func runGCPLock(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	if !gcpLockAllProjects && len(gcpLockProjects) == 0 {
		return fmt.Errorf("provide --project at least once, or pass --all")
	}

	ts, err := auth.TokenSource(ctx, auth.Options{
		Mode:         gcpAuthMode,
		ClientID:     gcpClientID,
		ClientSecret: gcpClientSecret,
	})
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	targets := gcpLockProjects
	if gcpLockAllProjects {
		fmt.Fprintln(os.Stderr, "discovering accessible projects...")
		discovered, err := locker.ListAccessibleProjects(ctx, ts)
		if err != nil {
			return fmt.Errorf("list projects: %w", err)
		}
		targets = discovered
		fmt.Fprintf(os.Stderr, "found %d project(s)\n", len(targets))
	}
	if len(targets) == 0 {
		return fmt.Errorf("no projects to lock")
	}

	if !gcpLockYes && !dryRun {
		if !gcpConfirm(targets) {
			return fmt.Errorf("aborted")
		}
	}

	l := &locker.Locker{
		TokenSource: ts,
		DryRun:      dryRun,
		Concurrency: gcpLockConcurrency,
	}
	return l.LockProjects(ctx, targets)
}

func gcpConfirm(projects []string) bool {
	fmt.Fprintf(os.Stderr, "\nAbout to set EVERY eligible quota to 0 across %d GCP project(s):\n", len(projects))
	for _, p := range projects {
		fmt.Fprintf(os.Stderr, "  - %s\n", p)
	}
	fmt.Fprint(os.Stderr, "\nType 'lock' to proceed: ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line) == "lock"
}
