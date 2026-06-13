# Design

## Goals

1. Reduce every configurable quota (or the cloud-specific equivalent) to a
   "no new work allowed" state for a target account.
2. Operate on one target, several explicit targets, or every target the
   caller can discover, with a single flag (`--all`).
3. Be honest about what each cloud actually supports — do not pretend an
   AWS quota can be decreased when its API forbids it.
4. Default to safe: dry-run mode, interactive confirmation, caller-ARN
   exclusion in the IAM-deny path.

## Non-goals

- A unified abstraction over "quota" across clouds. The three APIs are
  fundamentally different shapes; a forced unification hides important
  trade-offs from the operator. Provider subcommands keep the per-cloud
  vocabulary intact (project / account / subscription).
- An `unlock` command. The unwind paths differ enough per cloud (delete
  Preferences vs. detach SCP vs. delete Policy assignment) that a single
  reverse command would have a worse safety profile than handing the
  operator the explicit `gcloud` / `aws` / `az` commands they already know.
- Idempotency guarantees beyond "running twice does not double-create".

## High-level architecture

```
main.go
  └─ cmd/         # cobra wiring; one file per subcommand
       └─ pkg/    # provider packages — pure logic, no cobra
            ├─ auth/    # GCP OAuth (loopback + ADC)
            ├─ locker/  # GCP quotas
            ├─ aws/     # AWS auth + accounts + locker
            └─ azure/   # Azure auth + subscriptions + locker
```

`cmd/` is a thin layer: flag definitions, prompts, and a call into the
provider package. All cloud interaction lives in `pkg/<provider>/`. This
keeps the per-cloud blast radius scoped to one directory.

## Per-provider strategy

### GCP — real quota writes

GCP is the only cloud with a public API that genuinely lets you set quotas
to zero: the Cloud Quotas API (`cloudquotas.googleapis.com`).

The flow per project:

1. **Service Usage API** (`serviceusage.googleapis.com`) — list every
   enabled service. We need this because Cloud Quotas does not expose a
   "list all services with quotas" endpoint; you must walk per-service.
2. **Cloud Quotas `ListQuotaInfos`** for each service. Many services
   haven't onboarded yet — those return `NOT_FOUND` / `UNIMPLEMENTED` /
   `PERMISSION_DENIED`. We treat those three as "no quotas to manage" and
   continue to the next service.
3. For each `QuotaInfo`, skip if `IsFixed`. Otherwise call
   `CreateQuotaPreference` with `PreferredValue: 0`, supplying
   `ignore_safety_checks=[QUOTA_DECREASE_BELOW_USAGE,
   QUOTA_DECREASE_PERCENTAGE_TOO_HIGH]`. On `AlreadyExists`, fall back to
   `UpdateQuotaPreference` so reruns are idempotent.

Dimensions are intentionally left empty — the preference applies as the
project-wide default. Per-region preferences would multiply the API
surface and aren't necessary for "block new work".

### AWS — SCP or IAM deny, because Service Quotas can't decrease

`servicequotas.amazonaws.com` only accepts increase requests. Setting an
AWS quota to zero via the public API is impossible. The two honest
alternatives are:

**`--method=scp`** (default): create a single `QuotaLockerDenyAll` SCP in
the org (idempotent — we find-by-name before creating) with
`{"Effect":"Deny","Action":"*","Resource":"*"}`, then attach it to each
target account via `organizations:AttachPolicy`. This is the cleanest
lockdown — it sits at the org boundary and cannot be bypassed by anything
inside the account. Requires Organizations + SCPs enabled + the caller
running in the management account.

**`--method=iam-deny`**: create a managed policy with the same deny-all
JSON in every target account, then attach it to every IAM user and role.
Cross-account uses `sts:AssumeRole` against
`arn:aws:iam::<acct>:role/<assume-role-name>` (default
`OrganizationAccountAccessRole`, the role AWS auto-creates in Organizations
member accounts). Service-linked roles (`/aws-service-role/...`) are
skipped because IAM rejects policy attachment to them.

In the caller's own account, the caller's STS ARN is automatically added
to the exclude list so the operator running the tool keeps access. In
fanned-out accounts that protection does not apply — operators must
explicitly pass `--exclude-arn` substrings for any break-glass role they
need to keep working.

