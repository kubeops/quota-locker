package locker

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	cloudquotas "cloud.google.com/go/cloudquotas/apiv1"
	"cloud.google.com/go/cloudquotas/apiv1/cloudquotaspb"
	"golang.org/x/oauth2"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	serviceusage "google.golang.org/api/serviceusage/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Locker drives the Cloud Quotas API to set every eligible quota to 0
// for one or more projects.
type Locker struct {
	TokenSource oauth2.TokenSource
	DryRun      bool
	Concurrency int
}

// LockProjects iterates through each project, listing its enabled services and
// zeroing every QuotaInfo that isn't fixed. Per-project failures are logged but
// do not abort the run.
func (l *Locker) LockProjects(ctx context.Context, projects []string) error {
	for _, p := range projects {
		fmt.Fprintf(os.Stderr, "\n=== %s ===\n", p)
		if err := l.lockProject(ctx, p); err != nil {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", p, err)
		}
	}
	return nil
}

func (l *Locker) lockProject(ctx context.Context, projectID string) error {
	services, err := l.enabledServices(ctx, projectID)
	if err != nil {
		return fmt.Errorf("list enabled services: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  %d enabled service(s)\n", len(services))

	qc, err := cloudquotas.NewClient(ctx, option.WithTokenSource(l.TokenSource))
	if err != nil {
		return fmt.Errorf("create cloudquotas client: %w", err)
	}
	defer qc.Close()

	conc := l.Concurrency
	if conc <= 0 {
		conc = 4
	}
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup

	var totals struct {
		sync.Mutex
		seen, zeroed, skipped, failed int
	}

	for _, svc := range services {
		parent := fmt.Sprintf("projects/%s/locations/global/services/%s", projectID, svc)
		it := qc.ListQuotaInfos(ctx, &cloudquotaspb.ListQuotaInfosRequest{Parent: parent})
		for {
			qi, err := it.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				// Many services have not yet onboarded to the Cloud Quotas API.
				// Treat NOT_FOUND / UNIMPLEMENTED / PERMISSION_DENIED as "no quotas
				// to manage here" and keep walking.
				c := status.Code(err)
				if c == codes.NotFound || c == codes.Unimplemented || c == codes.PermissionDenied {
					break
				}
				fmt.Fprintf(os.Stderr, "  ! list quotas for %s: %v\n", svc, err)
				break
			}

			totals.Lock()
			totals.seen++
			totals.Unlock()

			if qi.GetIsFixed() {
				fmt.Fprintf(os.Stderr, "  - skip %s (fixed)\n", qi.GetQuotaId())
				totals.Lock()
				totals.skipped++
				totals.Unlock()
				continue
			}

			info := qi
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				if err := l.zeroQuota(ctx, qc, projectID, info); err != nil {
					fmt.Fprintf(os.Stderr, "  ! %s: %v\n", info.GetQuotaId(), err)
					totals.Lock()
					totals.failed++
					totals.Unlock()
					return
				}
				totals.Lock()
				totals.zeroed++
				totals.Unlock()
			}()
		}
	}
	wg.Wait()

	mode := "applied"
	if l.DryRun {
		mode = "would apply"
	}
	fmt.Fprintf(os.Stderr, "  summary: seen=%d %s=%d skipped=%d failed=%d\n",
		totals.seen, mode, totals.zeroed, totals.skipped, totals.failed)
	return nil
}

func (l *Locker) zeroQuota(ctx context.Context, qc *cloudquotas.Client, projectID string, qi *cloudquotaspb.QuotaInfo) error {
	parent := fmt.Sprintf("projects/%s/locations/global", projectID)
	pref := &cloudquotaspb.QuotaPreference{
		Service: qi.GetService(),
		QuotaId: qi.GetQuotaId(),
		QuotaConfig: &cloudquotaspb.QuotaConfig{
			PreferredValue: 0,
		},
		Justification: "quota-locker: defensive lockdown",
	}
	req := &cloudquotaspb.CreateQuotaPreferenceRequest{
		Parent:          parent,
		QuotaPreference: pref,
		IgnoreSafetyChecks: []cloudquotaspb.QuotaSafetyCheck{
			cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_BELOW_USAGE,
			cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_PERCENTAGE_TOO_HIGH,
		},
	}

	if l.DryRun {
		fmt.Fprintf(os.Stderr, "  ~ would zero %s/%s\n", shortService(qi.GetService()), qi.GetQuotaId())
		return nil
	}

	if _, err := qc.CreateQuotaPreference(ctx, req); err != nil {
		// AlreadyExists → fall back to UpdateQuotaPreference so reruns are idempotent.
		if status.Code(err) == codes.AlreadyExists {
			pref.Name = fmt.Sprintf("%s/quotaPreferences/%s", parent, qi.GetQuotaId())
			if _, uerr := qc.UpdateQuotaPreference(ctx, &cloudquotaspb.UpdateQuotaPreferenceRequest{
				QuotaPreference: pref,
				IgnoreSafetyChecks: []cloudquotaspb.QuotaSafetyCheck{
					cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_BELOW_USAGE,
					cloudquotaspb.QuotaSafetyCheck_QUOTA_DECREASE_PERCENTAGE_TOO_HIGH,
				},
				AllowMissing: true,
			}); uerr != nil {
				return fmt.Errorf("update: %w", uerr)
			}
			fmt.Fprintf(os.Stderr, "  = updated %s/%s\n", shortService(qi.GetService()), qi.GetQuotaId())
			return nil
		}
		return fmt.Errorf("create: %w", err)
	}
	fmt.Fprintf(os.Stderr, "  + zeroed %s/%s\n", shortService(qi.GetService()), qi.GetQuotaId())
	return nil
}

// enabledServices returns the service short-names (e.g., "compute.googleapis.com")
// that are currently enabled on the project.
func (l *Locker) enabledServices(ctx context.Context, projectID string) ([]string, error) {
	svc, err := serviceusage.NewService(ctx, option.WithTokenSource(l.TokenSource))
	if err != nil {
		return nil, err
	}

	var out []string
	parent := fmt.Sprintf("projects/%s", projectID)
	call := svc.Services.List(parent).Filter("state:ENABLED").PageSize(200)
	if err := call.Pages(ctx, func(page *serviceusage.ListServicesResponse) error {
		for _, s := range page.Services {
			// s.Name looks like "projects/123/services/compute.googleapis.com".
			name := s.Name
			if i := strings.LastIndex(name, "/services/"); i >= 0 {
				name = name[i+len("/services/"):]
			}
			if name != "" {
				out = append(out, name)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return out, nil
}

func shortService(s string) string {
	if i := strings.Index(s, "."); i > 0 {
		return s[:i]
	}
	return s
}
