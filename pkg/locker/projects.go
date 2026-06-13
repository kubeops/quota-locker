package locker

import (
	"context"
	"fmt"
	"strings"

	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"cloud.google.com/go/resourcemanager/apiv3/resourcemanagerpb"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

// ListAccessibleProjects returns the IDs of every ACTIVE project the caller can
// see via Cloud Resource Manager search.
func ListAccessibleProjects(ctx context.Context, ts oauth2.TokenSource) ([]string, error) {
	client, err := resourcemanager.NewProjectsClient(ctx, option.WithTokenSource(ts))
	if err != nil {
		return nil, fmt.Errorf("create projects client: %w", err)
	}
	defer client.Close()

	var ids []string
	it := client.SearchProjects(ctx, &resourcemanagerpb.SearchProjectsRequest{
		Query: "state:ACTIVE",
	})
	for {
		p, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("search projects: %w", err)
		}
		id := p.ProjectId
		if id == "" {
			id = strings.TrimPrefix(p.Name, "projects/")
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
