package azure

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

type AuthOptions struct {
	Tenant       string
	UseCLI       bool
	ClientID     string
	ClientSecret string
}

// Credential picks a TokenCredential based on the supplied options. Default is
// the Entra ID device-code flow because it has no infra requirements and works
// over SSH. --cli reuses 'az login' cached creds, and --client-id/--client-secret
// uses a service principal.
func Credential(ctx context.Context, opts AuthOptions) (azcore.TokenCredential, error) {
	switch {
	case opts.UseCLI:
		c, err := azidentity.NewAzureCLICredential(nil)
		if err != nil {
			return nil, fmt.Errorf("AzureCLICredential (is `az login` set up?): %w", err)
		}
		return c, nil

	case opts.ClientID != "" && opts.ClientSecret != "":
		if opts.Tenant == "" {
			return nil, errors.New("--tenant is required when using --client-id/--client-secret")
		}
		c, err := azidentity.NewClientSecretCredential(opts.Tenant, opts.ClientID, opts.ClientSecret, nil)
		if err != nil {
			return nil, fmt.Errorf("ClientSecretCredential: %w", err)
		}
		return c, nil

	default:
		dco := &azidentity.DeviceCodeCredentialOptions{
			TenantID: opts.Tenant,
			UserPrompt: func(_ context.Context, m azidentity.DeviceCodeMessage) error {
				fmt.Fprintf(os.Stderr, "\n%s\n\n", m.Message)
				return nil
			},
		}
		c, err := azidentity.NewDeviceCodeCredential(dco)
		if err != nil {
			return nil, fmt.Errorf("DeviceCodeCredential: %w", err)
		}
		return c, nil
	}
}
