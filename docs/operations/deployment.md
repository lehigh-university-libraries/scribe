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

Terraform receives the exact reviewed Git commit as the Compose source ref.
Backend and frontend images come directly from build-produced immutable
digests; OCR images are either built for that commit or carried forward by
digest after the protected workflow proves that no image-content input changed.
PR code receives no CI credentials while it is tested and built. After
environment approval, that image runs only as the preview ingress identity,
whose sole workload permission
is power control over that preview VM. The job that executes a PR-built image
for backend probing uses a dedicated no-data identity with no Vault, bucket,
Pub/Sub, OCR-invoker, or project grants. The OCR deep probe has a separate
no-data identity whose only workload grants invoke the exact segmentation and
transcription services under test. Never grant preview identities production resources. Preview
infrastructure and shared helpers run from a SHA resolved independently from
protected `main`, including teardown after a PR is retargeted. Preview deploys
read the digest-pinned OCR map recorded by the production deployment of the
exact protected-base commit, so PRs do not rebuild or resolve mutable
model-image tags.

Before a production apply publishes OCR images, the protected deploy identity
reads the last successful production `deployment_inputs` record. The workflow
requires that record to belong to the configured project and to name an
ancestor of the new `main` commit, then compares the commits using the sorted
pathspecs from `ci/ocr-source-paths.sh`. Those pathspecs cover the segmentor
Docker context and build inputs, the OCR matrix and image-map generators, the
complete Ollama image context, and the in-module transitive Go dependency
closure of `cmd/segmentor` under the same Linux, amd64, CGO, and `localocr`
build constraints used by the image.

Any missing or malformed deployment record, state-read failure, history gap,
non-ancestor commit, dependency-discovery failure, or diff ambiguity selects a
full rebuild. When any watched input changed, the protected OCR publisher
rebuilds and republishes the complete configured matrix under the new commit
SHA. Only a certain no-change result may skip that job. In that case,
`ci/select-current-ocr-images.sh` first proves the recorded digest map covers
the current required service-key matrix, then Terraform receives that validated
map directly. Reuse does not relax the build invariant that `image_tag` equals
`checkout_ref`: no replacement tag is invented, and the prior reviewed image
digests retain their original provenance.

The preview resolver uses its protected deploy identity to read the same stable
production record while serialized with production deployment. It requires the
recorded Compose SHA to equal the current protected `main` SHA and projects out
production-only Ollama services before applying a `pr-*` workspace. A preview
therefore waits for production to finish deploying a new base commit instead
of guessing an OCR tag or observing transient forward/rollback state.

Deterministic workflow decisions live in the typed `cmd/deployer` helper. Its
preview resolver fetches the privileged checkout from protected `main`, treats
event base data only as a lifecycle signal, validates every value written to
`GITHUB_OUTPUT`, and rejects forked manual dispatches. The same helper owns the
published plan/apply/readiness/rollback status precedence. The
`ci/resolve-preview-inputs.sh` and `ci/deployment-status.sh` files are thin
entrypoints so workflows and operators use the unit-tested Go contract rather
than separate shell implementations.

Preview ingress is already restricted by the protected `ALLOWED_IPS` policy,
so preview application auth deliberately uses its isolated anonymous workspace
instead of a reusable Google OAuth client. `AUTH_PREVIEW_ANONYMOUS` is accepted
only when `VAULT_WORKSPACE=pr-<number>` and the public origin is the matching
HTTPS `scribe-pr-<number>-*.run.app` service; production and dev fail startup if
it is enabled. Protected orchestration creates only a random database password
under `scribe/previews/scribe-pr-<number>@<project>.iam.gserviceaccount.com`.
The API and worker require that exact service-account namespace and, in preview
mode, read only its database bootstrap path; they do not request OAuth or
provider credentials that the preview Vault policy intentionally withholds.
Pull-request code receives no dev or production
OAuth, database, or provider credential. Reapply preserves the password. A
preview destroy is successful only after it recursively removes its Vault
namespace; transient Vault HTTP 5xx and transport failures receive four bounded
attempts, while authorization and other non-transient responses fail
immediately without exposing response bodies.

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
inputs could mutate the plan before teardown. Four additive schema transitions
have unambiguous historical values: an absent
`dev_external_ocr_impersonators` becomes `[]`, an absent
`browser_readiness_image` becomes the pre-runner empty value, and an absent
`browser_readiness_subnet_cidr` becomes the reviewed `10.43.0.0/26` default.
An absent `preview_machine_type` becomes the former `e2-medium` preview
default; this dedicated variable is structurally ignored by production and
development workspaces.
Explicit null or malformed values still fail closed; non-empty browser images
remain governed by the exact preview/production image contract. Terraform
destroy makes at most three bounded attempts with the same validated
state-derived inputs for ordinary failures, so short-lived
cloud dependency cleanup can converge without rereading partially changed
state. The exact preview-only Google-managed subnet delay described below gets
a separate two-hour bound. After either bound, a later protected run or operator
recovery is required. The workspace is deleted only after a successful destroy.

