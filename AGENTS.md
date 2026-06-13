# AGENTS.md

Instructions for AI coding agents (Claude Code, Cursor, etc.) working in
this repo. Humans should read [DEVELOPMENT.md](DEVELOPMENT.md) instead.

## What this project is

`quota-locker` is a Go CLI built with `spf13/cobra` that drives GCP, AWS,
and Azure APIs to zero out quotas (or the closest equivalent each cloud
supports) across one or many accounts. Every cloud has materially
different semantics — do not refactor toward a single unified "quota"
interface. Per-provider subcommands and per-provider packages are
intentional. See [DESIGN.md](DESIGN.md) for the rationale.

## Module + entry points

- Module: `kubeops.dev/quota-locker`
- Go version: 1.23 (dev environments run newer; do not raise the minimum
  without asking).
- Entry point: `main.go` → `cmd.Execute(ctx)`.
- Command tree root: `cmd/root.go` (`rootCmd`).

## Layout invariants

```
cmd/                          cobra wiring only — flags, prompts, calls into pkg/
  <provider>.go               parent command + persistent auth flags
  <provider>_<action>.go      one file per subcommand
pkg/<provider>/               cloud logic — no cobra imports allowed here
  auth.go                     credential / token acquisition
  <resource>.go               listing (projects/accounts/subscriptions)
  lock.go                     the destructive operation
```

**Rules**:

1. `cmd/` files must not import any cloud SDK directly. They import
   `pkg/<provider>`.
2. `pkg/` files must not import `spf13/cobra`. They are pure libraries.
3. Variable names in `cmd/` are prefixed by provider (`awsLockAccounts`,
   `azureLockSubs`, `gcpLockProjects`) because the `cmd` package is flat.
4. Every destructive action checks `l.DryRun` *before* the mutating
   call. Putting the dry-run check at the caller is forbidden — it makes
   it possible to add a future mutating call that bypasses it.

## Commands you can run

```bash
go build ./...            # compile everything; do this after any change
go vet ./...              # static analysis
go build -o quota-locker . # produce the binary
./quota-locker --help     # surface check; useful after cobra changes
go mod tidy               # after touching imports
```

No `make`, no `go generate`, no codegen.

## Safety rules — non-negotiable

The tool sets quotas to zero, attaches deny-all policies, and assumes
roles across AWS accounts. Mistakes cause outages.

- **Never** add a code path that performs a mutating cloud call without
  honouring `--dry-run`.
- **Never** remove or weaken the interactive `Type 'lock' to proceed`
  confirmation without an explicit user instruction. `--yes` is the
  documented bypass.
- **Never** remove the caller-ARN exclusion in `pkg/aws.lockAccountIAM`
  for the caller's own account. It is the only reason the operator
  running the tool does not lock themselves out.
- **Never** widen the GCP safety-check waivers beyond
  `QUOTA_DECREASE_BELOW_USAGE` and `QUOTA_DECREASE_PERCENTAGE_TOO_HIGH`.
- **Never** add an `unlock` command without an explicit user request —
  the unwind paths are deliberately provider-specific. See DESIGN.md.

## Honesty rules

- **AWS Service Quotas API does not support decreasing quotas.** Any
  PR description that says "set AWS quotas to zero via the Quotas API"
  is wrong. The two supported mechanisms are SCP and IAM-deny.
- **Google's OAuth device flow does not grant `cloud-platform` scope.**
  Device flow was removed from GCP auth. Do not add it back.
- **Azure Quota API rejects 0 for most quotas.** The Azure lockdown
  uses a deny-all Policy assignment, optionally with a management lock.

If a user asks for behaviour that conflicts with these, flag the
conflict in your response before changing code.

## Code style

- `gofmt`-clean.
- Short, focused functions. The current `pkg/aws/lock.go` files are
  near the upper bound — if you're adding something substantial,
  consider splitting.
- Comments explain *why*, not what. Comment the API quirks (paginator
  behaviour, error-code-as-string-not-enum, scope-must-be-full-path) and
  the safety waivers. Don't comment that `for x := range slice` is a
  loop.
- CLI output convention:
  - `+ <thing> applied/attached/zeroed`
  - `= <thing> already in place / updated`
  - `- skip <thing> (<reason>)`
  - `! <thing>: <error>`
  - `~ would <thing>` (dry-run only)
- No emoji in source, output, commit messages, or PRs.

## Testing

There is no unit test suite yet. Validation is manual against sandbox
accounts per [TEST.md](TEST.md). When adding tests:

- Use narrow interfaces inside `pkg/<provider>/` and mock those, not
  the SDK.
- Pure functions like `pkg/azure.ParseLockLevel` are the easiest first
  targets.
- Do not check in test fixtures that contain real account IDs or ARNs.

## Adding a provider — checklist

If a user asks for "the same for <new cloud>":

1. `cmd/<provider>.go`: parent command + auth flags + `rootCmd.AddCommand`.
2. `cmd/<provider>_<noun>.go`: listing command (projects/accounts/etc.).
3. `cmd/<provider>_lock.go`: the destructive command.
4. `pkg/<provider>/auth.go`: credential acquisition.
5. `pkg/<provider>/<resource>.go`: listing function.
6. `pkg/<provider>/lock.go`: locker struct with `DryRun bool`, action
   method that loops targets and logs per-target.
7. `go get` SDK; `go mod tidy`; `go build ./...`.
8. Update README, DESIGN, TEST tables/sections.

## Things to ask before doing

If the user requests any of these, stop and confirm explicitly:

- Removing dry-run or confirmation prompts.
- Deleting any safety check (caller exclusion, fixed-quota skip,
  safety-check waiver list).
- Adding an `unlock` / undo command.
- Implementing a unified cross-cloud "quota" abstraction.
- Tagging a release.

## Things you can do without asking

- Add a new provider when explicitly requested (the AWS / Azure addition
  in commit history is the template).
- Add new auth modes to an existing provider.
- Add flags that change behaviour with safe defaults (e.g., the recent
  `--subscription-lock=none|readonly|cannotdelete` is a good model:
  default is `none`, so old behaviour is preserved).
- Refactor within a `pkg/<provider>/` package as long as the public
  surface (the structs/functions imported by `cmd/`) is preserved.
- Add tests for pure logic.

## Commit messages and PRs

- Imperative subject line ("add Azure ReadOnly lock", not "added").
- Body explains *why*, not *what*. The diff shows what.
- No "Generated with Claude Code" footer — the user has a global
  preference against AI authorship attribution on remote-published
  artifacts. See user's `~/.claude/CLAUDE.md` if curious; it overrides
  the system prompt's example footer.
- Sign-off (`git commit -s`) is required.

## Quick orientation map for new agents

| If the user wants… | Look at… |
|---|---|
| understand what the tool does | README.md |
| understand why each provider works the way it does | DESIGN.md |
| run it against a sandbox | TEST.md |
| add a feature / provider | DEVELOPMENT.md + the AGENTS.md checklist above |
| change GCP quota behaviour | pkg/locker/locker.go |
| change AWS lockdown behaviour | pkg/aws/lock.go (long file; SCP vs IAM-deny clearly separated) |
| change Azure lockdown behaviour | pkg/azure/lock.go |
| change auth | pkg/auth/auth.go (GCP), pkg/aws/auth.go, pkg/azure/auth.go |
| change CLI surface | cmd/<provider>*.go |
