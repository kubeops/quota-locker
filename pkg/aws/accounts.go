package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

type Account struct {
	ID   string
	Name string
}

// ListAccounts returns every ACTIVE account reachable by the caller. If the
// caller is in the Organizations management account, it walks the entire org;
// otherwise it falls back to the single calling account from STS.
func ListAccounts(ctx context.Context, cfg aws.Config) ([]Account, error) {
	orgs := organizations.NewFromConfig(cfg)
	var accounts []Account
	pg := organizations.NewListAccountsPaginator(orgs, &organizations.ListAccountsInput{})
	for pg.HasMorePages() {
		page, err := pg.NextPage(ctx)
		if err != nil {
			if isNotOrgMember(err) {
				return singleAccount(ctx, cfg)
			}
			return nil, fmt.Errorf("organizations.ListAccounts: %w", err)
		}
		for _, a := range page.Accounts {
			if a.Status != orgtypes.AccountStatusActive {
				continue
			}
			accounts = append(accounts, Account{
				ID:   awsStr(a.Id),
				Name: awsStr(a.Name),
			})
		}
	}
	if len(accounts) == 0 {
		return singleAccount(ctx, cfg)
	}
	return accounts, nil
}

func singleAccount(ctx context.Context, cfg aws.Config) ([]Account, error) {
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("sts.GetCallerIdentity: %w", err)
	}
	id := awsStr(out.Account)
	if id == "" {
		return nil, errors.New("STS returned an empty account ID")
	}
	return []Account{{ID: id, Name: "(current account)"}}, nil
}

func isNotOrgMember(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AWSOrganizationsNotInUseException",
			"AccessDeniedException",
			"AccessDeniedForDependencyException":
			return true
		}
	}
	return false
}
