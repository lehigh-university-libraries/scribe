# Scribe Terraform

This directory deploys Scribe to GCP through the pinned
[`libops/cloud-compose`](https://github.com/libops/cloud-compose) module. The
root owns the private VM/Compose application boundary, Cloud Run ingress and
frontend sidecar, image normalization and OCR services, uploads and backup
storage, Pub/Sub processing resources, monitoring, and the environment-owned
Vault service. Project-wide APIs, Artifact Registry, and fixed custom roles
belong to the separate `terraform/foundation` root.

For the full delivery and trust model, see the
[deployment guide](../docs/operations/deployment.md). Recovery, monitoring,
and runtime configuration live in the Zensical documentation:

- [configuration](../docs/operations/configuration.md)
- [observability](../docs/operations/observability.md)
- [backup and restore](../docs/operations/backup-restore.md)
- [job recovery](../docs/operations/job-recovery.md)
- [quality gates](../docs/reference/quality-gates.md)

## Prerequisites

Use the repository-pinned toolchain. `make terraform-check` runs formatting,
module contracts, initialization, and validation with the required Terraform
version. Local deployment also requires `gcloud`, Docker with Buildx, `git`,
`curl`, and `jq`.

Before a local plan or apply:

1. authenticate `gcloud` to the target project;
2. configure Docker for `us-docker.pkg.dev`;
3. provide an external GCS state bucket with versioning and at least 14 days of
   retention or soft delete;
4. apply the standalone foundation through the protected workflow on a new
   project; and
5. copy `terraform.tfvars.example` to the ignored `terraform.tfvars`, or supply
   equivalent `TF_VAR_*` and deploy-helper environment values.

Runtime quota, storage, and IIIF defaults are intentionally absent from the
example tfvars file. `../config.yaml` owns those defaults; use an explicit
`TF_VAR_*` only for a reviewed deployment-specific override.

```bash
export GCLOUD_PROJECT=your-gcp-project-id
export TF_STATE_BUCKET="${GCLOUD_PROJECT}-terraform"
gcloud auth login
gcloud config set project "$GCLOUD_PROJECT"
gcloud auth configure-docker us-docker.pkg.dev
```

The local helper verifies the state-bucket recovery policy before every plan or
apply. It resolves mutable local image tags to digests; an apply can build a
missing frontend or OCR GAR image, while a plan stays read-only and fails with
the missing image reference.

## State and workspaces

| Environment | Workspace | Site | Source ref | Shared services |
| --- | --- | --- | --- | --- |
| Development | `dev` | `scribe-dev` | supplied branch/ref | owns shared dev Vault |
| Production | `prod` | `scribe` | immutable 40-character SHA | owns production Vault and shared Ollama |
| Preview | `pr-<number>` | `scribe-pr-<number>` | immutable protected base SHA | consumes dev Vault and production Ollama state |

Application workspaces read singleton foundation state and never recreate its
resources. That foundation also binds Google's managed Cloud Run service agent
to `roles/compute.publicIpAdmin`, which external-IPv6 Direct VPC subnets
require; per-preview states and application identities never own that
project-wide grant. Initialize the foundation state with the default workspace
and isolated prefix:

The protected foundation apply enables `serviceusage.googleapis.com` after
verifying the production WIF boundary and before Terraform initialization.
This is the irreducible API bootstrap that lets Terraform own both Service
Usage and Service Management with `disable_on_destroy = false` and
`deletion_policy = "ABANDON"`, so removing them from state leaves the APIs
enabled. A manual foundation plan remains read-only and never enables an API.

```bash
terraform -chdir=terraform/foundation init \
  -backend-config="bucket=${TF_STATE_BUCKET}" \
  -backend-config="prefix=scribe-foundation"
terraform -chdir=terraform/foundation plan -var="project_id=${GCLOUD_PROJECT}"
# Shared foundation apply belongs in the protected Terraform Apply workflow.
```

## Entry points

The Make targets call [deploy-local.sh](deploy-local.sh), select the correct
workspace, and pass dynamic values as explicit `terraform -var` arguments so
they override local tfvars. They default to `ACTION=plan`.

```bash
# Shared development
make tf-dev BRANCH="$(git rev-parse HEAD)" ACTION=plan
make tf-dev BRANCH="$(git rev-parse HEAD)" ACTION=apply

# Production
production_sha="$(git rev-parse HEAD)"
make tf-prod BRANCH="$production_sha" ACTION=plan
make tf-prod BRANCH="$production_sha" ACTION=apply

# A local preview matching protected preview naming
preview_base_sha="$(git rev-parse main)"
make tf-preview PR=23 BRANCH="$preview_base_sha" ACTION=plan
make tf-preview PR=23 BRANCH="$preview_base_sha" ACTION=apply

# Normalize moved resource addresses from the selected workspace before maintenance.
# Refresh replays the workspace's recorded immutable release and configuration inputs;
# BRANCH and SCRIBE_* deployment overrides are not used.
make tf-prod ACTION=refresh
make tf-preview PR=23 ACTION=refresh
```

Targeted owner-workspace plans/applies are available for maintenance:

- `make tf-dev-vault-ci-identities BRANCH=<ref> ACTION=plan|apply`
- `make tf-dev-vault-preview-runtime BRANCH=<ref> ACTION=plan|apply`
- `make tf-dev-ocr BRANCH=<ref> ACTION=plan|apply`
- `make tf-prod-ocr BRANCH=<40-character-sha> ACTION=plan|apply`

Targeted apply is not a substitute for the full plan, apply, attestation, and
readiness path. Root outputs are lifecycle-recorded by full applies, so a
targeted recovery keeps the last complete snapshot instead of persisting its
partial inputs. The Vault CI-identity path rejects its saved plan if it changes
any unrelated resource or recorded output. The preview-runtime entry point does
not target the Terraform owner graph: its typed reconciler first verifies the
project-bound Vault origin and GCP auth alias/metadata contract, then checks or
idempotently converges only the exact dev-owned policy and role with typed
readback. Auth-backend drift remains owner configuration and fails closed
without mutation. Shared Vault bootstrap and authorized root-token recovery are
documented in the
[deployment guide](../docs/operations/deployment.md#shared-vault-owner-bootstrap-and-recovery).
`ACTION=refresh` cannot be combined with a targeted maintenance entry point.
It validates and replays every field in the recorded `deployment_inputs`
schema. Additive legacy state has four unambiguous defaults: a missing
`dev_external_ocr_impersonators` is `[]`, a missing
`browser_readiness_image` is empty, and a missing
`browser_readiness_subnet_cidr` is `10.43.0.0/26`. A missing dedicated
`preview_machine_type` is the former `e2-medium` preview default and is ignored
outside preview workspaces. Explicit null or malformed values still fail
closed, and non-empty browser images remain subject to the exact
environment/image contract.
Refresh creates a full-graph saved `terraform plan -refresh-only` and rejects
non-move drift or any non-no-op resource/output action. The wrapper prints the
verified plan before auto-applying that exact saved plan. It does not invoke
image resolution, pull, or build tooling; remote-state, provider, backup-policy,
and Vault authentication prerequisites may still be required. Refresh and
ordinary destroy require an existing selected workspace and never create one.
The final Scribe move phase transfers the existing application VPC and subnet
from the counted Cloud Compose module addresses to root ownership only after
the module's historical address moves have converged. Cloud Compose then
consumes those exact resources with network creation disabled; normalization
does not recreate either object. Protected preview orchestration normalizes the
shared `dev` state after its backup audit and before targeted Vault maintenance;
production and preview workspace applies retain the full Terraform graph.
Destroy retains its one validated input snapshot across at most three attempts
for ordinary failures. A Google-managed `serverless-ipv4-*` or
`serverless-ipv6-*` dependency on the exact preview application subnet, exact
deterministic dual-stack browser subnet, or exact deterministic retired
`browser-v6` subnet from the previous independent-network design receives up
to 25 attempts, five minutes apart, because Cloud Run Direct VPC address
release can take two hours; it does not authorize deletion of the
provider-managed address. The retired name is a teardown-only classifier and
grants no active job attachment or PPB access. Only preview destroy receives
the extended workflow timeout; other deployment modes keep the ordinary bound.
A preview workspace is deleted only after destroy succeeds. If an interrupted
teardown already removed the current output, the protected preview workflow's
explicit
`recover-destroy` action can read the newest valid lower-serial, same-lineage
output from the versioned state object's history. It does not restore or
overwrite state and is not available for production. When a prior protected
destroy already deleted the exact preview workspace, the same recovery action
accepts that outcome only after a successful workspace inventory proves the
name is absent, then allows the workflow to finish its idempotent Vault/evidence
cleanup. Ordinary destroy, failed inventory, and a listed-but-unselectable
workspace remain fail closed.
For a pre-`deployment_inputs` workspace, `ACTION=normalize-moves` verifies the
remote-state backup policy and applies only the reviewed transitive Scribe and
pinned cloud-compose address changes with `terraform state mv`. It does not
refresh providers or apply infrastructure and fails on source/destination
conflicts.

## Inputs

Do not commit `terraform.tfvars`. The complete typed input contract and
examples are in [variables.tf](variables.tf) and
[terraform.tfvars.example](terraform.tfvars.example). The important groups are:

- `project_id`, `region`, `zone`, `name`, `docker_compose_branch`, and the
  preview-only `preview_machine_type` (`n2d-standard-2` or `e2-medium`);
- `allowed_ips`, exact SSH CIDRs, and the managed/Compose network CIDRs;
- `data_generation`, which scopes every persistent store and queue;
- the API, frontend GAR, and model-service image digests;
- workspace/global storage and transcription admission limits;
- Vault administrator and CI service-account identities; and
- production monitoring channels, backup identity, snapshot, and retention
  controls.

`deploy-local.sh` accepts these deployment-specific overrides:

- `TF_STATE_BUCKET`
- `SCRIBE_API_IMAGE`
- `SCRIBE_FRONTEND_GAR_IMAGE`
- `SCRIBE_OCR_IMAGES_JSON`
- `SCRIBE_DATA_GENERATION`
- `SCRIBE_OCR_IMAGE_TAG`
- `SCRIBE_PREVIEW_MACHINE_TYPE` (`n2d-standard-2` or `e2-medium`, preview only)
- `SCRIBE_REGION` and `SCRIBE_ZONE`
- `ALLOWED_IPS`, `ALLOWED_SSH_IPV4`, and `ALLOWED_SSH_IPV6`
- `DEV_EXTERNAL_OCR_IMPERSONATORS`, a JSON array of explicit `user:` or
  `group:` IAM members accepted only by the `dev` workspace
- `VAULT_ADMIN_EMAILS` and `VAULT_CI_SERVICE_ACCOUNT_EMAILS`
- the explicitly authorized `VAULT_BOOTSTRAP_MODE` and stored-root recovery
  location overrides described in the deployment guide

The current forward default is `data_generation = "canonical-v2"`. It is the
intentional cutover boundary for the released `0002` database schema and
isolates MariaDB, blobs, Triplet, cache, and queued work together. Do not reuse
`canonical-v1` for a forward rollout. Deployment state records the reviewed
value; rollback, refresh, and teardown reuse that recorded value rather than
the current caller default. Production and preview source refs and runtime
images must be immutable.

The deployed `canonical-v1` Pub/Sub resources keep their original Terraform
addresses. Forward generations use a parallel keyed graph selected from an
explicit ordered review list, and Terraform keeps every graph from v2 through
the selected entry. This makes a v2 apply create-only for the queue boundary,
preserves v1 messages and monitoring for rollback, and lets a rollback return
state to the exact address shape understood by the recorded v1 source. A future
cutover must append its generation to that list and its input allowlists in the
same reviewed change.

### Dev external OCR identity

Only workspace `dev` creates `scribe-dev-external`. Populate
`dev_external_ocr_impersonators` with reviewed `user:` or `group:` members;
Terraform grants those principals only `roles/iam.serviceAccountTokenCreator`
on that one service account. The account itself receives Cloud Run invoker on
the dev-owned Kraken and segmentor services, with no project-wide role and no
downloadable key.

Ollama is deliberately excluded. This repository's Ollama services are owned
by workspace `prod`, even when dev consumes them, so granting this identity
there would cross its dev-only boundary. Review the dev IAM plan explicitly
before applying any impersonator change.

### Image contract

Terraform receives only images consumed by deployed resources:

- `api_image` is the digest-pinned GHCR backend image run by Compose;
- `frontend_gar_image` is the digest-pinned GAR image run by the Cloud Run
  frontend sidecar; and
- `ocr_service_images` maps every configured OCR service key to its
  digest-pinned GAR image.

The reviewed frontend GHCR digest is a delivery-only source. Protected apply
verifies it, promotes that exact digest to GAR, and then passes only the
resolved GAR digest to Terraform. Terraform does not expose a parallel
`frontend_image` variable or state field.

Protected production delivery compares the last successfully deployed commit
with the new `main` commit using the deterministic OCR source path set. A
relevant change rebuilds the complete OCR matrix. A certain no-change result
instead validates the recorded `ocr_service_images` map against the current
required service keys and carries those reviewed digests forward. Missing
state, history, dependency information, or any other ambiguous comparison
rebuilds rather than reusing. The checkout-SHA image-tag invariant still
applies to every build; carried digests keep the provenance of the prior
reviewed build and are not assigned a replacement tag.

The `deployment_inputs` output records the Compose SHA, persistence generation,
actual runtime image digests, and non-secret configuration needed by refresh,
destroy, drift detection, and rollback. Replay validates the recorded schema
and fails closed rather than resolving a tag or guessing a missing value, apart
from the four documented additive legacy defaults.

## GitHub delivery

- [terraform-preview.yaml](../.github/workflows/terraform-preview.yaml) builds
  untrusted code without credentials, publishes reviewed artifacts after the
  protected gate, reads the stable OCR digest map recorded by production for
  the exact current `main` SHA, and applies or destroys `pr-*` workspaces.
- [terraform-apply.yaml](../.github/workflows/terraform-apply.yaml) reconciles
  the foundation, builds and scans immutable artifacts, rebuilds OCR only when
  its image inputs changed or reuse is ambiguous, and calls the protected
  production deployment workflow.
- [terraform-deploy.yaml](../.github/workflows/terraform-deploy.yaml) plans,
  applies, attests the deployed frontend digest, runs backend/OCR readiness,
  and restores the prior recorded release after a failed production rollout.
- [terraform-drift.yaml](../.github/workflows/terraform-drift.yaml) plans from
  the recorded production source and runtime inputs.
- [backup-verification.yaml](../.github/workflows/backup-verification.yaml)
  exercises the independent production recovery boundary.

Protected preview workspaces retain one root-owned per-environment application
VPC. The managed browser job attaches to a dedicated, non-overlapping
dual-stack `/26` in that VPC, and its interface tag selects an egress firewall
deny for the exact private application subnet CIDR. Only the browser subnet's
external IPv6 `/64` enters that preview's PPB allowlist. A reserved IPv4 address
and subnet-scoped Cloud NAT serve fixed DNS and reviewed IPv4-only fixture
origins without widening PPB or another environment. This topology adds no
browser-only VPC to the project's network quota.

A manual protected production `mode=plan` reuses current runtime image digests
and does not build, publish, promote, or apply. Production destroy is not
exposed by the GitHub workflow. An authorized local destroy requires a verified
recovery point and:

```bash
export CONFIRM_PRODUCTION_DESTROY=scribe-prod-destroy
make tf-prod BRANCH=<40-character-production-sha> ACTION=destroy
```

See the deployment guide for protected-environment variables, Workload
Identity Federation conditions, preview isolation, promotion, rollback,
readiness, and state-lock recovery. State files and `terraform.tfvars` are
ignored by the repository; never add secrets to Terraform values, workflow
inputs, build arguments, or image layers.
