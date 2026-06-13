# Development

What you need to know to make changes to quota-locker.

## Prerequisites

- Go 1.23+. The module currently builds on 1.23; CI will pin a minimum
  once it exists. The author dev-builds on Go 1.26 without issue.
- A cloud account (or three) for end-to-end testing. See [TEST.md](TEST.md).
- No protobuf or codegen step — all cloud SDKs are vendored as standard
  Go modules.

## Build, run, lint

```bash
go build ./...       # compile every package
go build .           # build the CLI binary into ./quota-locker
go vet ./...         # static checks
go test ./...        # no tests yet
go mod tidy          # after adding/removing imports
```

The binary is single-file: `go build -o quota-locker .` puts it wherever
you want. There is no `make`, no `scripts/`, no `go generate`.

## Repo layout

```
main.go                  signal handler + cmd.Execute
go.mod                   one module, kubeops.dev/quota-locker
cmd/
  root.go                rootCmd, --dry-run (global)
  gcp.go                 gcpCmd parent + auth flags
  lock.go                `gcp lock` subcommand
  projects.go            `gcp projects` subcommand
  aws.go                 awsCmd parent + auth/SSO flags
  aws_accounts.go        `aws accounts`
  aws_lock.go            `aws lock`
  azure.go               azureCmd parent + auth flags
  azure_subscriptions.go `azure subscriptions`
  azure_lock.go          `azure lock`
pkg/
  auth/auth.go           GCP OAuth loopback + ADC
  locker/                GCP — quota writer + project search
    projects.go
    locker.go
  aws/                   AWS provider
    auth.go              SDK config loader + IAM Identity Center device flow
    accounts.go          Organizations.ListAccounts → STS fallback
    lock.go              SCP method + IAM-deny method + AssumeRole fanout
  azure/                 Azure provider
    auth.go              DeviceCode | AzureCLI | ClientSecret
    subscriptions.go     armsubscription paging
    lock.go              Policy assignment + optional management lock
```

The split is "cobra in `cmd/`, logic in `pkg/`". `cmd/*.go` files are
thin: flag wiring, an interactive prompt helper, and one call into the
provider package. Anything that touches a cloud SDK lives in `pkg/`.

## Adding a new provider

Concretely, what it took to add AWS or Azure to a GCP-only tree:

1. **Command tree** — create `cmd/<provider>.go` with the parent
   `cobra.Command`, persistent flags for auth, and `rootCmd.AddCommand()`
   in its `init()`.
2. **Subcommands** — create `cmd/<provider>_<noun>.go` and
   `cmd/<provider>_lock.go`. Subcommand vars must be prefixed (e.g.
   `awsLockAccounts`) so they don't collide across provider files in the
   `cmd` package.
3. **Package** — create `pkg/<provider>/{auth,<resource>,lock}.go`. Keep
   the public surface narrow: an `AuthOptions` struct, a credential
   constructor, a `List<Resource>s(ctx, cred)` function, and a `Locker`
   struct with `DryRun bool` and a `Lock<Resources>(ctx, []string)`
   method.
4. **Dependencies** — `go get` the SDK packages, then `go build ./...`
   and `go mod tidy`. The cloud SDKs are large; the indirect-deps
   section of go.mod will grow noticeably.
5. **Document** — add the provider's row to the table in README, a
   section in DESIGN, a playbook in TEST. The README's safety section
   should call out any new flag.

The shape of each provider package is intentionally similar but not
identical — forcing a single interface across GCP quotas, AWS SCPs, and
Azure Policy would hide things the operator should see (e.g., GCP's
`--concurrency`, AWS's `--method`, Azure's `--subscription-lock`).

## Updating SDK dependencies

GCP and AWS SDKs cut releases weekly. Azure cuts per-service modules
independently. To refresh:

```bash
go get -u cloud.google.com/go/cloudquotas \
          cloud.google.com/go/resourcemanager \
          google.golang.org/api
go get -u github.com/aws/aws-sdk-go-v2/... \
          github.com/aws/smithy-go
go get -u github.com/Azure/azure-sdk-for-go/sdk/...
go mod tidy
go build ./...
```

