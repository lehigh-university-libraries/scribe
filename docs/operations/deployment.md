# Deployment

## Trust model

- Pull-request CI has read-only repository permissions and no cloud secrets.
- Same-repository pull requests automatically deploy a preview; forks run
  secret-free CI but do not receive cloud credentials.
- Backend and frontend PR source is built into OCI archives only after the
  secret-free test gate. A protected publisher copies those archives to GHCR;
  no pull-request script runs with registry credentials. Credentialed OCR
  builds, Terraform, and helper scripts come from the trusted base commit.
- The workflow requests OIDC only from a protected environment. Repository
  operators must configure required reviewers on that GitHub environment;
  workflow source cannot create or prove that protection.
- Images are resolved by digest before Terraform receives them. The reviewed
  frontend GHCR digest is a delivery-only source: protected apply promotes it
  to GAR, and Terraform receives and records only the GAR digest actually run
  by the Cloud Run sidecar.
- Backend, frontend, OCR, and Vault images are digest-pinned, emit provenance
  and SBOM attestations where the build path supports them, and must pass their
  packaged-runtime smoke tests. Runtime image vulnerability scanning is
  deferred and is not a CI, deployment, or release gate.
- Production apply requires the protected `production` environment.

Terraform receives the exact reviewed Git commit as the Compose source ref,
and deployable images come directly from build-produced immutable digests. PR code receives
no CI credentials while it is tested and built. After environment approval, that
image runs only as the preview ingress identity, whose sole workload permission
is power control over that preview VM. The job that executes a PR-built image
for backend probing uses a dedicated no-data identity with no Vault, bucket,
Pub/Sub, OCR-invoker, or project grants. The OCR deep probe has a separate
no-data identity whose only workload grants invoke the exact segmentation and
transcription services under test. Never grant preview identities production resources. Preview
infrastructure and shared helpers run from a SHA resolved independently from
protected `main`, including teardown after a PR is retargeted. Preview
deploys reuse the
digest-pinned OCR images keyed to the exact protected-base commit, so PRs do
not rebuild or resolve mutable model-image tags.

Preview ingress is already restricted by the protected `ALLOWED_IPS` policy,
so preview application auth deliberately uses its isolated anonymous workspace
instead of a reusable Google OAuth client. `AUTH_PREVIEW_ANONYMOUS` is accepted
only when `VAULT_WORKSPACE=pr-<number>` and the public origin is the matching
HTTPS `scribe-pr-<number>-*.run.app` service; production and dev fail startup if
it is enabled. Protected orchestration creates only a random database password
under `scribe/previews/scribe-pr-<number>@<project>.iam.gserviceaccount.com`.
Pull-request code receives no dev or production
OAuth, database, or provider credential. Reapply preserves the password, and a
successful preview destroy recursively removes its Vault namespace.

Kraken model declarations in `config/ocr.yaml` include the immutable DOI,
expected filename, and SHA-256 digest. The image build refuses a download whose
content does not match that digest; this applies to both the default BLLA
segmentation model and transcription models.
The segmentor image installs the exact official CPU-only PyTorch/Torchvision
pair because its Cloud Run services do not request GPUs; CUDA runtime packages
must not be added to that image. `config/segmentor-requirements.lock` pins every
Python transitive and accepted artifact hash. Change the short reviewed input,
run `make segmentor-lock`, and review the generated lock for an upgrade; image
builds use `--require-hashes --only-binary=:all:` and never re-resolve mutable
Python metadata.

Closing a PR or retargeting it away from `main` destroys its preview and then
deletes the empty `pr-<number>` Terraform workspace. Teardown remains
serialized behind any in-flight apply so state is never mutated concurrently.
Destroy reads the exact Compose SHA and image digests recorded in that
workspace's `deployment_inputs`; it never resolves a tag, pulls an image, or
builds PR code with deployment credentials. Missing or malformed state fails
closed with an operator recovery instruction, because guessing replacement
inputs could mutate the plan before teardown.

When a Terraform upgrade adds `moved` blocks, normalize the selected
workspace's resource addresses before any targeted maintenance:

```bash
make tf-prod ACTION=refresh
# or: make tf-preview PR=75 ACTION=refresh
```

Refresh validates and replays every field in the exact `deployment_inputs`
schema recorded by that workspace, including its Compose SHA, data generation,
runtime image digests, network/admission settings, monitoring channels, and
Vault identities. Caller values for those recorded fields are ignored. The
wrapper saves a full-graph `terraform plan -refresh-only`, permits only no-op
address moves, rejects non-move drift or non-no-op resource/output actions,
prints the accepted plan, and auto-applies that exact saved plan. Target sets
and non-empty `TF_CLI_ARGS*` variables are rejected; refresh and destroy also
refuse to create a missing workspace. The wrapper never invokes image
resolution or build tooling, but remote-state, provider, backup-policy, and
Vault authentication prerequisites may still be required. Proceed with a
separate targeted plan or apply only after reviewing the refresh output.

A workspace created before `deployment_inputs` existed cannot safely use
`ACTION=refresh`, because there are no recorded inputs to replay. For that
one-time state-only case, run `make tf-dev ACTION=normalize-moves` (or the
corresponding production/preview target). The command first verifies state
backup policy, then applies only the transitive address changes declared by
Scribe and the pinned cloud-compose module with `terraform state mv`. It does
not refresh providers or plan/apply infrastructure, is retry-idempotent, and
fails if both sides of a move are populated.

The trusted `pull_request_target` workflow initially belongs to the protected
base revision, so GitHub's automatic environment record is not evidence for the
pull-request revision. After Terraform attests the deployed Cloud Run revision
and readiness, a separate no-checkout job revalidates that the same-repository
PR is still open against `main` at the tested head SHA. It then creates a
transient, non-production `preview` deployment for that exact SHA with
`auto_merge: false`, no mutable required-context resolution, and the attested
HTTPS preview URL. The record reaches `success` only after readiness passes.
Failed or cancelled reruns receive a terminal `failure` or `error` and
supersede earlier records for the same PR; a stale run cannot approve a newer
head. A successful destroy marks every explicit deployment for that PR
`inactive`, while the serialized workflow keeps apply and teardown from racing.

Create protected `preview` and `production` GitHub environments and configure
required reviewers in GitHub before the first release. Treat this setting as an
operator prerequisite and verify it in the hosted deployment evidence; an
`environment:` workflow declaration does not configure reviewers. Each needs
`OCR_GCLOUD_OIDC_POOL` and `OCR_GSA` environment-scoped GitHub Actions
configuration variables. These are non-secret WIF resource identifiers;
keeping them environment-scoped lets the same reusable workflow select the
dedicated preview or production identity without inheriting repository
secrets. Each environment also needs
`GCLOUD_OIDC_POOL`, `GSA`, `TF_STATE_BUCKET`, and the other OIDC/Vault/state
secrets used by the workflow. The deploy secrets and OCR configuration must
name different, full numeric-project provider resources in dedicated pools.
Production and preview use distinct service accounts and pools for each
identity class; sharing a target service account would add a second federated
impersonation principal and fail the live preflight. `OCR_GSA` is mandatory
and its IAM roles must be
limited to reading and publishing Artifact Registry images; the broader deploy
`GSA` and its identity pool are never used as OCR-build fallbacks. Each
environment also needs a
`VAULT_ADMIN_EMAILS` environment variable containing a non-empty JSON array of
the `lehigh.edu` accounts enforced by the Vault JWT role, for example
`["operator@lehigh.edu"]`. Keep human access environment-scoped;
do not hard-code an administrator in workflow source.

The `production` environment also requires `BACKUP_GCLOUD_OIDC_POOL` and
`BACKUP_RESTORE_GSA`. Both are dedicated to backup verification and distinct
from deploy, OCR, application, VM, Vault runtime, and Vault initializer
identities. Terraform grants the service account only the custom disposable-
restore verifier role, Storage Transfer observation, bucket metadata
observation, and object reads from the state and independent uploads backup
buckets. It receives no Vault token, KMS decrypt, production upload write,
application runtime, or Terraform-apply capability.