### Azure — deny-all Policy, optional management lock

The Azure Quota API (`Microsoft.Quota`) is request-based and many quotas
reject zero. The reliable lockdown is Azure Policy.

Per subscription:

1. **`PolicyDefinitionsClient.CreateOrUpdate`** at subscription scope —
   a custom definition `quota-locker-deny-all` with rule:
   ```json
   {"if": {"field": "type", "exists": "true"}, "then": {"effect": "deny"}}
   ```
   `Mode: All` so it matches every resource type, not just tracked ones.
2. **`PolicyAssignmentsClient.Create`** at the same scope, referencing
   that definition. `EnforcementMode: Default` — Azure rejects new
   creates/updates immediately.
3. **(optional)** If `--subscription-lock=readonly|cannotdelete`, also
   apply a `Microsoft.Authorization/locks` resource at subscription scope.
   `ReadOnly` blocks modifications and deletes; `CanNotDelete` only blocks
   deletes. The policy assignment handles "no new resources"; the lock
   handles "existing resources are frozen too".

Reverting requires deleting the assignment (and the lock, if present).
The policy *definition* can be left in place — removing it requires that
no assignments reference it, so the unwind order is assignment → lock →
definition (if you choose to clean up).

## Authentication

| Cloud | Default | Why |
|---|---|---|
| GCP | OAuth loopback | Works headless-over-SSH (with port-forward), no `gcloud` install required, full `cloud-platform` scope. ADC is the alternative; pure device flow does not grant `cloud-platform` and was removed. |
| AWS | Default SDK chain | Most callers already have `~/.aws/credentials` or are running in EC2/Lambda. SSO via IAM Identity Center device flow is opt-in via `--sso-start-url`. |
| Azure | Device-code via Entra ID | The one cloud where device-code is the canonical headless-friendly path and grants management-plane scopes. `az login`-cached creds and service principals are also supported. |

## Idempotency

Each provider's lock handler is designed so a re-run is a no-op or a
controlled fallthrough:

- GCP: `CreateQuotaPreference` → on `AlreadyExists` →
  `UpdateQuotaPreference` with `AllowMissing: true`.
- AWS SCP: search-by-name finds the existing SCP and reuses its ID;
  `AttachPolicy` returns `DuplicatePolicyAttachmentException` for already
  attached, which we log as `= already attached`.
- AWS IAM: `CreatePolicy` returns `EntityAlreadyExists`, which we map to
  the deterministic ARN `arn:aws:iam::<acct>:policy/QuotaLockerDenyAll`.
- Azure: `CreateOrUpdate` for the definition and management lock are
  inherently idempotent. `Create` on the assignment will fail on rerun;
  this is acceptable because the assignment is the last step and a failure
  means "lock already applied".

## Failure handling

The locker logs per-target failures and continues. Rationale: when the
operator says "lock 47 accounts", aborting on the third account's IAM
glitch is worse than locking the other 44 and surfacing the failed three.
Stderr is the audit log; exit code 0 even with partial failures.

This is a deliberate choice for a destructive, mass-action tool. The
alternative — fail-fast — would force the operator to re-discover which
accounts succeeded on every retry.

## Concurrency

GCP supports `--concurrency` (default 4) for parallel
`CreateQuotaPreference` writes within a project; the limit is enforced
with a semaphore. Cross-project work is sequential — most operators
target a small number of projects and the parallelism inside each is
already enough.

AWS and Azure are sequential per account/subscription. The destructive
ops are not the bottleneck (a few API calls each) and serializing them
keeps log output coherent.

## Extension points

Adding a provider follows the existing shape:

1. `cmd/<provider>.go` — parent cobra command with provider-specific
   persistent flags.
2. `cmd/<provider>_<action>.go` — one file per subcommand.
3. `pkg/<provider>/auth.go` — token/credential acquisition.
4. `pkg/<provider>/<resource>.go` — listing (projects/accounts/subs).
5. `pkg/<provider>/lock.go` — the lockdown logic.

See [DEVELOPMENT.md](DEVELOPMENT.md) for the step-by-step.
