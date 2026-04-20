# Scribe Terraform

This directory deploys Scribe's `docker-compose.yaml` to a Google Compute Engine VM
through the external [`cloud-compose`](https://github.com/libops/cloud-compose)
module so the root Terraform can also attach the app ingress to a shared load balancer.

The same Terraform root can also manage shared production edge services:

- a frontend Cloud Run sidecar image attached to the app ingress
- a shared Cantaloupe Cloud Run deployment
- shared private Ollama model services built from a single generic module
- a shared external HTTPS load balancer that routes hostnames to the main app
  ingress and the shared Cantaloupe backend
- a self-hosted Vault Cloud Run deployment with bootstrapped mounts,
  policies, and GCP IAM auth roles

## What this does

- creates a GCE VM and persistent disks
- clones the Scribe repo onto the VM
- deploys the frontend image on the VM compose stack and, unless
  `frontend_image = ""`, also attaches it as a sidecar next to ppb
- runs `bash generate-secrets.sh` on first boot to create any missing docker
  secret files under `./secrets/`
- patches `config.yaml` on the VM with non-secret runtime values such as the
  Vault address
- pulls the configured API image from GHCR, then starts Scribe with the
  pinned compose commands defined in the root module

## Local usage

Local Terraform should use the same conventions as GitHub Actions:

- shared dev workspace: `dev`
- shared dev image: `ghcr.io/lehigh-university-libraries/scribe:<branch>`
- shared dev frontend image: `ghcr.io/lehigh-university-libraries/scribe-frontend:<branch>`
- production workspace: `prod`
- production image: `ghcr.io/lehigh-university-libraries/scribe:main`
- production frontend image: `ghcr.io/lehigh-university-libraries/scribe-frontend:main`
- preview workspace: `pr-<number>`
- preview image: `ghcr.io/lehigh-university-libraries/scribe:<branch>`
- preview frontend image: `ghcr.io/lehigh-university-libraries/scribe-frontend:<branch>`
- preview site name: `scribe-pr-<number>`

```bash
export GCLOUD_PROJECT=your-gcp-project-id
make tf-dev BRANCH=google-cloud ACTION=plan
make tf-dev BRANCH=google-cloud ACTION=apply
```

For production:

```bash
export GCLOUD_PROJECT=your-gcp-project-id
make tf-prod ACTION=plan
make tf-prod ACTION=apply
```

For a local preview environment that matches GitHub Actions:

```bash
export GCLOUD_PROJECT=your-gcp-project-id
make tf-preview PR=23 BRANCH=google-cloud ACTION=plan
make tf-preview PR=23 BRANCH=google-cloud ACTION=apply
```

The underlying script is [deploy-local.sh](/workspace/terraform/deploy-local.sh). It mirrors the
GitHub deploy workflow's variable setup for:

- `TF_VAR_name`
- `TF_VAR_docker_compose_branch`
- `TF_VAR_run_snapshots`
- `TF_VAR_project_id`
- `TF_VAR_allowed_ips`
- `TF_VAR_allowed_ssh_ipv4`
- `TF_VAR_api_image`
- `TF_VAR_frontend_image`
- Terraform workspace selection

Unlike plain `TF_VAR_*` environment variables, the local script passes the
dynamic values such as `name`, `docker_compose_branch`, and image refs as
explicit `terraform -var ...` flags so they override any checked-in or local
`terraform.tfvars`.

If you need to debug a failed GitHub deploy locally, use the same PR number and
branch name so the local run targets the same workspace and image tag.

By default, the local script uses `TF_STATE_BUCKET=${GCLOUD_PROJECT}-terraform`.
Set `TF_STATE_BUCKET` explicitly only if you need a different bucket.

## Required edits in `terraform.tfvars`

- set `project_id`
- replace the sample SSH key
- restrict `allowed_ips` and `allowed_ssh_ipv4`
- set `docker_compose_branch` to the branch you want the VM to run
- optionally set `app_domain` and `cantaloupe_domain` if production should use
  the shared edge
- set `frontend_image = ""` only if you intentionally want to disable the ppb
  frontend sidecar; otherwise the default split-frontend deployment is used
- optionally set `ollama_models` to build and deploy one or more private Ollama
  Cloud Run services keyed by model string
- set `vault_admin_emails` and `vault_ci_service_account_emails` for the
  always-on Vault deployment
- initialize Terraform with a GCS backend bucket so CI and local runs share state

## GitHub workflow

- Open or update a PR against `main` to create or refresh a preview environment.
- Close the PR to destroy that preview environment.
- Merge to `main` to deploy production.

Preview environments use:

- Terraform workspace `pr-<number>`
- VM name `scribe-pr-<number>`
- the PR branch as `docker_compose_branch`
- no snapshots

Production uses:

- Terraform workspace `prod`
- VM name `scribe`
- branch `main`
- Cloud Run ingress port `8080`

Shared dev uses:

- Terraform workspace `dev`
- VM name `scribe-dev`
- the supplied branch as `docker_compose_branch`
- the shared dev Vault server

## Creating the shared Vaults locally

Set the Vault admins in your tfvars first, for example:

```hcl
vault_admin_emails = ["jjc223@lehigh.edu"]
```

Then create the shared dev environment, which also creates the shared dev Vault:

```bash
export GCLOUD_PROJECT=your-gcp-project-id
gcloud services enable cloudkms.googleapis.com --project "${GCLOUD_PROJECT}"
make tf-dev BRANCH=main ACTION=apply
```

Create production the same way:

```bash
export GCLOUD_PROJECT=your-gcp-project-id
gcloud services enable cloudkms.googleapis.com --project "${GCLOUD_PROJECT}"
make tf-prod ACTION=apply
```

If you only want the Vault/bootstrap resources first, select the workspace and
do it in two steps so the Vault provider has a concrete URL to talk to:

```bash
cd terraform
terraform init -backend-config="bucket=${GCLOUD_PROJECT}-terraform" -backend-config="prefix=scribe"
terraform workspace select dev || terraform workspace new dev
terraform apply -target=module.vault
terraform apply \
  -target=vault_mount.secret \
  -target=vault_mount.keys \
  -target=vault_policy.vault \
  -target=vault_auth_backend.gcp
```

Repeat that with workspace `prod` for production.

## Notes

- The checked-in [config.yaml](/workspace/config.yaml) is the source of truth
  for non-secret runtime config. To point Scribe at a shared Ollama Cloud Run
  service, set `llm.ollama.url` to the Terraform output for the desired model.
- The root module is intentionally opinionated. Service names, Artifact
  Registry layout, Cantaloupe sizing, Ollama sizing, and the compose bootstrap
  commands are internal defaults in Terraform rather than deployer-facing
  inputs.
- When the frontend sidecar is enabled, the frontend container must know how to reach
  the VM backend. The GitHub deploy workflow builds the frontend image with
  `SCRIBE_FRONTEND_BACKEND_ORIGIN=http://<site>.<zone>.c.<project>.internal`.
  Local Docker Compose instead sets `SCRIBE_FRONTEND_BACKEND_ORIGIN=http://api:8080`.
- If the Ollama Cloud Run service uses its default `run.app` URL, Scribe can
  derive the ID token audience automatically. Set `llm.ollama.audience` only
  if you intentionally configured a custom audience.
- The root Terraform uses the upstream `cloud-compose` module directly and is
  pinned to a commit that exposes the ppb backend output and optional frontend
  sidecar input.
- Shared Cantaloupe, shared Ollama model services, and the shared load balancer
  are only created in the `prod` workspace.
- Vault is split by environment: the `dev` workspace owns the shared dev Vault
  used by local dev and `pr-*` preview environments, while `prod` owns the
  production Vault. Preview workspaces do not create their own Vault servers;
  they read the shared dev Vault URL from remote state and create a
  workspace-specific GCP auth role there.
- Preview and production deploys push backend and frontend images to GHCR, then
  inject those exact image references into `TF_VAR_api_image` and
  `TF_VAR_frontend_image`.
- MariaDB passwords are now generated into docker secret files by
  `generate-secrets.sh` instead of being stored directly in `.env`.
- The PR preview comment includes the Cloud Run ingress URL from
  `urls["us-east5"]` after a successful apply.
- Shared Ollama model service URLs are exported in the `ollama_services` output,
  keyed by the original model string such as `glm-ocr:bf16`.
- `cloud-compose` expects an HTTPS clone URL unless you also manage SSH deploy keys
  on the VM.
- GitHub Actions preview and prod deploys assume a remote GCS backend and use
  Terraform workspaces to isolate `prod` from `pr-<number>` preview environments.
- State files are already ignored by the repo's top-level `.gitignore`.
