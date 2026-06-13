package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

const (
	denyAllPolicyName        = "QuotaLockerDenyAll"
	denyAllPolicyDescription = "quota-locker: defensive lockdown — deny all actions"
	denyAllPolicyDocument    = `{"Version":"2012-10-17","Statement":[{"Sid":"DenyEverything","Effect":"Deny","Action":"*","Resource":"*"}]}`
)

type Locker struct {
	Config      aws.Config
	Method      string // "scp" | "iam-deny"
	DryRun      bool
	ExcludeARNs []string
	// AssumeRoleName is the IAM role assumed in each target account for
	// --method=iam-deny when the target is not the calling account. Empty
	// disables cross-account fanout. Conventional default:
	// "OrganizationAccountAccessRole".
	AssumeRoleName    string
	AssumeRoleExtID   string // optional external ID
	AssumeRoleSession string // session name; default "quota-locker"
}

func (l *Locker) LockAccounts(ctx context.Context, accounts []string) error {
	switch l.Method {
	case "scp":
		return l.lockViaSCP(ctx, accounts)
	case "iam-deny":
		return l.lockViaIAMDeny(ctx, accounts)
	default:
		return fmt.Errorf("unknown method %q", l.Method)
	}
}

func (l *Locker) lockViaSCP(ctx context.Context, accounts []string) error {
	orgs := organizations.NewFromConfig(l.Config)

	policyID, err := l.ensureSCP(ctx, orgs)
	if err != nil {
		return err
	}

	for _, acc := range accounts {
		fmt.Fprintf(os.Stderr, "\n=== %s ===\n", acc)
		if l.DryRun {
			fmt.Fprintf(os.Stderr, "  ~ would attach SCP %s to %s\n", policyID, acc)
			continue
		}
		_, err := orgs.AttachPolicy(ctx, &organizations.AttachPolicyInput{
			PolicyId: awsString(policyID),
			TargetId: awsString(acc),
		})
		if err != nil {
			if isAlreadyAttached(err) {
				fmt.Fprintf(os.Stderr, "  = already attached\n")
				continue
			}
			fmt.Fprintf(os.Stderr, "  ! attach: %v\n", err)
			continue
		}
		fmt.Fprintf(os.Stderr, "  + attached SCP %s\n", policyID)
	}
	return nil
}

func (l *Locker) ensureSCP(ctx context.Context, orgs *organizations.Client) (string, error) {
	// CreatePolicy is idempotent only on policy ID. We search by name to
	// avoid creating duplicates across runs.
	if existing, err := findSCPByName(ctx, orgs, denyAllPolicyName); err == nil && existing != "" {
		fmt.Fprintf(os.Stderr, "reusing existing SCP %s (id=%s)\n", denyAllPolicyName, existing)
		return existing, nil
	}
	if l.DryRun {
		fmt.Fprintf(os.Stderr, "~ would create SCP %s\n", denyAllPolicyName)
		return "(dry-run-policy-id)", nil
	}
	out, err := orgs.CreatePolicy(ctx, &organizations.CreatePolicyInput{
		Name:        awsString(denyAllPolicyName),
		Description: awsString(denyAllPolicyDescription),
		Content:     awsString(denyAllPolicyDocument),
		Type:        orgtypes.PolicyTypeServiceControlPolicy,
	})
	if err != nil {
		return "", fmt.Errorf("organizations.CreatePolicy: %w (is the management account, and are SCPs enabled?)", err)
	}
	id := awsStr(out.Policy.PolicySummary.Id)
	fmt.Fprintf(os.Stderr, "created SCP %s (id=%s)\n", denyAllPolicyName, id)
	return id, nil
}

func findSCPByName(ctx context.Context, orgs *organizations.Client, name string) (string, error) {
	pg := organizations.NewListPoliciesPaginator(orgs, &organizations.ListPoliciesInput{
		Filter: orgtypes.PolicyTypeServiceControlPolicy,
	})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return "", err
		}
		for _, p := range page.Policies {
			if awsStr(p.Name) == name {
				return awsStr(p.Id), nil
			}
		}
	}
	return "", nil
}

func isAlreadyAttached(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode() == "DuplicatePolicyAttachmentException"
	}
	return false
}

// lockViaIAMDeny iterates each target account, assuming AssumeRoleName in any
// account other than the caller's, and attaches a deny-all managed policy to
// every IAM user and role in that account.
func (l *Locker) lockViaIAMDeny(ctx context.Context, targets []string) error {
	callerOut, err := sts.NewFromConfig(l.Config).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return fmt.Errorf("sts.GetCallerIdentity: %w", err)
	}
	callerAccount := awsStr(callerOut.Account)
	callerARN := awsStr(callerOut.Arn)

	for _, target := range targets {
		fmt.Fprintf(os.Stderr, "\n=== %s ===\n", target)
		cfg := l.Config
		if target != callerAccount {
			if l.AssumeRoleName == "" {
				fmt.Fprintf(os.Stderr, "  ! cross-account requires --assume-role-name (caller=%s)\n", callerAccount)
				continue
			}
			assumed, err := l.assumeInto(ctx, target)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! assume role into %s: %v\n", target, err)
				continue
			}
			cfg = assumed
		}
		if err := l.lockAccountIAM(ctx, cfg, target, callerARN, target == callerAccount); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %v\n", err)
		}
	}
	return nil
}

