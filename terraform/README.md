# Scribe Terraform

This directory deploys Scribe to GCP through the pinned
[`libops/cloud-compose`](https://github.com/libops/cloud-compose) module. The
root owns the VM and Compose application, Cloud Run ingress and frontend
sidecar, OCR services, upload and backup storage, Pub/Sub, monitoring, and the
environment-owned Vault service. Project-wide APIs, Artifact Registry, and
custom roles belong to the separate `terraform/foundation` root.

The [deployment guide](../docs/operations/deployment.md) covers environments,
CI, Vault, and new-project setup.

## Usage

```bash
export GCLOUD_PROJECT=your-gcp-project-id
gcloud auth login
make tf-dev                       # plan the dev workspace
make tf-prod ACTION=apply         # apply production
make tf-preview PR=23 ACTION=destroy
```

The Make targets init the `scribe` state prefix, select or create the workspace
(`dev`, `prod`, or `pr-<number>`), get a Vault token from
`../scripts/vault-token.sh`, and run `terraform $ACTION $ARGS`. Copy
`terraform.tfvars.example` to the ignored `terraform.tfvars` for local values,
or use `TF_VAR_*` variables.

`make terraform-check` formats, initializes, and validates both roots with the
pinned Terraform version.

## What comes from the workspace

- Name: `scribe` for `prod`, otherwise `scribe-<workspace>`.
- Zone: `<region>-b`, or `<region>-c` for previews, unless `zone` is set (CI
  passes the `SCRIBE_ZONE` or `SCRIBE_PREVIEW_ZONE` variable).
- Snapshots, upload backups, and Ollama services exist only in `prod`.
- Previews use dev's Vault and get a generated database password there.

## Images

[images.tf](images.tf) resolves every image tag to a digest at plan time:

- `ghcr.io/lehigh-university-libraries/scribe:<image_tag>` (backend, run by
  Compose);
- `<GAR>/scribe-frontend:<image_tag>` (Cloud Run frontend sidecar);
- one GAR image per OCR service in `../config/ocr.yaml`, at `<ocr_image_tag>`.

Both tags default to `main`, so re-applying after CI pushes a new `:main`
rolls it out. The VM's Compose checkout follows `docker_compose_branch`, also
`main` by default.

## Inputs

[variables.tf](variables.tf) and [terraform.tfvars.example](terraform.tfvars.example)
define the inputs. The important ones are `allowed_ips`, SSH CIDRs, the
network CIDRs, `data_generation`, storage and transcription limits, the Vault
administrator and CI identities, and production monitoring channels. Runtime
quota, storage, and IIIF defaults come from `../config.yaml`; set a `TF_VAR_*`
only to override one.

`data_generation` (default `canonical-v2`) scopes every persistent store and
queue. Changing it is an explicit cutover; see the deployment guide.

The `*_moved.tf` files map pre-1.x cloud-compose addresses. Delete them once a
plan in every workspace shows no moves.

### Dev external OCR identity

Only workspace `dev` creates `scribe-dev-external`. List reviewed `user:` or
`group:` members in `dev_external_ocr_impersonators`; they get only
`roles/iam.serviceAccountTokenCreator` on that account, which can invoke the
dev Kraken and segmentor services. Ollama belongs to `prod` and is excluded.

## GitHub delivery

- [terraform-apply.yaml](../.github/workflows/terraform-apply.yaml): on push to
  `main`, builds `:main` images, rebuilds OCR images when their inputs
  changed, applies the foundation, then applies `prod`.
- [terraform-preview.yaml](../.github/workflows/terraform-preview.yaml): builds
  same-repository PRs without credentials, publishes the archives, and applies
  or destroys `pr-<number>`.
- [terraform-deploy.yaml](../.github/workflows/terraform-deploy.yaml): the
  shared Terraform job, plus the readiness jobs after an apply.
- [build-ocr.yaml](../.github/workflows/build-ocr.yaml): builds the OCR image
  matrix.

Never put secrets in Terraform values, workflow inputs, build arguments, or
image layers.