### External Workload Identity Federation contract

The identity pools and initial service-account impersonation bindings are an
external bootstrap boundary: application Terraform cannot safely create the
provider that authenticates its own first run. Each protected job therefore
runs `ci/verify-gcp-wif.sh` immediately after authentication and before any
publish, plan, apply, restore, or Vault operation. A missing inspection grant
or a broader live policy fails the job. A successful local/static check is not
release evidence; the protected workflow preflight must pass against GCP.

Configure one active provider per dedicated pool with no custom audience, the
issuer `https://token.actions.githubusercontent.com`, and exactly these
attribute mappings:

```text
google.subject             = assertion.sub
attribute.repository       = assertion.repository
attribute.workflow_ref     = assertion.workflow_ref
attribute.ref              = assertion.ref
attribute.environment      = assertion.environment
```

The provider condition must exactly match the condition generated for its
environment and identity class:

```bash
WIF_EXPECTED_ENVIRONMENT=production WIF_IDENTITY_CLASS=deploy \
  ./ci/verify-gcp-wif.sh --print-expected-condition
WIF_EXPECTED_ENVIRONMENT=production WIF_IDENTITY_CLASS=ocr \
  ./ci/verify-gcp-wif.sh --print-expected-condition
WIF_EXPECTED_ENVIRONMENT=production WIF_IDENTITY_CLASS=backup \
  ./ci/verify-gcp-wif.sh --print-expected-condition
WIF_EXPECTED_ENVIRONMENT=preview WIF_IDENTITY_CLASS=deploy \
  ./ci/verify-gcp-wif.sh --print-expected-condition
WIF_EXPECTED_ENVIRONMENT=preview WIF_IDENTITY_CLASS=ocr \
  ./ci/verify-gcp-wif.sh --print-expected-condition
```

All conditions require this repository, `refs/heads/main`, and the exact
protected environment. Production deploy permits only `terraform-apply.yaml`
and `terraform-drift.yaml`; production OCR permits only the caller
`terraform-apply.yaml`; backup permits only `backup-verification.yaml`.
Preview deploy and OCR permit only `terraform-preview.yaml`. That preview
workflow uses `pull_request_target` solely as reviewed main-branch
orchestration; PR-head code never requests the token.

Each target service-account IAM policy must consist of exactly one
unconditional `roles/iam.workloadIdentityUser` binding whose sole member is
its pool's repository-scoped `principalSet`. Additional Token Creator, service
account user, key administrator, custom-role, conditional, user, group, or
service-account bindings fail the preflight because any of them could broaden
the reviewed impersonation boundary. The identity also needs read-only
inspection access for `workloadIdentityPoolProviders.get`,
`workloadIdentityPoolProviders.list`, and
`iam.serviceAccounts.getIamPolicy`; the preflight intentionally cannot pass
without live evidence.

Bootstrap the four external production OCR, production backup, preview deploy,
and preview OCR identities from the already authenticated production deploy
service account with `make bootstrap-gcp-identities`. The target defaults to a
read-only plan and emits non-secret JSON containing the source and target
provider resources, service-account emails, observed source roles, explicit
preview grants, state-bucket actions, and notification channel. Review that JSON before rerunning
with `ACTION=apply`. Set `GCLOUD_PROJECT`, `SOURCE_DEPLOY_GSA`,
`SOURCE_DEPLOY_PROVIDER`, and `MONITORING_NOTIFICATION_EMAIL`; optionally set
`TF_STATE_BUCKET` to enable versioning, enforce at least 14 days of soft-delete
retention, and grant only the two explicit state-backend roles. The operator
never clones the production deploy identity. Its fixed preview allowlist omits
Owner, Editor, security administration, and role administration; unexpected
existing roles fail closed. The current shared-project interim does include
`roles/resourcemanager.projectIamAdmin` because cloud-compose manages the
per-preview VM's logging and metric-writer project bindings. This is not true
least privilege: the exact trusted-main repository, workflow, ref, and
protected-environment WIF boundary is the compensating control for this
release. A dedicated preview project or a protected IAM broker/foundation that
removes project-policy mutation from preview Terraform is the required target.
OCR writer access is
repository-level and limited to `artifact_registry.ocr_publish_repositories`
in `config/ocr.yaml`; project-wide or non-enumerated repository access fails.
The preview deploy identity currently receives
`roles/artifactregistry.repoAdmin` only on those same repositories for the
protected preview image path. That built-in role includes artifact deletion but
does not include repository IAM-policy access. The standalone foundation adds
the separate two-permission custom role described below; reducing the legacy
publication grant to `roles/artifactregistry.writer` requires an explicit live
IAM migration.
An existing backup identity may have only bootstrap viewer roles plus the
Terraform-managed Storage Transfer viewer and `scribeBackupRestoreVerifier`
custom role. Policy writes retain etags so concurrent IAM changes fail.

