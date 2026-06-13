package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sso"
	"github.com/aws/aws-sdk-go-v2/service/ssooidc"
)

type AuthOptions struct {
	Profile     string
	Region      string
	SSOStartURL string
	SSORegion   string
}

// LoadConfig returns an AWS SDK config. If SSOStartURL is set it triggers the
// IAM Identity Center device-code flow and uses the resulting short-lived
// credentials; otherwise it falls back to the default credential chain
// (env, ~/.aws, IMDS, container, ECS, etc.).
func LoadConfig(ctx context.Context, opts AuthOptions) (aws.Config, error) {
	if opts.SSOStartURL != "" {
		return loadSSOConfig(ctx, opts)
	}
	loaders := []func(*config.LoadOptions) error{
		config.WithRegion(opts.Region),
	}
	if opts.Profile != "" {
		loaders = append(loaders, config.WithSharedConfigProfile(opts.Profile))
	}
	cfg, err := config.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load default AWS config: %w", err)
	}
	return cfg, nil
}

// loadSSOConfig runs the OAuth 2.0 Device Authorization Grant against AWS IAM
// Identity Center, asks the user to pick an account/role on stderr, and returns
// an AWS config whose credentials use the resulting SSO session.
func loadSSOConfig(ctx context.Context, opts AuthOptions) (aws.Config, error) {
	base, err := config.LoadDefaultConfig(ctx, config.WithRegion(opts.SSORegion))
	if err != nil {
		return aws.Config{}, fmt.Errorf("base config for SSO: %w", err)
	}
	oidc := ssooidc.NewFromConfig(base)

	reg, err := oidc.RegisterClient(ctx, &ssooidc.RegisterClientInput{
		ClientName: awsString("quota-locker"),
		ClientType: awsString("public"),
	})
	if err != nil {
		return aws.Config{}, fmt.Errorf("ssooidc.RegisterClient: %w", err)
	}

	dev, err := oidc.StartDeviceAuthorization(ctx, &ssooidc.StartDeviceAuthorizationInput{
		ClientId:     reg.ClientId,
		ClientSecret: reg.ClientSecret,
		StartUrl:     awsString(opts.SSOStartURL),
	})
	if err != nil {
		return aws.Config{}, fmt.Errorf("ssooidc.StartDeviceAuthorization: %w", err)
	}

	url := awsStr(dev.VerificationUriComplete)
	if url == "" {
		url = awsStr(dev.VerificationUri)
	}
	fmt.Fprintf(os.Stderr, "\nVisit %s\nand enter the code: %s\n\nWaiting for authorization...\n",
		url, awsStr(dev.UserCode))

	interval := time.Duration(dev.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dev.ExpiresIn) * time.Second)

	var tok *ssooidc.CreateTokenOutput
	for {
		if time.Now().After(deadline) {
			return aws.Config{}, errors.New("device authorization expired")
		}
		tok, err = oidc.CreateToken(ctx, &ssooidc.CreateTokenInput{
			ClientId:     reg.ClientId,
			ClientSecret: reg.ClientSecret,
			GrantType:    awsString("urn:ietf:params:oauth:grant-type:device_code"),
			DeviceCode:   dev.DeviceCode,
		})
		if err == nil {
			break
		}
		// AuthorizationPendingException and SlowDownException are signal-only.
		// Keep polling. Anything else aborts.
		es := err.Error()
		if !contains(es, "AuthorizationPending") && !contains(es, "SlowDown") {
			return aws.Config{}, fmt.Errorf("ssooidc.CreateToken: %w", err)
		}
		select {
		case <-ctx.Done():
			return aws.Config{}, ctx.Err()
		case <-time.After(interval):
		}
	}

	ssoClient := sso.NewFromConfig(base)
	accountID, roleName, err := pickAccountRole(ctx, ssoClient, awsStr(tok.AccessToken))
	if err != nil {
		return aws.Config{}, err
	}
	fmt.Fprintf(os.Stderr, "using account=%s role=%s\n", accountID, roleName)

	roleCreds, err := ssoClient.GetRoleCredentials(ctx, &sso.GetRoleCredentialsInput{
		AccessToken: tok.AccessToken,
		AccountId:   awsString(accountID),
		RoleName:    awsString(roleName),
	})
	if err != nil {
		return aws.Config{}, fmt.Errorf("sso.GetRoleCredentials: %w", err)
	}

	region := opts.Region
	if region == "" {
		region = opts.SSORegion
	}
	return aws.Config{
		Region: region,
		Credentials: credentials.NewStaticCredentialsProvider(
			awsStr(roleCreds.RoleCredentials.AccessKeyId),
			awsStr(roleCreds.RoleCredentials.SecretAccessKey),
			awsStr(roleCreds.RoleCredentials.SessionToken),
		),
	}, nil
}

func pickAccountRole(ctx context.Context, c *sso.Client, accessToken string) (string, string, error) {
	accs, err := c.ListAccounts(ctx, &sso.ListAccountsInput{AccessToken: awsString(accessToken)})
	if err != nil {
		return "", "", fmt.Errorf("sso.ListAccounts: %w", err)
	}
	if len(accs.AccountList) == 0 {
		return "", "", errors.New("SSO returned no accessible accounts")
	}
	// If exactly one account+role is reachable, take it. Otherwise prompt.
	if len(accs.AccountList) == 1 {
		acc := accs.AccountList[0]
		roles, err := c.ListAccountRoles(ctx, &sso.ListAccountRolesInput{
			AccessToken: awsString(accessToken),
			AccountId:   acc.AccountId,
		})
		if err != nil {
			return "", "", fmt.Errorf("sso.ListAccountRoles: %w", err)
		}
		if len(roles.RoleList) == 1 {
			return awsStr(acc.AccountId), awsStr(roles.RoleList[0].RoleName), nil
		}
	}
	fmt.Fprintln(os.Stderr, "\nMultiple SSO accounts/roles available — pick one:")
	type choice struct{ accountID, roleName string }
	var choices []choice
	for _, acc := range accs.AccountList {
		roles, err := c.ListAccountRoles(ctx, &sso.ListAccountRolesInput{
			AccessToken: awsString(accessToken),
			AccountId:   acc.AccountId,
		})
		if err != nil {
			continue
		}
		for _, r := range roles.RoleList {
			n := len(choices) + 1
			fmt.Fprintf(os.Stderr, "  [%d] account=%s (%s) role=%s\n",
				n, awsStr(acc.AccountId), awsStr(acc.AccountName), awsStr(r.RoleName))
			choices = append(choices, choice{awsStr(acc.AccountId), awsStr(r.RoleName)})
		}
	}
	fmt.Fprint(os.Stderr, "select [1-N]: ")
	var idx int
	if _, err := fmt.Fscanln(os.Stdin, &idx); err != nil {
		return "", "", fmt.Errorf("read selection: %w", err)
	}
	if idx < 1 || idx > len(choices) {
		return "", "", fmt.Errorf("out of range")
	}
	return choices[idx-1].accountID, choices[idx-1].roleName, nil
}

func awsString(s string) *string { return &s }
func awsStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