A preview that uses Cloud Run Direct VPC can fail after its services are gone
with `resourceInUseByAnotherResource` and an
`/addresses/serverless-ipv4-...` or `/addresses/serverless-ipv6-...`
reservation still holding the preview subnet.
This is a Google-managed cleanup delay, not permission to edit Terraform state
or delete the reservation: Google documents a **one-to-two-hour wait** after
removing the Cloud Run resources before deleting the subnet. When Terraform's
preview-destroy diagnostic contains either exact managed `serverless-ipv4-*`
or `serverless-ipv6-*` address and `resourceInUseByAnotherResource`, the
protected job retries every five minutes for up to two hours using the same
already-validated deployment inputs. Every Terraform error in that attempt
must identify either the exact current preview application subnet, its exact
deterministic legacy browser subnet, or its exact deterministic dual-stack
browser subnet, plus a managed address; mixed or differently scoped failures
retain the ordinary bound. The wait retains the shared preview serialization
lock and the job-scoped deploy identity so no other preview can replace shared runtime
bindings during a partial teardown. Its preview-only timeout leaves a one-hour
execution and cleanup margin beyond the two-hour sleep budget; other deploy
modes retain their shorter timeout. After Terraform succeeds, the workflow
mints new short-lived Vault proxy, identity, and client tokens before removing
the preview namespace, rather than reusing credentials created before the wait.
Other errors retain the ordinary three-attempt bound. If Google still has not
released the address after the longer bound, let the run fail closed and rerun
**Terraform Preview** from protected `main` later. Use ordinary `destroy` while
the current state still has a readable `deployment_inputs` output. If ordinary
destroy instead reports that the output is absent, use `recover-destroy` as
described below. Never manually delete a `serverless-ipv4-*` or
`serverless-ipv6-*` reservation. See
Google's
[Direct VPC subnet deletion guidance](https://cloud.google.com/run/docs/configuring/vpc-direct-vpc#ip-address-allocation).

An interrupted or older teardown can remove `deployment_inputs` while leaving
other resources in the current state. In that narrow case, dispatch **Terraform
Preview** from protected `main` with the PR number and action
`recover-destroy`. The job reads, but never restores, the exact
`scribe/pr-<number>.tfstate` object history. It accepts only the newest lower
state serial with the same non-empty lineage and a fully valid
`deployment_inputs` value, then destroys the resources still present in the
current state. Ambiguous history, a current output, a different lineage, or no
valid prior value fails closed without writing state. Use ordinary `destroy`
for every normal teardown; `recover-destroy` exists only for this partial-state
condition.

If Terraform already deleted the exact `pr-<number>` workspace but a later
Vault request failed, rerun `recover-destroy`. Only this protected recovery mode
may treat that workspace as already destroyed, and only after a successful
workspace inventory proves the exact name is absent. An inventory failure or a
workspace that is listed but cannot be selected still fails closed. The rerun
then idempotently removes the preview Vault namespace and repairs deployment
evidence. Success requires all three outcomes: Terraform workspace absent,
Vault namespace removed, and GitHub preview-deployment evidence inactive.

When a Terraform upgrade adds `moved` blocks, normalize the selected
workspace's resource addresses before any targeted maintenance:

```bash
make tf-prod ACTION=refresh
# or: make tf-preview PR=75 ACTION=refresh
```

Refresh validates and replays every field in the `deployment_inputs` schema
recorded by that workspace, including its Compose SHA, data generation, runtime
image digests, network/admission settings, monitoring channels, and Vault
identities. The same missing legacy external-OCR impersonator key is normalized
to `[]`; every other field must match the complete schema, and caller values for
recorded fields are ignored. The wrapper saves a full-graph
`terraform plan -refresh-only`, permits only no-op
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

### Managed preview browser readiness

Every protected preview apply checks out the exact protected base SHA for its
workflow, source-preparation helper, Dockerfile, package manifests, and locked
dependencies. Before cloud authentication, that helper requests only
the exact commit, the non-recursive Git trees leading to
`web/e2e/deployed-readiness.mjs`, and that fixed Contents path at the already
resolved same-repository PR-head SHA. Every response is bounded. The helper
requires `web/` and `e2e/`
to be unique `040000` tree entries and the source to be one `100644` blob, then
requires the Contents API payload's path, base64 encoding, declared size, and
Git blob SHA to match that tree entry. Symlinks, gitlinks, duplicate entries,
truncated trees, and mismatched payloads fail closed before staging. The
credentialed Docker build copies the script into the final image but never
executes it; the protected Dockerfile installs only protected-base dependencies
before that copy. The exact script embeds the reviewed two-line upload fixture
and its SHA-256 digest; no separate PR-head fixture is staged into the protected
build or Terraform. The image tag is the source SHA and Terraform records its
resolved digest.

Because the credentialed apply deliberately executes only protected-base
Terraform, a pull request that depends on preview infrastructure changes must
land those changes first as a narrow reviewed prerequisite. Rebase the dependent
pull request onto that protected commit before running its preview. Do not stage
or execute pull-request Terraform with cloud credentials to collapse the two
reviews into one run.

The PR-head script runs only later in the preview-only Playwright job. The
runtime and browser are digest-pinned, run as `pwuser`, and receive only the
preview's canonical HTTPS origin. A dedicated Cloud Run job uses a dedicated
service account with no project, storage, Vault, Pub/Sub, or OCR IAM grants.
Direct VPC egress attaches only this job to a dedicated, non-overlapping preview
`/26` in an independent custom VPC. The subnet is dual stack and receives one
external IPv6 `/64`; Terraform appends only that dedicated `/64` to this
preview's PPB ingress allowlist. The standalone project foundation owns the
documented `roles/compute.publicIpAdmin` grant for Google's Cloud Run service
agent. It is never recreated by a preview workspace, and no human, deploy, or
application identity receives that role. Cloud NAT and a reserved regional
IPv4 address cover the same subnet only for fixed public DNS and reviewed
IPv4-only fixture origins. Public Cloud NAT automatically uses Private Google
Access rather than translating
`run.app`, so the exact-head runner must force the canonical preview host over
AAAA and fail closed if that path is unavailable. The protected routing helper
accepts at most 32 public-global AAAA answers, maps only the exact canonical
host in Chromium, and disables Node's IPv4 family race for Playwright API
requests. The VM, backend probe, OCR
probe, application subnet, production, and other previews cannot use the
dedicated `/64`. The original application-VPC browser subnet, address, router,
and NAT stay state-managed during this additive migration to avoid blocking an
apply on delayed Direct VPC address release; the job is detached from that
subnet and its former `/32` is no longer allowlisted.

The job authenticates through the existing isolated
`AUTH_PREVIEW_ANONYMOUS` workspace. It leaves context selection on `Default`
and proves the durable job resolved a concrete context, uploads the reviewed
two-line image fixture through the frontend retry path, and intentionally holds
the editor bundle until the exact durable job has completed. The loaded editor
must catch up from that completed job and its canonical revision: the visible
magic-wand badge must move between two distinct line annotations in increasing
order, and both lines must emit matched start and result events for the exact
job and successful attempt. The runner then reloads an unpinned editor, waits
for its overlay and event bridge to mount, and enqueues a distinct durable job.
It waits for the item-scoped SSE response before enqueueing so no task event can
race ahead of the new stream's outbox high-water mark. That in-flight path must
emit live (not reconstructed catch-up) events and move the visible wand through
both lines before completion. The editor does not
publish its terminal available state or re-enable
foreground retranscription until Mirador acknowledges the exact correlated
canonical reload; mismatched, failed, or timed-out reloads remain blocked and
require a page reload. The runner also proves automatic transcription made no
foreground `EnrichAnnotation` request, the durable job completed with zero failed
segments, and every canonical line contains text. It then
exercises overlays, retranscription, draw and centered-line creation,
undo/redo, inline text, word/line split and join adapters, deletion, save,
publish, responsive bottom-pane layout, both home/sidebar trash actions, and
the copy-once workspace-token modal. The editor loads once and resizes in place
through 360x800, 667x375, 768x1024, and 1440x900; every viewport must keep all
14 primary actions in view without scrolling, retain a usable OpenSeadragon
image height, and preserve the saved canonical revision and page. A second
golden path imports the reviewed six-page Lehigh manifest without reprocessing.
It requires the mounted Scribe
panel, a successful bounded nonempty image response rendered by OpenSeadragon,
and exact agreement among the active Canvas, item-image, and editor URL. The
runner validates both the canonical and exact public Presentation
AnnotationPage, turns to the second Canvas with Mirador's real UI, repeats the
identity/image/page checks, cycles every overlay mode, and proves an editor
action remains usable.

Retryable `UploadItemImage` responses do not independently fail the browser's
global network sentinel. The runner requires one to three attempts, permits
only retryable Connect/transport outcomes before the last attempt, validates
the last response's item/image/job identities, and requires the exact editor
handoff before any terminal upload result. The manifest path observes exactly
one import request, no reprocess request, and six positive unique image IDs.
Expected image evidence is capped at 100 responses, rejects empty or
larger-than-64-MiB bodies, and shares the bounded stage timeout. Its two local
publications are removed with the exact disposable manifest item during normal
cleanup.

Before teardown, the runner returns home, deletes the upload through the
homepage and the manifest item through the sidebar, accepts only the exact
item-ID confirmation dialog, and requires both rendered library copies to
disappear after each deletion. Cleanup then closes the page to stop future
browser retries and uses the retained session only for direct Connect
reconciliation. It records the latest observed upload, manifest-import, and
token-creation request plus response-settled and validated state. Any uncertain
operation is polled through at least the 300-second request commit horizon
before stable absence can pass. Uploads are matched by unique exact name;
manifest discovery scans a capped, empty-query workspace inventory and loads
candidates before matching the exact source type and URL; workspace tokens are
listed and matched by their unique exact name. Same-origin failures outside
that bounded upload retry path, failed requests, CSP console violations,
unexpected native dialogs, or failed cleanup make readiness fail. Cloud
diagnostics accept only these exact browser categories: `home`, `context`,
`upload`, `handoff`,
`transcription`, `annotations`, `editor`, `overlay`, `structure`, `manifest`,
`retranscribe`, `save`, `publish`, `responsive`, `token`, `cleanup`, `network`,
and `csp`; free-form or suffixed messages are discarded. The runner stores no
browser state and uploads no browser output.

A 30-minute scenario deadline measured from process startup closes the browser
page and enters normal cleanup before the platform boundary. The Cloud Run task
has a 40-minute timeout and reserves its final 10 minutes for deadline-aware
reconciliation: 300 seconds for a late mutation to commit, a 180-second
recovery tail, and bounded request/control overhead. The protected 120-minute
deploy workflow covers the sequential 300-second backend, 1,800-second OCR,
and 2,400-second browser job bounds plus 1,800 seconds of control-plane
headroom.

Terraform records the exact runner digest and dedicated subnet in
`deployment_inputs`, so
refresh and destroy replay the same graph. The protected apply workflow always
supplies a non-empty digest and requires the resulting browser job to pass
before preview success. Terraform permits an empty runner value only so
non-preview and pre-runner lifecycle state can be planned or destroyed;
historical absence normalizes to that empty value. Explicit null, mutable,
cross-project, or placeholder values fail closed.

### Trusted browser smoke session

Production browser readiness must not depend on an operator's Google OAuth
session. The reviewed backend image includes `/app/scribe-browser-session` for
an operator or protected deploy job with administrator SSH access to the Cloud
Compose VM. Run it inside the API container with one new, direct child of
`/tmp`; the basename must start with `scribe-browser-session-` and end with
`.json`:

```bash
docker compose exec -T api /app/scribe-browser-session \
  --output "/tmp/scribe-browser-session-${GITHUB_RUN_ID}-${GITHUB_RUN_ATTEMPT}.json"
```

The command emits no credential or success message. It verifies that reserved
user 1 is not a system administrator or OAuth identity, owns only reserved
personal workspace 1, and is that workspace's administrator. It then creates
an ordinary session with a fixed five-minute lifetime and writes a
Playwright-compatible storage-state document to a new mode-`0600` file in the
API container's private `/tmp` directory. There are no user, workspace, role, or
lifetime override flags. Any drift in the reserved identity, an existing
output file, a non-HTTPS public origin, or a cookie-domain mismatch fails
closed.

The protected SSH step must copy the file into a job-private mode-`0700`
directory without printing its contents, remove the container copy
immediately, load it directly through Playwright's `storageState` option, and
remove the host copy as soon as the browser context is created. On successful
readiness, POST `/logout` from that context to revoke the database session;
abandoned sessions expire after five minutes. Never put the file contents in a
shell command substitution, process argument, URL, GitHub output/environment
file, log, trace, screenshot, video, or artifact.

This helper deliberately relies on the existing host-administrator trust
boundary: anyone able to execute commands in the API container can already
reach its database and Vault bootstrap material. Do not expose the binary
through an HTTP route or copy it into the public frontend image. Preview auth
continues to use its isolated `AUTH_PREVIEW_ANONYMOUS` principal and does not
need this credential.

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
The resolver uses the independently derived project-number URL for requests but
preserves Cloud Run's validated reported service URI as the Google ID-token
audience owned by the Terraform-managed Vault JWT role.
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
The foundation also owns the one project-wide
`roles/compute.publicIpAdmin` binding that Google requires for its managed
Cloud Run service agent to use external-IPv6 Direct VPC subnets. Application
workspaces do not manage that singleton binding. The foundation also grants
the protected preview deploy identity a custom role
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
reviewed frontend digest, then pass two common managed readiness jobs. The
first runs the real server from the exact deployed frontend image with direct
VPC egress and polls its proxied `/healthz`. The second submits the deterministic
`SCRIBE TEST` image to the private image normalizer, segmentation, and Kraken
transcription endpoints, validates the JPEG and non-empty model outputs, and in
production requires the default Ollama OCR model to finish a non-streaming
image request. Protected preview applies then pass the separate browser job
described above. All three use separate no-data probe identities so PR-built
frontend code never inherits the OCR probe's invoker grants. If Terraform
partially applies, or attestation/readiness fails after a production apply, the
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

Before first production use or any canonical-host cutover, register
`https://<service>-<project-number>.<region>.run.app/auth/callback/google` as an
authorized redirect URI on the external Google OAuth client stored in Vault.
Keep the former callback URI registered during the rollback window, verify a
complete sign-in on the deterministic hostname, and only then remove the old
URI. Terraform cannot inspect that external client allowlist, so this is a
required operator precondition rather than an inferred deployment success.

During the one-time cutover, the new frontend accepts the immediately previous
API readiness payload without canonical redirects so independently replaced
Cloud Run and VM revisions do not create an outage. The protected post-apply
readiness job always requires the reviewed API digest and an exact HTTPS
`public_origin`. Protected pull-request previews run trusted orchestration from
`main`, so the one preview that introduces the expected-origin input can only
validate that shape; after the orchestration is merged, the job also requires
exact equality with Terraform's deterministic origin. Deployment success
cannot be reported while the backend is in the old no-origin compatibility
state. Browser tabs already running JavaScript on the former hostname cannot
be relocated by server-side redirects; announce a reload and, if needed,
re-login on the deterministic hostname after the cutover. Fresh page
navigations and bookmarks receive a non-cacheable permanent redirect
automatically.

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

Every protected preview deploy first checks out the immutable current `main`
base and runs the same narrow entry point available to an authorized operator:

```bash
export VAULT_ADMIN_EMAILS='["operator@lehigh.edu"]'
export VAULT_CI_SERVICE_ACCOUNT_EMAILS='["github@example-project.iam.gserviceaccount.com"]'
export VAULT_BOOTSTRAP_MODE=root-token
make tf-dev-vault-preview-runtime BRANCH="$(git rev-parse main)" ACTION=apply
```

The reusable trusted deploy job holds one shared preview lock from this
reconciliation through Terraform apply and readiness verification, so another
preview cannot replace the shared role between reconciliation and use. Pending
preview deploys queue behind that lock instead of replacing one another. The
CI Vault token intentionally cannot administer policies or auth roles, so the
protected job uses the existing encrypted root-token recovery channel. The
shared Make entry point independently resolves the configured project number
and the exact live `vault-server-dev` service, verifies its runtime identity,
and runs a typed Go reconciler instead of refreshing the Terraform owner graph.
The reconciler binds the Vault origin to that project number and requires the
live `gcp/` auth backend to retain its `unique_id` alias and exact
`service_account_email` metadata contract. It renders the verified backend
accessor into the one-path identity policy, idempotently writes only the exact
`scribe-preview-app` policy and role endpoints, and reads both back before
reporting success. Backend configuration drift is reported for the normal dev
Terraform owner to repair; this narrow operation never mutates it. `ACTION=plan`
performs the same exact comparison without writing; `ACTION=apply` converges
policy or role drift. The normal dev Terraform configuration remains the
declarative owner and retains its initialization and reverse-destroy dependency
on the Vault module. One shared project-bound resolver also supplies the later
Vault login and secret steps. Owner maintenance still accepts runtime
service-account drift at discovery time so the full Terraform graph can repair
it; preview reconciliation and credential use require the exact expected
identity.
The reconciler's sole terminal success diagnostic is
`[preview-vault-runtime] policy=true role=true`; tokens, configuration values,
and response bodies are never printed. The surrounding deployment path retains
its normal redacted progress diagnostics. Pull-request code never runs with
this credential.

## Persistence generations

`data_generation` is an immutable reviewed deployment input. The current
application generation is `canonical-v2`. One value scopes all Compose volumes
(MariaDB, uploads, cache, and both Triplet stores), the GCS upload prefix, and
the transcription Pub/Sub topics and subscriptions. A process therefore cannot
combine a new canonical schema with blobs, publications, or queued work from an
older generation.

Changing the value is an explicit cutover, not a routine configuration edit.
The `canonical-v2` cutover isolates the released `0002` schema from the
`canonical-v1` database used by the previously deployed binary; it does not
copy application data between generations. The protected workflow captures the
complete `canonical-v1` deployment record before applying `canonical-v2`. If
that first apply or its readiness checks fail, automatic rollback exports the
recorded `data_generation` and therefore restarts the prior binary against its
unchanged `canonical-v1` volumes. After a successful cutover, subsequent
deployments record and roll back within `canonical-v2`.

Compose shutdown does not delete prior named volumes, and GCS retains the prior
prefix, so operators can inspect or recover them before separately approved
disposal. Terraform also leaves the `canonical-v1` Pub/Sub topics,
subscriptions, IAM grants, and alert-policy addresses intact while creating the
`canonical-v2` queue graph alongside them. Messages on the retained
`canonical-v1` worker subscription keep their seven-day service lifetime, and
its dead-letter monitor messages keep their fourteen-day lifetime; rollback
does not reset either clock. A rollback removes the failed forward graph, so
messages created only in `canonical-v2` are not retained. The application and
Pub/Sub service agents retain grants on both graphs while both are retained so
the prior graph remains immediately runnable; runtime configuration names only
the selected generation. Later canonical cutovers must append their generation
to Terraform's explicit ordered review list. The forward graph is sliced from
v2 through the selected entry, which guarantees that the immediately prior
graph remains available and prevents an unreviewed generation value from
causing resource fan-out.

Terraform records the generation beside every image digest, Compose SHA, and
non-secret deployment configuration. Destroy, drift detection, and rollback
reload those exact values from state. Drift checks run the recorded source
commit. Rollback first proves that source is reviewed `main` history, then uses
the current infrastructure safety code to replay its recorded application
artifacts, generation, and configuration. Returning to `canonical-v1` removes
the parallel forward queue graph and leaves the original unindexed Terraform
addresses intact, so the recorded prior source can still evaluate drift. It
never executes retired infrastructure scripts from the prior checkout.
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
Ephemeral pull-request previews still run COS, but default to
`n2d-standard-2` with Standard Persistent Disk. That profile keeps preview
disks out of the finite production Hyperdisk capacity pool while preserving
the same Cloud Compose bootstrap, separate data and Docker-volume disks, and
teardown path.

Production defaults to `us-east5-b`; pull-request previews use the separate
`us-east5-c` placement default so preview capacity does not contend with the
production VM's zone. Set the protected repository variable
`SCRIBE_PREVIEW_ZONE` to another zone in the configured region that supports
the selected reviewed machine profile when preview placement must change. Set
the protected repository variable
`SCRIBE_PREVIEW_MACHINE_TYPE` to `n2d-standard-2` or the reviewed fallback
`e2-medium` when preview compute capacity must change. Protected orchestration
reads that setting once before any credentialed job, validates the exact
allowlist, and freezes it for both apply and teardown; pull-request and manual
dispatch input cannot select a profile. Local preview commands use the same
`us-east5-c` and `n2d-standard-2` defaults and accept `SCRIBE_ZONE` and
`SCRIBE_PREVIEW_MACHINE_TYPE` as explicit overrides. Changing a preview's zone
replaces its three ephemeral zonal disks; changing its machine profile performs
the provider-managed stop/update/start while retaining its persistent disks.
Refresh and destroy always replay the region, zone, and preview machine profile
recorded in that workspace's deployment inputs instead of guessing from the
current defaults.

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