Create a protected `release` environment for the cross-repository
`HOMEBREW_REPO_GHAT` secret, restrict it to `main`, and require review before
the secret is released. The sole credentialed release path is
`github-release.yaml`, whose workflow definition always comes from protected
`main`.

An ordinary merged PR uses its `pull_request_target` close event. The workflow
explicitly checks out `refs/heads/main`, proves that the event's 40-character
merge SHA is reachable from `origin/main`, and detaches to that exact commit
before any release helper or GoReleaser input is used. A later main commit
cannot silently become that event's release target.

An exceptional release of a direct-main commit uses the typed
`repository_dispatch` event `release-main`. Its payload must contain the exact
current `main` SHA and the expected numeric tag. GitHub runs the workflow from
the default branch; the job additionally requires the payload SHA to equal
both `GITHUB_SHA` and fetched `origin/main`, and requires AutoTag's computed
version to equal the requested tag before it mutates a remote ref. There is no
arbitrary-ref `workflow_dispatch` or pushed-tag workflow. Pushing a numeric tag
therefore cannot execute the tagged tree or receive repository/Homebrew
credentials.

After the exact main deployment and required checks pass, dispatch a direct-main
release without creating the tag locally:

```bash
repo=lehigh-university-libraries/scribe
release_sha="$(gh api "repos/${repo}/git/ref/heads/main" --jq .object.sha)"
release_tag="${RELEASE_TAG:?set RELEASE_TAG to the next numeric version, for example 0.7.0}"

test "$release_sha" = "$(git rev-parse HEAD)"
jq -n --arg sha "$release_sha" --arg tag "$release_tag" \
  '{event_type:"release-main",client_payload:{release_sha:$sha,release_tag:$tag}}' |
  gh api --method POST "repos/${repo}/dispatches" --input -
```

Tags are bare numeric SemVer (`0.7.0`, not `v0.7.0`). Monitor the resulting
`Create release` run, then verify that the remote tag and published release both
resolve to `release_sha`.

Before creating or reusing any tag, the release job reads the `Terraform Apply`
run for that exact SHA. It accepts only a successful `main` push run whose
protected production deploy and reusable `lint-test` jobs all completed
successfully. Merged-PR release events can arrive before deployment finishes,
so this read-only gate waits for a bounded interval; a missing, failed, skipped,
or ambiguous run fails without mutating release state.

AutoTag v1.4.1 is downloaded by exact version and SHA-256. Tag creation is
retry-idempotent: a rerun reuses the one numeric tag already pointing at the
reviewed release source, verifies the remote ref has not moved, and supplies that
tag to GoReleaser through `GORELEASER_CURRENT_TAG`. Before GoReleaser runs, the
job verifies that remote tag ref as the source identity, then idempotently
creates or discovers one draft for the tag. GitHub may report the default
branch in `target_commitish` after selecting an existing tag, so that advisory
field is not treated as source evidence. GoReleaser reuses the draft, replaces
partial assets, and leaves it unpublished. Only after the GitHub and Homebrew
publishers succeed does the final step download the assets,
require Linux, Darwin, Windows, and checksum files, verify exact checksum
coverage and bytes, and publish the draft. A rerun of a fully published exact
release is a no-op; it does not rerun GoReleaser or Homebrew. Multiple tags on
the commit, a moved remote tag, duplicate drafts for one tag, a later checked-out
HEAD, a PR-head checkout, a broad dispatch, or a pushed-tag credential path
fail repository contracts.
The concurrency group serializes release jobs but does not assume GitHub will
start merged-PR events in merge order. Before tagging, an older event inspects
numeric tags on later `origin/main` descendants. A fully published exact
descendant release already contains that merge, so the older event exits as a
no-op; a missing or draft descendant release fails closed until its owning run
is successfully retried.

