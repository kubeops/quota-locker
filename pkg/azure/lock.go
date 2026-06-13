package azure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armlocks"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armpolicy"
)

const (
	denyAllPolicyName        = "quota-locker-deny-all"
	denyAllPolicyDisplayName = "quota-locker defensive lockdown"
	denyAllPolicyDescription = "quota-locker: deny all resource create/update operations."
	subLockName              = "quota-locker-sub-lock"
)

// SubscriptionLockLevel selects an optional management lock applied at
// subscription scope alongside the deny-all Policy assignment.
//   - "" / "none"      → no management lock
//   - "readonly"       → blocks deletes AND modifications (very heavy)
//   - "cannotdelete"   → blocks deletes only
type SubscriptionLockLevel string

const (
	LockNone         SubscriptionLockLevel = "none"
	LockReadOnly     SubscriptionLockLevel = "readonly"
	LockCanNotDelete SubscriptionLockLevel = "cannotdelete"
)

func ParseLockLevel(s string) (SubscriptionLockLevel, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return LockNone, nil
	case "readonly":
		return LockReadOnly, nil
	case "cannotdelete":
		return LockCanNotDelete, nil
	default:
		return "", fmt.Errorf("invalid --subscription-lock %q (want none | readonly | cannotdelete)", s)
	}
}

type Locker struct {
	Credential       azcore.TokenCredential
	DryRun           bool
	SubscriptionLock SubscriptionLockLevel
}

// LockSubscriptions creates a custom 'deny *' policy definition under each
// subscription and assigns it to that subscription. Existing resources keep
// running; new creates/updates are denied.
func (l *Locker) LockSubscriptions(ctx context.Context, subs []string) error {
	for _, sub := range subs {
		fmt.Fprintf(os.Stderr, "\n=== %s ===\n", sub)
		if err := l.lockOne(ctx, sub); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %v\n", err)
		}
	}
	return nil
}

func (l *Locker) lockOne(ctx context.Context, subscriptionID string) error {
	if subscriptionID == "" {
		return errors.New("empty subscription ID")
	}
	scope := fmt.Sprintf("/subscriptions/%s", subscriptionID)
	defID := fmt.Sprintf("%s/providers/Microsoft.Authorization/policyDefinitions/%s", scope, denyAllPolicyName)

	if l.DryRun {
		fmt.Fprintf(os.Stderr, "  ~ would create policy definition %s\n", defID)
		fmt.Fprintf(os.Stderr, "  ~ would create assignment %s on %s\n", denyAllPolicyName, scope)
		return nil
	}

	defClient, err := armpolicy.NewDefinitionsClient(subscriptionID, l.Credential, nil)
	if err != nil {
		return fmt.Errorf("definitions client: %w", err)
	}

	rule := map[string]any{
		"if": map[string]any{
			"field":  "type",
			"exists": "true",
		},
		"then": map[string]any{
			"effect": "deny",
		},
	}
	defParams := armpolicy.Definition{
		Properties: &armpolicy.DefinitionProperties{
			DisplayName: to.Ptr(denyAllPolicyDisplayName),
			Description: to.Ptr(denyAllPolicyDescription),
			Mode:        to.Ptr("All"),
			PolicyType:  to.Ptr(armpolicy.PolicyTypeCustom),
			PolicyRule:  rule,
		},
	}
	if _, err := defClient.CreateOrUpdate(ctx, denyAllPolicyName, defParams, nil); err != nil {
		return fmt.Errorf("create policy definition: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  + policy definition %s ready\n", denyAllPolicyName)

	asgClient, err := armpolicy.NewAssignmentsClient(subscriptionID, l.Credential, nil)
	if err != nil {
		return fmt.Errorf("assignments client: %w", err)
	}
	asgParams := armpolicy.Assignment{
		Properties: &armpolicy.AssignmentProperties{
			DisplayName:        to.Ptr(denyAllPolicyDisplayName),
			Description:        to.Ptr(denyAllPolicyDescription),
			PolicyDefinitionID: to.Ptr(defID),
			EnforcementMode:    to.Ptr(armpolicy.EnforcementModeDefault),
		},
	}
	if _, err := asgClient.Create(ctx, scope, denyAllPolicyName, asgParams, nil); err != nil {
		return fmt.Errorf("create assignment: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  + assignment %s applied to %s\n", denyAllPolicyName, scope)

	if err := l.applySubscriptionLock(ctx, subscriptionID); err != nil {
		return fmt.Errorf("apply subscription lock: %w", err)
	}
	return nil
}

func (l *Locker) applySubscriptionLock(ctx context.Context, subscriptionID string) error {
	level := l.SubscriptionLock
	if level == "" || level == LockNone {
		return nil
	}

	var azLevel armlocks.LockLevel
	switch level {
	case LockReadOnly:
		azLevel = armlocks.LockLevelReadOnly
	case LockCanNotDelete:
		azLevel = armlocks.LockLevelCanNotDelete
	default:
		return fmt.Errorf("unsupported subscription lock level %q", level)
	}

	if l.DryRun {
		fmt.Fprintf(os.Stderr, "  ~ would apply %s management lock %s to subscription\n", azLevel, subLockName)
		return nil
	}

	c, err := armlocks.NewManagementLocksClient(subscriptionID, l.Credential, nil)
	if err != nil {
		return fmt.Errorf("locks client: %w", err)
	}
	params := armlocks.ManagementLockObject{
		Properties: &armlocks.ManagementLockProperties{
			Level: to.Ptr(azLevel),
			Notes: to.Ptr("quota-locker: defensive lockdown"),
		},
	}
	if _, err := c.CreateOrUpdateAtSubscriptionLevel(ctx, subLockName, params, nil); err != nil {
		return fmt.Errorf("create subscription lock: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  + applied %s management lock %s\n", azLevel, subLockName)
	return nil
}
