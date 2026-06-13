package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	azurepkg "kubeops.dev/quota-locker/pkg/azure"
)

var (
	azureLockSubs []string
	azureLockAll  bool
	azureLockYes  bool
	azureLockSub  string
)

var azureLockCmd = &cobra.Command{
	Use:   "lock",
	Short: "Apply a deny-all Policy assignment to one or more Azure subscriptions",
	RunE:  runAzureLock,
}

func init() {
	f := azureLockCmd.Flags()
	f.StringSliceVar(&azureLockSubs, "subscription", nil, "subscription ID to lock (repeatable)")
	f.BoolVar(&azureLockAll, "all", false, "lock every subscription the caller can access")
	f.BoolVar(&azureLockYes, "yes", false, "skip the interactive confirmation prompt")
	f.StringVar(&azureLockSub, "subscription-lock", "none", "additionally apply a subscription-scope management lock: none | readonly | cannotdelete")
	azureCmd.AddCommand(azureLockCmd)
}

func runAzureLock(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	if !azureLockAll && len(azureLockSubs) == 0 {
		return fmt.Errorf("provide --subscription at least once, or pass --all")
	}

	lockLevel, err := azurepkg.ParseLockLevel(azureLockSub)
	if err != nil {
		return err
	}

	cred, err := azurepkg.Credential(ctx, azurepkg.AuthOptions{
		Tenant:       azureTenantID,
		UseCLI:       azureUseCLI,
		ClientID:     azureClientID,
		ClientSecret: azureClientSecret,
	})
	if err != nil {
		return err
	}

	targets := azureLockSubs
	if azureLockAll {
		fmt.Fprintln(os.Stderr, "discovering accessible subscriptions...")
		subs, err := azurepkg.ListSubscriptions(ctx, cred)
		if err != nil {
			return fmt.Errorf("list subscriptions: %w", err)
		}
		for _, s := range subs {
			targets = append(targets, s.ID)
		}
		fmt.Fprintf(os.Stderr, "found %d subscription(s)\n", len(targets))
	}
	if len(targets) == 0 {
		return fmt.Errorf("no subscriptions to lock")
	}

	if !azureLockYes && !dryRun {
		if !azureConfirm(targets) {
			return fmt.Errorf("aborted")
		}
	}

	l := &azurepkg.Locker{
		Credential:       cred,
		DryRun:           dryRun,
		SubscriptionLock: lockLevel,
	}
	return l.LockSubscriptions(ctx, targets)
}

func azureConfirm(subs []string) bool {
	fmt.Fprintf(os.Stderr, "\nAbout to apply a deny-all Policy assignment to %d Azure subscription(s):\n", len(subs))
	for _, s := range subs {
		fmt.Fprintf(os.Stderr, "  - %s\n", s)
	}
	fmt.Fprint(os.Stderr, "\nType 'lock' to proceed: ")
	r := bufio.NewReader(os.Stdin)
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line) == "lock"
}
