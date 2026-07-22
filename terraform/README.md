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
resources. Initialize that state with the default workspace and isolated
prefix:

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
```

Targeted owner-workspace plans/applies are available for maintenance:

- `make tf-dev-vault BRANCH=<ref> ACTION=plan|apply`
- `make tf-dev-ocr BRANCH=<ref> ACTION=plan|apply`
- `make tf-prod-ocr BRANCH=<40-character-sha> ACTION=plan|apply`

Targeted apply is not a substitute for the full plan, apply, attestation, and
readiness path. Shared Vault bootstrap and authorized root-token recovery are
documented in the [deployment guide](../docs/operations/deployment.md#shared-vault-owner-bootstrap-and-recovery).

## Inputs

Do not commit `terraform.tfvars`. The complete typed input contract and
examples are in [variables.tf](variables.tf) and
[terraform.tfvars.example](terraform.tfvars.example). The important groups are:

- `project_id`, `region`, `zone`, `name`, and `docker_compose_branch`;
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
- `SCRIBE_REGION` and `SCRIBE_ZONE`
- `ALLOWED_IPS`, `ALLOWED_SSH_IPV4`, and `ALLOWED_SSH_IPV6`
- `VAULT_ADMIN_EMAILS` and `VAULT_CI_SERVICE_ACCOUNT_EMAILS`
- the explicitly authorized `VAULT_BOOTSTRAP_MODE` and stored-root recovery
  location overrides described in the deployment guide

Keep `data_generation = "canonical-v1"` unless an intentional greenfield
cutover is isolating MariaDB, blobs, Triplet, cache, and queued work together.
Production and preview source refs and runtime images must be immutable.

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

The `deployment_inputs` output records the Compose SHA, persistence generation,
actual runtime image digests, and non-secret configuration needed by destroy,
drift detection, and rollback. Replay validates the exact schema and fails
closed rather than resolving a tag or guessing a missing value.

## GitHub delivery

- [terraform-preview.yaml](../.github/workflows/terraform-preview.yaml) builds
  untrusted code without credentials, publishes reviewed artifacts after the
  protected gate, and applies or destroys `pr-*` workspaces.
- [terraform-apply.yaml](../.github/workflows/terraform-apply.yaml) reconciles
  the foundation, builds and scans immutable artifacts, and calls the protected
  production deployment workflow.
- [terraform-deploy.yaml](../.github/workflows/terraform-deploy.yaml) plans,
  applies, attests the deployed frontend digest, runs backend/OCR readiness,
  and restores the prior recorded release after a failed production rollout.
- [terraform-drift.yaml](../.github/workflows/terraform-drift.yaml) plans from
  the recorded production source and runtime inputs.
- [backup-verification.yaml](../.github/workflows/backup-verification.yaml)
  exercises the independent production recovery boundary.

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