Restrict `HOMEBREW_REPO_GHAT` itself to the one external Homebrew repository.
GoReleaser uses the job's repository-scoped token for Scribe tags and release
assets and passes the external token only to the Homebrew publisher. Include
`skip-release` in a merged PR title only when intentionally suppressing that
release.

The `production` environment must also define `MONITORING_NOTIFICATION_CHANNELS`
as a non-empty JSON array of full Cloud Monitoring notification-channel names.
The externally managed `TF_STATE_BUCKET` must have object versioning plus at
least 14 days of retention or soft delete; every plan verifies that live policy.

Each environment must set `ALLOWED_IPS` to a non-empty JSON array of the
smallest office, VPN, or operator CIDRs that may power on and reach the site.
Terraform's default is an empty, fail-closed list.
Optional `ALLOWED_SSH_IPV4` and `ALLOWED_SSH_IPV6` variables are JSON arrays of
the exact administrator CIDRs permitted to reach the VM; an empty array keeps
that address family closed.

Set repository variables `SCRIBE_REGION` and `SCRIBE_ZONE` together (defaults
`us-east5` and `us-east5-b`). The workflow passes that exact pair to the
frontend build, Vault lookup, Terraform, and output lookup so the frontend
cannot be built against a VM DNS name in a different zone.

The managed GCP subnet defaults to `10.42.0.0/24` and is the only range from
which Traefik accepts forwarded identity. Inside Compose, only the fixed
Traefik address `172.30.0.2/32` is trusted by the API. Do not replace either
boundary with RFC1918, carrier-grade NAT, or link-local aggregate ranges. The
optional protected variables `NETWORK_IP_CIDR_RANGE` and
`COMPOSE_NETWORK_CIDR` must be changed together with an infrastructure review;
the deployment and drift workflows pass the same values to Terraform.

Each preview discovers the exact shared `vault-server-dev` Cloud Run service by
project, region, and fixed name, then binds its runtime identity to the
preview's app and VM service accounts with metadata-read and public-key-read
permissions. This direct lookup avoids coupling a fresh preview plan to stale
or partially upgraded root outputs in the owner workspace while still failing
closed when the shared service is absent. It allows Vault GCP IAM login without
granting key creation, deletion, or access to another workspace.
The shared dev Vault pre-creates one `scribe-preview-app` GCP role and an
identity-templated policy. The verified GCP alias email renders one exact
preview database read path, so preview Terraform never creates an auth role or
policy. The deploy CI token can stage and remove all `scribe-pr-*` bootstrap
paths; it is a shared protected orchestration identity, not a per-PR isolation
boundary. Required review, workflow/ref-restricted WIF, trusted workflow source,
and exact path validation are the trust boundary for cross-preview mutation.

Project-wide APIs, the internal Artifact Registry repository, and fixed-ID
custom roles are owned by the standalone `terraform/foundation` root under the
`scribe-foundation` backend prefix and default workspace. Protected delivery
plans or applies it before any credentialed image build or application
workspace. Because Terraform cannot manage project APIs while the Service
Usage API itself is disabled, the protected apply enables
`serviceusage.googleapis.com` after production WIF verification and before
foundation initialization. Terraform then owns both Service Usage and Service
Management with `disable_on_destroy = false` and
`deletion_policy = "ABANDON"`, so removing them from state leaves both APIs
enabled. The manual plan path does not perform that bootstrap or any other API
mutation. Dev, production, and previews only consume the foundation outputs.
The foundation also grants the protected preview deploy identity a custom role
containing only `artifactregistry.repositories.getIamPolicy` and
`artifactregistry.repositories.setIamPolicy`, and binds it only on the
reviewed `us/internal` repository. The built-in repository administrator role
does not contain those IAM-policy permissions, but Cloud Compose needs them to
converge each preview VM's repository-reader member. Keeping the two policy
permissions in a repository-scoped custom role avoids granting project-wide
Artifact Registry administration.