Watch for `arm*` package name changes — Azure occasionally renames a
sub-package or splits it into its own module (e.g. `armlocks` was
previously inside `armresources`). The Go compiler will tell you;
update imports and re-tidy.

## Cobra flag conventions

Inherited persistent flags live on the provider parent command (auth,
profile, tenant). Action-specific flags (`--account`, `--project`,
`--method`, `--subscription-lock`) live on the subcommand itself. The
global flag list is intentionally small: only `--dry-run`.

When you add a new persistent flag at a provider level, check that it
shows up under "Global Flags" in `quota-locker <provider> <sub> --help`
— that's the cue cobra inherited it correctly.

## Patterns to follow

- **Pointers everywhere in SDKs.** Use `to.Ptr` (Azure),
  `aws.String`-equivalent helpers (already in `pkg/aws/auth.go` as
  `awsString` / `awsStr`), or local helpers. Don't sprinkle inline
  `&"foo"` because it doesn't compile for string literals.
- **Paginators**, not raw `NextToken` loops. Every SDK has them; use
  them. See `pkg/aws/accounts.go` for AWS, `pkg/azure/subscriptions.go`
  for Azure, the `iterator.Done` loop in `pkg/locker/locker.go` for GCP.
- **Per-target failure logging, not abort.** Lock commands fan out;
  swallowing one target's error and continuing is the right default
  (see DESIGN.md for the reasoning).
- **Dry-run branches before any mutating call.** `if l.DryRun { log; return }`
  belongs at the top of the function that would write, not at the
  caller. This makes it impossible to add a new mutating call that
  silently bypasses dry-run.
- **Idempotency**: when an SDK has CreateOrUpdate, use it. When it only
  has Create, handle `AlreadyExists` explicitly. See the GCP locker's
  fall-through to `UpdateQuotaPreference`.
- **No emoji in CLI output.** Stick to `+ added`, `= unchanged`,
  `- skipped`, `! error`, `~ would (dry-run)`. The output is grep-able.

## Common gotchas

- **GCP `for { qi, err := it.Next() ... }`** — the loop variable `qi` is
  freshly declared each iteration in this construct, so the
  Go-1.22-style `qi := qi` shadow is rejected (`no new variables on left
  side of :=`). Use a different name (we use `info := qi`) if you need
  to capture for a goroutine.
- **AWS `smithy.APIError`** — error codes from AWS are strings, not
  typed values. Compare with `.ErrorCode() == "DuplicatePolicyAttachmentException"`
  not against a constant.
- **Azure `armpolicy.AssignmentsClient.Create`** — `scope` is a full
  resource path (`/subscriptions/<sub>`), not just a subscription ID.
  Easy to get wrong; if you build the wrong scope, the call still
  succeeds but on the wrong place.
- **Cross-account AssumeRole dry-run** — see TEST.md; dry-run does NOT
  actually call AssumeRole, so it uses the caller's config for the
  read-only `ListUsers` / `ListRoles`. The dry-run output therefore
  looks like it came from the caller's account. This is documented
  behaviour, not a bug.

## Style

- `gofmt` (run via your editor; CI will enforce it once it exists).
- No comments that just restate the code. Comments explain "why",
  surprising constraints, or API quirks (see the SCP de-dup comment in
  `pkg/aws/lock.go` or the device-flow-scope caveat in
  `pkg/auth/auth.go`).
- Errors propagate with `%w` and a leading context phrase
  (`"list quotas for %s: %w"`).

## Releasing

There's no tagged release process yet. When there is, expect:

- semver tags on `main`,
- GoReleaser config for cross-platform binaries,
- the test matrix from TEST.md as a release gate.

## Where things will break first

If you're going to ship a regression, the highest-risk areas are:

- **Cobra flag wiring** when you add a subcommand under a provider —
  miss the `.AddCommand` and the subcommand is silently absent from
  `--help`.
- **AWS pagination terminating early** — some endpoints return `null`
  for the last `NextToken` and the SDK paginator handles it, but custom
  loops do not.
- **Azure `armpolicy` package renames** — the team has split this
  module before. Build failures after `go get -u` usually mean an
  import path moved.

When in doubt, dry-run against a sandbox account before merging.
