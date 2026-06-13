# Testing

There is no unit test suite yet. This document describes how to validate
changes manually until that lands. CI testing is constrained because every
destructive code path needs a real cloud account.

## What you can verify without any cloud

```bash
go build ./...      # compile everything
go vet ./...        # static checks
go test ./...       # currently a no-op; will run when packages get tests

# Surface-level smoke test — exercises cobra wiring + flag parsing.
./quota-locker --help
./quota-locker gcp --help
./quota-locker aws lock --help
./quota-locker azure lock --help
```

A build failure or a missing-flag regression will catch most refactor
mistakes. Anything that touches a cloud API has to be exercised against a
sandbox.

## What you cannot trust until you run it against a real cloud

- That a given AWS region / Org structure has the SCP feature enabled.
- That a service-linked role gets skipped correctly.
- That `Microsoft.Authorization/policyDefinitions` is granted to the
  caller in the Azure tenant.
- That the GCP Cloud Quotas API accepts a `preferredValue=0` for any
  given service. Many services have onboarded; others return
  `UNIMPLEMENTED` and are silently skipped.

## Sandbox setup

Always test against accounts you control and can afford to nuke. The
recommended pattern is one disposable sandbox per cloud:

### GCP sandbox

```bash
PROJECT=quota-locker-sandbox-$RANDOM
gcloud projects create $PROJECT
gcloud beta billing projects link $PROJECT --billing-account=<id>
gcloud services enable cloudquotas.googleapis.com --project=$PROJECT
gcloud services enable serviceusage.googleapis.com --project=$PROJECT
gcloud services enable compute.googleapis.com --project=$PROJECT
```

### AWS sandbox

A standalone account (not a member of your real Org) is safest for
`--method=iam-deny`. For `--method=scp` you need an Organizations setup;
the AWS-provided sandbox-org pattern works.

### Azure sandbox

A trial subscription or a pay-as-you-go subscription with $0 budget is
fine — the policy assignment is free.

## Per-provider playbook

### GCP

1. **Auth dry-run**:
   ```bash
   ./quota-locker gcp --auth=adc projects | head
   ```
   Confirms ADC works and at least one project is reachable.

2. **Single-project dry-run**:
   ```bash
   ./quota-locker gcp --auth=adc lock \
       --project $PROJECT --dry-run
   ```
   Expect a stream of `~ would zero <service>/<quotaId>` lines plus a
   `summary: seen=N would apply=M skipped=K failed=0`. If `skipped` is
   high, many of the project's enabled services have fixed quotas — fine.

3. **Single-project real run**:
   ```bash
   ./quota-locker gcp --auth=adc lock --project $PROJECT --yes
   ```
   In the Cloud Console, navigate to the project → IAM & Admin → Quotas.
   You should see `QuotaPreference` overrides at zero.

4. **Verify lockdown**:
   ```bash
   gcloud compute instances create test-vm --project=$PROJECT --zone=us-central1-a
   # Should fail with a quota-exceeded error.
   ```

5. **Rerun idempotency**:
   ```bash
   ./quota-locker gcp --auth=adc lock --project $PROJECT --yes
   ```
   Should report `= updated <service>/<quotaId>` for previously-set
   preferences rather than a wall of `! AlreadyExists`.

### AWS — `--method=scp`

1. **List accounts**:
   ```bash
   ./quota-locker aws --profile org-mgmt accounts
   ```
   Should list every Org member.

2. **Dry-run against one account**:
   ```bash
   ./quota-locker aws --profile org-mgmt lock \
       --account 222222222222 --dry-run
   ```
   First run: `~ would create SCP QuotaLockerDenyAll`. Subsequent runs
   (real or dry) should say `reusing existing SCP`.

3. **Real run against the sandbox account**:
   ```bash
   ./quota-locker aws --profile org-mgmt lock \
       --account 222222222222 --yes
   ```

4. **Verify**:
   ```bash
   aws --profile <role-into-222> ec2 describe-instances
   # Should fail with UnauthorizedOperation / explicit deny.
   ```

5. **Revert**:
   ```bash
   aws organizations detach-policy \
       --policy-id <id-from-step-3-output> \
       --target-id 222222222222
   ```

### AWS — `--method=iam-deny` (single account)

1. **Confirm caller exclusion logic**: dry-run, look for the
   `excluding caller arn:aws:iam::...:user/...` line.