Vault uses separate runtime and initializer Google service accounts. Runtime
can access only the data bucket; initializer alone can access initialization
material. The protected deployment identity may read and KMS-decrypt the
stored root object solely for owner-workspace Terraform bootstrap/recovery.
The ordinary Vault CI policy is limited to preview bootstrap KV paths and
cannot administer mounts, policies, auth methods, or dev/production data.

Every successful Terraform apply must first attest that the latest created
Cloud Run revision is Ready, serves 100% of traffic, and contains the exact
reviewed frontend digest, then pass two managed readiness jobs. The
first runs the real server from the exact deployed frontend image with direct
VPC egress and polls its proxied `/healthz`. The second submits the deterministic
`SCRIBE TEST` image to the private image normalizer, segmentation, and Kraken
transcription endpoints, validates the JPEG and non-empty model outputs, and in
production requires the default Ollama OCR model to finish a non-streaming
image request. They use separate no-data probe identities so PR-built frontend
code never inherits the OCR probe's invoker grants. If Terraform partially
applies, or attestation/readiness fails after a production apply, the
workflow checks out the previously recorded reviewed commit, verifies that it
is an ancestor of the deploying `main` commit, and reapplies its persistence
generation, image digests, network/auth settings, and runtime limits. It then
reattests the restored frontend digest and both readiness jobs; a rollback
failure is reported distinctly. A nonzero apply can still have committed cloud
changes, so reaching the apply step is sufficient to trigger rollback. State
written before the complete source and
configuration record exists has no automatic rollback target and fails closed.

Rollback projects the recorded OCR image map onto the service keys in the
current reviewed model configuration. Retired service images are ignored, but
a missing current service image fails closed. Terraform supplies each route's
public model ID and baked filename from that same configuration, so both the
current image contract and the immediately preceding recorded image contract
remain probeable during a failed rollout.

Cloud deployments never use localhost as their public identity.
`PUBLIC_BASE_URL` uses the predictable Cloud Run URL
`https://<service>-<project-number>.<region>.run.app`. Direct `run.app`
ingress is the sole reviewed topology; adding a load balancer or custom domain
requires a new forwarding-depth design and acceptance tests before it can be
supported.

## Shared Vault owner bootstrap and recovery

The `dev` and `prod` workspaces own their environment's Vault. Before their
first full apply, provide both the human administrators and the protected
deploy identity that Terraform must keep in the CI login role:

```hcl
vault_admin_emails = ["operator@lehigh.edu"]
vault_ci_service_account_emails = ["github@example-project.iam.gserviceaccount.com"]
```

For a local run, the equivalent environment values are JSON arrays:

```bash
export VAULT_ADMIN_EMAILS='["operator@lehigh.edu"]'
export VAULT_CI_SERVICE_ACCOUNT_EMAILS='["github@example-project.iam.gserviceaccount.com"]'
make tf-dev BRANCH="$(git rev-parse HEAD)" ACTION=apply
```

On a clean owner workspace, `terraform/deploy-local.sh` first applies only the
Vault service shell, waits for initialization, obtains an authorized management
token, and then runs the complete apply that reconciles mounts, audit devices,
policies, and auth roles. The targeted step is bootstrap sequencing, not a
complete deployment; do not stop after it.

Normal local recovery uses the existing Google JWT admin role. If that role is
unavailable and the operator is explicitly authorized for stored-root
recovery, select the recovery mode for one run:

```bash
export VAULT_BOOTSTRAP_MODE=root-token
make tf-prod BRANCH="$(git rev-parse HEAD)" ACTION=apply
```

