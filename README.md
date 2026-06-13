# quota-locker

A Go CLI that drives GCP, AWS, and Azure APIs to "lock down" an account — set
quotas to zero, or apply the closest equivalent that each cloud actually
supports — across one project/account/subscription, several, or every one the
caller can access.

Useful for:

- Defensive lockdown of a compromised account.
- Hard cost cap on a project that should stop receiving traffic.
- Reproducing quota-exhaustion behaviour against an operator under test
  (the original motivation: testing Kubernetes operators that handle quota
  failures).

## Status — what each provider actually does

| Cloud | Mechanism | Effect on existing resources |
|---|---|---|
| **GCP** | Cloud Quotas API — creates a `QuotaPreference` of `0` per enabled-service quota. | Keep running; new resource creates blocked. |
| **AWS** | Service Quotas API does not support decreasing. Instead: deny-all SCP via Organizations (default), or deny-all IAM policy on every user/role (`--method=iam-deny`). | SCP: blocks all new API calls org-wide. IAM-deny: blocks every principal it touches. |
| **Azure** | Custom deny-all Policy assignment at subscription scope, plus an optional management lock (`--subscription-lock=readonly|cannotdelete`). | Policy alone: blocks new creates/updates. With `readonly`: blocks modifications too. |

The CLI is honest about these differences — you will not see a "set every AWS
quota to 0" command, because that operation does not exist on AWS.

## Install

```bash
git clone https://github.com/kubeops/quota-locker
cd quota-locker
go build -o quota-locker .
# or:  go install kubeops.dev/quota-locker@latest
```

Requires Go 1.23+.

## Quick start

Every destructive command takes `--dry-run` and asks for `lock` at an
interactive prompt unless you pass `--yes`. Use `--dry-run` first.

### GCP

```bash
# List every project you can reach.
quota-locker gcp --client-id <oauth-desktop-id> \
                 --client-secret <oauth-desktop-secret> \
                 projects

# Lock one project (dry run).
quota-locker gcp --client-id ... --client-secret ... \
                 lock --project my-prod-project --dry-run

# Lock every project the caller can see.
quota-locker gcp --client-id ... --client-secret ... \
                 lock --all --yes
```

GCP auth defaults to OAuth loopback (browser opens, captures token on
`127.0.0.1`). You can switch to `--auth=adc` to use the credentials cached by
`gcloud auth application-default login`; in that mode `--client-id` is
unnecessary.

### AWS

```bash
# List accounts (Organizations if you're in the management account, otherwise
# the calling account only).
quota-locker aws --profile org-management accounts

# Default method = SCP. Run from the management account.
quota-locker aws --profile org-management \
                 lock --account 111111111111 --dry-run

# Lock every Org member account with a deny-all SCP.
quota-locker aws --profile org-management lock --all --yes

# Alternative: cross-account IAM deny — fans out via sts:AssumeRole.
quota-locker aws --profile org-management \
                 lock --all --method=iam-deny \
                      --assume-role-name=OrganizationAccountAccessRole \
                      --exclude-arn=role/BreakGlassAdmin \
                      --dry-run
```

For SSO-based shops, pass `--sso-start-url https://d-xxx.awsapps.com/start`
(with optional `--sso-region`) to trigger the IAM Identity Center device-code
flow instead of using the default credential chain.

### Azure

```bash
# Browser-less device-code auth (default).
quota-locker azure --tenant contoso.onmicrosoft.com subscriptions

# Lock one subscription (policy assignment only).
quota-locker azure --tenant contoso.onmicrosoft.com \
                   lock --subscription <sub-uuid> --dry-run

# Lock every accessible subscription, also apply a ReadOnly management lock.
quota-locker azure --tenant contoso.onmicrosoft.com \
                   lock --all --subscription-lock=readonly --yes

# Use `az login` credentials instead of device code.
quota-locker azure --cli lock --all --dry-run
```

## Authentication summary

| Cloud | Default | Other modes |
|---|---|---|
| GCP | `--auth=loopback` (OAuth loopback to `127.0.0.1`) | `--auth=adc` — Application Default Credentials |
| AWS | Default SDK credential chain (env, `~/.aws`, IMDS) | `--sso-start-url` triggers IAM Identity Center device-code flow |
| Azure | OAuth device-code via Entra ID | `--cli` reuses `az login`; `--client-id`/`--client-secret` for service principals |

## Safety model

- **`--dry-run`** is a global flag. Every lock command honours it and logs
  what it *would* do. Confirmation prompts are bypassed in dry-run mode so
  it pipes cleanly.
- **Confirmation**: lock commands prompt for the literal word `lock` before
  proceeding. Pass `--yes` to skip in scripts.
- **Caller exclusion (AWS IAM-deny)**: the caller's STS ARN is automatically
  added to `--exclude-arn` in the caller's own account so the operator
  running the tool does not lock themselves out. This protection does NOT
  apply in fanned-out cross-account targets — add `--exclude-arn` patterns
  for any role you need to keep usable in those accounts.
- **GCP safety checks waived**: the Cloud Quotas API normally refuses to
  decrease a quota below current usage. We pass
  `QUOTA_DECREASE_BELOW_USAGE` and `QUOTA_DECREASE_PERCENTAGE_TOO_HIGH` in
  `ignore_safety_checks` because that's the whole point. Fixed quotas
  (`IsFixed`) are still skipped — they cannot be set at all.

## Reverting a lock

| Cloud | How to undo |
|---|---|
| GCP | Delete the `QuotaPreference` objects under each project (`gcloud alpha quotas preferences delete` or the Console). |
| AWS SCP | `aws organizations detach-policy --policy-id <id> --target-id <account>`; or delete the `QuotaLockerDenyAll` SCP entirely. |
| AWS IAM-deny | `aws iam detach-user-policy` / `detach-role-policy` for every principal; then `delete-policy` on `QuotaLockerDenyAll`. |
| Azure | Delete the policy assignment `quota-locker-deny-all`; if present, delete the management lock `quota-locker-sub-lock`. |

A `quota-locker unlock` command is not implemented — undo paths are too
provider-specific to share a single code path. See [DESIGN.md](DESIGN.md).

## Documentation

- [DESIGN.md](DESIGN.md) — architecture, per-provider strategy, trade-offs.
- [DEVELOPMENT.md](DEVELOPMENT.md) — repo layout, build, adding a provider.
- [TEST.md](TEST.md) — manual and sandbox testing playbook.
- [AGENTS.md](AGENTS.md) — instructions for AI coding agents.