2. **Dry-run** with at least one `--exclude-arn` covering your
   break-glass role:
   ```bash
   ./quota-locker aws --profile sandbox lock \
       --account <sandbox-acct> --method=iam-deny \
       --exclude-arn=role/BreakGlassAdmin --dry-run
   ```

3. **Real run** — only against a sandbox you can rebuild:
   ```bash
   ./quota-locker aws --profile sandbox lock \
       --account <sandbox-acct> --method=iam-deny \
       --exclude-arn=role/BreakGlassAdmin --yes
   ```

4. **Verify** with a non-excluded principal:
   ```bash
   aws --profile <non-excluded-user> s3 ls
   # Should fail with explicit deny.
   ```

5. **Revert**: for every IAM user/role the tool touched,
   `detach-user-policy` / `detach-role-policy` followed by
   `delete-policy QuotaLockerDenyAll`.

### AWS — cross-account IAM-deny

This path uses `sts:AssumeRole` so the assume-role trust on each target
account must permit your management-account identity. The default role
name is `OrganizationAccountAccessRole`, which AWS auto-creates in
Organizations member accounts.

1. **Dry-run** — note that dry-run does not actually call AssumeRole; it
   prints `~ would assume <arn>` and uses the caller's config for the
   read-only `ListUsers` / `ListRoles` calls. That means dry-run output
   *appears* to come from the caller's account. This is a known
   limitation; treat dry-run as flag-validation, not behaviour
   verification, for cross-account.

2. **Real run against ONE sandbox member**, never the full Org first:
   ```bash
   ./quota-locker aws --profile org-mgmt lock \
       --account <sandbox-member> --method=iam-deny \
       --assume-role-name=OrganizationAccountAccessRole \
       --exclude-arn=role/BreakGlassAdmin --yes
   ```

3. **Verify and revert** as above, inside the member account.

### Azure

1. **List subscriptions**:
   ```bash
   ./quota-locker azure --tenant <tenant> subscriptions
   ```

2. **Dry-run**:
   ```bash
   ./quota-locker azure --tenant <tenant> lock \
       --subscription <sub-uuid> --dry-run
   ```
   Look for `~ would create policy definition ...` and `~ would create
   assignment ... on /subscriptions/<sub>`.

3. **Real run, policy only**:
   ```bash
   ./quota-locker azure --tenant <tenant> lock \
       --subscription <sub-uuid> --yes
   ```

4. **Verify**:
   ```bash
   az group create -n test-rg -l eastus --subscription <sub-uuid>
   # Should fail with RequestDisallowedByPolicy.
   ```

5. **Add management lock** on top:
   ```bash
   ./quota-locker azure --tenant <tenant> lock \
       --subscription <sub-uuid> --subscription-lock=readonly --yes
   ```
   Existing resources are now read-only too.

6. **Revert**:
   ```bash
   az policy assignment delete -n quota-locker-deny-all \
       --scope /subscriptions/<sub-uuid>
   az lock delete -n quota-locker-sub-lock --resource-group ""
   az policy definition delete -n quota-locker-deny-all \
       --subscription <sub-uuid>
   ```

## Test matrix for a release

Before tagging a release, the maintainer runs:

- [ ] `go build ./...`, `go vet ./...` clean.
- [ ] `--help` for `root`, every provider parent, every subcommand.
- [ ] GCP single-project dry-run + real run + idempotent rerun.
- [ ] AWS SCP single-account dry-run + real run + revert.
- [ ] AWS IAM-deny dry-run with `--exclude-arn` present (output asserts
      caller-ARN exclusion).
- [ ] Azure single-subscription dry-run + real run + revert.
- [ ] Azure run with `--subscription-lock=readonly` against a sandbox.

Anything that adds a new provider or auth mode adds a row to this matrix.

## Adding unit tests (when we get there)

The provider packages are designed to be testable: each takes a typed
client (or `aws.Config`/`azcore.TokenCredential`) which can be replaced
with a fake. Suggested first targets:

- `pkg/locker.Locker.zeroQuota` — assert it sets the safety-check waivers
  and falls through to `UpdateQuotaPreference` on `AlreadyExists`.
- `pkg/aws.findSCPByName` — paginator behaviour.
- `pkg/aws.Locker.lockAccountIAM` — caller-ARN exclusion logic with a
  fake IAM client.
- `pkg/azure.ParseLockLevel` — pure function, no infra.

Avoid mocking the entire SDK — wrap it in a narrow interface inside the
provider package and mock that.