The helper defaults to the encrypted
`gs://<project>-vault-server-<dev|prod>-key/root-token.enc` object and the
matching `vault-server-<dev|prod>` KMS key ring, key `vault`, in `global`.
Nonstandard installations must explicitly set `VAULT_ROOT_TOKEN_OBJECT`,
`VAULT_ROOT_TOKEN_KMS_LOCATION`, `VAULT_ROOT_TOKEN_KMS_KEYRING`, and
`VAULT_ROOT_TOKEN_KMS_KEY`. The token is masked, kept only in the Terraform
process environment, and must not be persisted in tfvars, repository files, or
workflow artifacts.

If preview orchestration reports a missing `ci-...gserviceaccount-com` Vault
role, add the exact protected deploy service-account email to
`vault_ci_service_account_emails`, then reconcile the shared dev owner from an
authorized checkout:

```bash
make tf-dev-vault-ci-identities BRANCH="$(git rev-parse HEAD)" ACTION=apply
```

Preview workspaces never bootstrap Vault. They consume the already reconciled
shared `dev` URL, service-account identity, templated runtime role, and isolated
database-secret namespace.

## Persistence generations

`data_generation` is an immutable reviewed deployment input. The current
greenfield generation is `canonical-v1`. One value scopes all Compose volumes
(MariaDB, uploads, cache, and both Triplet stores), the GCS upload prefix, and
the transcription Pub/Sub topics and subscriptions. A process therefore cannot
combine a new canonical schema with blobs, publications, or queued work from an
older generation.

Changing the value is an explicit cutover, not a routine configuration edit.
Compose shutdown does not delete prior named volumes, and GCS retains the prior
prefix, so operators can inspect or recover them before separately approved
disposal. Terraform records the generation beside every image digest, Compose
SHA, and non-secret deployment configuration. Destroy, drift detection, and
rollback reload those exact values from state. Drift checks and rollback run
the recorded source commit after proving it is reviewed `main` history; they do
not interpret an old deployment through newer Terraform or repository scripts.
The first rollout from state that predates the complete record deliberately has
no automatic rollback target and fails closed instead of guessing which source,
configuration, or data belongs to the old application.

## Commands

Local Terraform defaults to plan:

```bash
make tf-dev ACTION=plan
make tf-preview PR=123 BRANCH=<40-character-base-sha> ACTION=plan
make tf-prod BRANCH=<40-character-production-sha> ACTION=plan
```

The project foundation is a separate root and must be reconciled first on a
clean project (shared applies belong in the protected workflow):

```bash
terraform -chdir=terraform/foundation init \
  -backend-config="bucket=${TF_STATE_BUCKET}" \
  -backend-config="prefix=scribe-foundation"
terraform -chdir=terraform/foundation plan -var="project_id=${GCLOUD_PROJECT}"
```

Use GitHub Actions for shared apply operations. Scheduled drift detection uses
the currently deployed source, configuration, generation, and digest set and
opens an issue on failure. Production and
preview local plans also verify the state-bucket recovery policy before reading
or mutating state. Root-module output preconditions make invalid release inputs
a plan error—not merely a warning—including placeholder or foreign images,
incomplete OCR image maps, an empty ingress allowlist, missing production
backup/monitoring controls, and incomplete Vault owner identities.
A manual protected production `mode=plan` reuses current recorded image
digests. It does not build or publish an image, promote a registry tag, or
apply either Terraform root.

### State-lock recovery

If Terraform is interrupted while holding a state lock, first inspect the
active GitHub workflow and every authorized local process. Only after proving
that no Terraform process still owns the operation may an operator recover the
reported lock:

```bash
terraform -chdir=terraform force-unlock <LOCK_ID>
make tf-prod BRANCH="$(git rev-parse HEAD)" ACTION=plan
```

Review the resulting plan before any apply. Never force-unlock merely because
a deployment is slow; doing so can create concurrent state writers.

Containers use `restart: unless-stopped`, readiness-aware dependencies, bounded
startup probes, and graceful stop periods. Compose also waits for Triplet's
image-supplied `/healthz` probe before starting its dependants. `/livez` reports
process liveness; `/readyz` and `/healthz` require persistence readiness. The
API and worker load the managed service-account credential and mint an identity
token for every configured outbound segmentation, Kraken, and Ollama audience
before either process begins listening. Runtime calls reuse that same
per-audience cache. Container access to GCE metadata remains blocked by the
host firewall; an invalid credential or failed token exchange therefore fails
startup and the ordinary Compose/backend readiness path instead of becoming a
request-time upload failure. Inbound JWT issuer audiences are never minted by
this preflight.

