package azure

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
)

type Subscription struct {
	ID   string
	Name string
}

// ListSubscriptions returns every enabled subscription the credential can see.
func ListSubscriptions(ctx context.Context, cred azcore.TokenCredential) ([]Subscription, error) {
	client, err := armsubscription.NewSubscriptionsClient(cred, nil)
	if err != nil {
		return nil, fmt.Errorf("create subscriptions client: %w", err)
	}

	var out []Subscription
	pager := client.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list subscriptions: %w", err)
		}
		for _, s := range page.Value {
			if s == nil {
				continue
			}
			if s.State != nil && *s.State != armsubscription.SubscriptionStateEnabled {
				continue
			}
			id, name := "", ""
			if s.SubscriptionID != nil {
				id = *s.SubscriptionID
			}
			if s.DisplayName != nil {
				name = *s.DisplayName
			}
			if id != "" {
				out = append(out, Subscription{ID: id, Name: name})
			}
		}
	}
	return out, nil
}