// assumeInto returns an aws.Config whose credentials come from assuming
// arn:aws:iam::<account>:role/<AssumeRoleName>.
func (l *Locker) assumeInto(ctx context.Context, accountID string) (aws.Config, error) {
	roleARN := fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, l.AssumeRoleName)
	sess := l.AssumeRoleSession
	if sess == "" {
		sess = "quota-locker"
	}
	if l.DryRun {
		fmt.Fprintf(os.Stderr, "  ~ would assume %s\n", roleARN)
		// Returning the caller's own config is fine — dry-run won't make
		// mutating calls, only listings, and we'd rather surface listing
		// errors with real creds than fail silently.
		return l.Config, nil
	}

	stsClient := sts.NewFromConfig(l.Config)
	input := &sts.AssumeRoleInput{
		RoleArn:         awsString(roleARN),
		RoleSessionName: awsString(sess),
	}
	if l.AssumeRoleExtID != "" {
		input.ExternalId = awsString(l.AssumeRoleExtID)
	}
	out, err := stsClient.AssumeRole(ctx, input)
	if err != nil {
		return aws.Config{}, fmt.Errorf("sts.AssumeRole %s: %w", roleARN, err)
	}
	fmt.Fprintf(os.Stderr, "  assumed %s\n", roleARN)

	cfg := l.Config
	cfg.Credentials = credentials.NewStaticCredentialsProvider(
		awsStr(out.Credentials.AccessKeyId),
		awsStr(out.Credentials.SecretAccessKey),
		awsStr(out.Credentials.SessionToken),
	)
	return cfg, nil
}

// lockAccountIAM creates the deny policy if missing, then attaches it to every
// user/role in the supplied account (whose credentials are encoded in cfg).
// callerARN is always excluded when isCallerAccount is true so the operator
// running the tool keeps their access.
func (l *Locker) lockAccountIAM(ctx context.Context, cfg aws.Config, accountID, callerARN string, isCallerAccount bool) error {
	im := iam.NewFromConfig(cfg)
	policyARN, err := l.ensureIAMDenyPolicy(ctx, cfg, accountID)
	if err != nil {
		return err
	}

	users, err := listUsers(ctx, im)
	if err != nil {
		return fmt.Errorf("iam.ListUsers: %w", err)
	}
	roles, err := listRoles(ctx, im)
	if err != nil {
		return fmt.Errorf("iam.ListRoles: %w", err)
	}

	excludes := append([]string{}, l.ExcludeARNs...)
	if isCallerAccount {
		excludes = append(excludes, callerARN)
		fmt.Fprintf(os.Stderr, "  excluding caller %s\n", callerARN)
	}

	for _, u := range users {
		arn := awsStr(u.Arn)
		if matchesAny(arn, excludes) {
			fmt.Fprintf(os.Stderr, "  - skip user %s (excluded)\n", awsStr(u.UserName))
			continue
		}
		if l.DryRun {
			fmt.Fprintf(os.Stderr, "  ~ would attach %s to user %s\n", denyAllPolicyName, awsStr(u.UserName))
			continue
		}
		if _, err := im.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{
			UserName:  u.UserName,
			PolicyArn: awsString(policyARN),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  ! attach user %s: %v\n", awsStr(u.UserName), err)
			continue
		}
		fmt.Fprintf(os.Stderr, "  + attached to user %s\n", awsStr(u.UserName))
	}
	for _, r := range roles {
		arn := awsStr(r.Arn)
		if matchesAny(arn, excludes) {
			fmt.Fprintf(os.Stderr, "  - skip role %s (excluded)\n", awsStr(r.RoleName))
			continue
		}
		if strings.HasPrefix(awsStr(r.Path), "/aws-service-role/") {
			fmt.Fprintf(os.Stderr, "  - skip role %s (service-linked)\n", awsStr(r.RoleName))
			continue
		}
		if l.DryRun {
			fmt.Fprintf(os.Stderr, "  ~ would attach %s to role %s\n", denyAllPolicyName, awsStr(r.RoleName))
			continue
		}
		if _, err := im.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
			RoleName:  r.RoleName,
			PolicyArn: awsString(policyARN),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "  ! attach role %s: %v\n", awsStr(r.RoleName), err)
			continue
		}
		fmt.Fprintf(os.Stderr, "  + attached to role %s\n", awsStr(r.RoleName))
	}
	return nil
}

func (l *Locker) ensureIAMDenyPolicy(ctx context.Context, cfg aws.Config, accountID string) (string, error) {
	if l.DryRun {
		return fmt.Sprintf("arn:aws:iam::%s:policy/%s", accountID, denyAllPolicyName), nil
	}
	im := iam.NewFromConfig(cfg)
	out, err := im.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     awsString(denyAllPolicyName),
		Description:    awsString(denyAllPolicyDescription),
		PolicyDocument: awsString(denyAllPolicyDocument),
	})
	if err == nil {
		return awsStr(out.Policy.Arn), nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "EntityAlreadyExists" {
		return fmt.Sprintf("arn:aws:iam::%s:policy/%s", accountID, denyAllPolicyName), nil
	}
	return "", fmt.Errorf("iam.CreatePolicy: %w", err)
}

func listUsers(ctx context.Context, im *iam.Client) ([]iamtypes.User, error) {
	var out []iamtypes.User
	pg := iam.NewListUsersPaginator(im, &iam.ListUsersInput{})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Users...)
	}
	return out, nil
}

func listRoles(ctx context.Context, im *iam.Client) ([]iamtypes.Role, error) {
	var out []iamtypes.Role
	pg := iam.NewListRolesPaginator(im, &iam.ListRolesInput{})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.Roles...)
	}
	return out, nil
}

func matchesAny(arn string, patterns []string) bool {
	for _, p := range patterns {
		if p != "" && strings.Contains(arn, p) {
			return true
		}
	}
	return false
}