The Cloud Compose boot disk is named from the complete rendered cloud-init digest,
so a reviewed Compose SHA, backend image, or runtime configuration change
replaces the VM boot disk and reruns bootstrap while retaining the stable data
and Docker-volume disks. The post-apply backend readiness job also requires the
live API to report the exact digest-pinned image requested by Terraform; an old
but otherwise healthy container fails deployment. The
API, worker, Triplet, and segmentor all handle `SIGTERM`; HTTP services
stop accepting new requests and receive a bounded ten-second drain period.
Application, worker, OCR, frontend, and Triplet containers run with all Linux
capabilities dropped, privilege escalation disabled, read-only root filesystems,
and bounded in-memory scratch filesystems; only declared data/cache volumes are
writable.

Cloud Compose's pinned GCP release selects the COS image for every preview and
production VM. Upgrade COS only by reviewing and pinning a Cloud Compose release
that changes that image. Terraform then replaces the boot disk and VM while
reattaching the stable data and Docker-volume disks; Scribe does not maintain a
second host-operating-system path.

Production and development use the reviewed N4/Hyperdisk Balanced profile.
Ephemeral pull-request previews still run COS, but use `e2-medium` with Standard
Persistent Disk. That profile keeps preview disks out of the finite production
Hyperdisk capacity pool while preserving the same Cloud Compose bootstrap,
separate data and Docker-volume disks, and teardown path.

Production defaults to `us-east5-b`; pull-request previews use the separate
`us-east5-c` placement default so preview capacity does not contend with the
production VM's zone. Set the protected repository variable
`SCRIBE_PREVIEW_ZONE` to another E2-capable zone in the configured region when
preview placement must change. Local preview commands use the same `us-east5-c`
default and accept `SCRIBE_ZONE` as an explicit override. Changing a preview's zone replaces its
three ephemeral zonal disks. Refresh and destroy always replay the region and
zone recorded in that workspace's deployment inputs instead of guessing from
the current default.

The Compose checkout is workspace-stable at
`/mnt/disks/data/scribe/<workspace>`, even though every deployment fetches,
detaches to, and verifies a different immutable commit. Do not run `git pull`
or the obsolete `/mnt/disks/data/{init,up,down}` helpers on a managed VM: those
legacy files are inert under Cloud Compose 1.7, and a mutable branch merge
would bypass the SHA recorded by Terraform. Use `cloud-compose.service` and
the current `/home/cloud-compose/{init,up,down}` dispatchers when diagnosing
the managed lifecycle.

Cloud Compose converges the exact manifest checkout and an existing `.env` as
root before unprivileged source preparation or application initialization. It
repairs metadata left by a replaced VM without recursively changing checkout
contents, and rejects symbolic links, hard-linked environment files, or paths
outside the persistent data boundary. This makes an interrupted bootstrap
repeatable while keeping root traversal out of application-controlled paths.

Before each start, Scribe renders and validates the checked-out Compose file
with a Terraform-owned network overlay. The overlay keeps the fixed IPAM
invariant available even when automatic rollback checks out an older
application commit. Secret generation creates any missing declared bind
files, and every required service image is pulled while the previous
containers remain available.

The runtime preflight leaves a healthy current project and compatible network
in place for Compose's normal update path. If the exact current project has
duplicated or missing services, retired or multiple project networks, stale
service state, incompatible IPAM, or a fixed-address collision, it removes only
containers and networks carrying that exact project label before Compose starts
them again. It refuses to alter a canonical-name network or network endpoint
owned outside that label. Named volumes and unrelated Docker resources are
always outside the recovery boundary, so repeating the lifecycle converges
without changing volume identity.

For exact COS boot, Compose, network, and managed-readiness diagnostic and
recovery commands, use the bounded
[production troubleshooting](troubleshooting.md) runbook.
