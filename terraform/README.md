# Scribe Terraform

This directory deploys Scribe's `docker-compose.yaml` to a Google Compute Engine VM
through the external [`cloud-compose`](https://github.com/libops/cloud-compose)
module so the root Terraform can also attach the app ingress to a shared load balancer.

The same Terraform root can also manage shared production edge services:

- a frontend Cloud Run sidecar image attached to the app ingress
- a shared Cantaloupe Cloud Run deployment
- shared private Ollama model services built from a single generic module
- shared private OCR helper services for segmentation and image manipulation
- a private GCS uploads bucket shared by the API, worker, and image-service
- a shared external HTTPS load balancer that routes hostnames to the main app
  ingress and the shared Cantaloupe backend
- a self-hosted Vault Cloud Run deployment with bootstrapped mounts,
  policies, and GCP IAM auth roles

## What this does

- creates a GCE VM for the API, worker, and MariaDB process boundary plus a
  GCS bucket for uploaded image blobs
- clones the Scribe repo onto the VM
- deploys the backend VM compose stack without a local frontend proxy; the
  Cloud Run frontend image talks straight to the backend API on port 80
- runs `bash generate-secrets.sh` on first boot to create any missing docker
  secret files under `./secrets/`, except for externally managed credentials
  such as `./secrets/GOOGLE_APPLICATION_CREDENTIALS` where local/CI runs may use
  a placeholder file until infra provides the real value
- when `VAULT_ADDRESS` is configured, rewrites the MariaDB Docker secret files
  from the workspace-scoped Vault KV v2 path
  `secret/data/scribe/<workspace>/database/app` before
  `docker compose up` so MariaDB and the app share the same database password
  source. The init-only `vault-init` Compose service signs into Vault from the
  mounted `/run/secrets/GOOGLE_APPLICATION_CREDENTIALS` file rather than the
  metadata server, because Docker traffic to metadata is blocked on the VM
- injects non-secret runtime config into the compose services as environment
  variables, which `config.yaml` resolves via `${VAR}` / `${VAR:-default}`
  interpolation at process startup
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

Before a local `apply`, configure Docker auth for Artifact Registry. When the
expected frontend/OCR GAR tags are missing, `terraform/deploy-local.sh`
auto-builds and pushes the missing images to `us-docker.pkg.dev` before it
runs Terraform:

```bash
gcloud auth login
gcloud config set project "${GCLOUD_PROJECT}"
gcloud auth configure-docker us-docker.pkg.dev
```

Your user also needs Artifact Registry write access to
`projects/${GCLOUD_PROJECT}/locations/us/repositories/internal`. Local
`ACTION=plan` stays read-only; if a required GAR tag is missing it fails with
the image reference so you can publish it first or rerun with `ACTION=apply`.
Local Terraform deploys default `DOCKER_DEFAULT_PLATFORM` to `linux/amd64`, so
Apple Silicon and other ARM hosts do not publish ARM-only images for Cloud Run.

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
- `TF_VAR_frontend_image` (retained for local compose/build parity)
- Terraform workspace selection

Unlike plain `TF_VAR_*` environment variables, the local script passes the
dynamic values such as `name`, `docker_compose_branch`, and image refs as
explicit `terraform -var ...` flags so they override any checked-in or local
`terraform.tfvars`.

If you need to debug a failed GitHub deploy locally, use the same PR number and
branch name so the local run targets the same workspace and image tag.

By default, the local script uses `TF_STATE_BUCKET=${GCLOUD_PROJECT}-terraform`.
Set `TF_STATE_BUCKET` explicitly only if you need a different bucket.

## Required local variables

Do not commit `terraform.tfvars`. Copy `terraform.tfvars.example` locally or
pass these values through `TF_VAR_*`/the `make tf-*` wrappers.

- set `project_id`
- replace the sample SSH key
- restrict `allowed_ips` and `allowed_ssh_ipv4`
- set `docker_compose_branch` to the branch you want the VM to run
- optionally set `app_domain` and `cantaloupe_domain` if production should use
  the shared edge
- set `frontend_image` only if you still want the GHCR frontend image available
  for local compose builds; the VM itself no longer runs that service
- edit the `ocr:` section of `config.yaml` to declare the Ollama and Kraken
  Cloud Run services to deploy. Terraform reads that section directly at plan
  time; image builds happen in GitHub Actions (see `.github/workflows/build-ocr.yaml`)
  or are auto-published locally during `ACTION=apply`, and the digest map is
  passed in through `ocr_service_images`. Non-prod local applies skip the
  prod-only Ollama images because those services are only deployed from the
  `prod` workspace
- set `vault_admin_emails` and `vault_ci_service_account_emails` for the
  always-on Vault deployment
- optionally set `monitoring_notification_channels` to Cloud Monitoring channel
  IDs that should receive managed alerts, including the transcription DLQ alert
- initialize Terraform with a GCS backend bucket so CI and local runs share state

`vault_ci_service_account_emails` must include the GitHub Actions deploy service
account used by `secrets.GSA`. Local bootstrap configures a `google-jwt` auth
backend, per-admin `admin-*` and short-lived `break-glass-admin-*` roles for
`vault_admin_emails`, and `ci` roles for those service accounts. The GitHub
Terraform workflows log into Vault with a Google ID token before they run
Terraform. Those CI service accounts are also merged into the Vault proxy's
`X-Admin-Token` allow-list so they can reach non-public Vault routes during
bootstrap and login flows.

Routine `operator` Vault tokens can manage the app KV tree and inspect platform
configuration, but cannot mutate auth backends, identity entities, or ACL
policies. Use the short-lived `break-glass` role for those recovery operations.

## GitHub workflow

- Open or update a PR against `main` to create or refresh a preview environment.
- Close the PR to destroy that preview environment.
- Merge to `main` to build images and run a production Terraform plan.
- Use the `Terraform Apply` workflow manually with `mode=apply` to deploy
  production after reviewing the plan and passing the GitHub `production`
  environment approval gate.
- Run the `Terraform Dev` workflow from GitHub Actions to create, refresh, or
  destroy the shared `dev` environment from GitHub instead of a local machine.
- PR preview runs also reapply the shared `dev` owner resources from the
  PR branch before they apply the `pr-*` workspace, so Vault policy/auth
  changes and shared OCR helper service changes land on the shared dev
  environment during preview deploys.
- Configure `GCLOUD_PROJECT` as a GitHub Actions variable, not a secret. The
  deploy workflows use it in job outputs and reusable-workflow inputs, and
  GitHub suppresses secret-derived job outputs with a
  `Skip output ... since it may contain secret` warning.

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
- must be applied before any `pr-*` preview workspace, because previews read the shared dev Vault URL from remote state
- should be bootstrapped locally once first so Vault itself, the `google-jwt`
  auth backend, the admin login roles, and the `ci` login roles exist before
  GitHub preview/prod runs

## Creating the shared Vaults locally

Set the Vault admin and GitHub Actions CI service account in your tfvars first:

```hcl
vault_admin_emails = ["jjc223@lehigh.edu"]
vault_ci_service_account_emails = ["github@lehigh-lyrasis-catalyst.iam.gserviceaccount.com"]
```

For local one-off runs you can also pass those values through the deploy helper:

```bash
export VAULT_ADMIN_EMAILS='["jjc223@lehigh.edu"]'
export VAULT_CI_SERVICE_ACCOUNT_EMAILS='["github@lehigh-lyrasis-catalyst.iam.gserviceaccount.com"]'
```

GitHub Actions sets `VAULT_CI_SERVICE_ACCOUNT_EMAILS` from `secrets.GSA` and
sets `VAULT_ADMIN_EMAILS` to `["jjc223@lehigh.edu"]` so owner workspace plans
do not rely on local-only tfvars.

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
do it in two steps so the Vault provider has a concrete URL to talk to. The
second apply must be a normal apply, not a narrow target list, so Terraform can
create the `google-jwt` backend, the per-admin `admin-*` and
`break-glass-admin-*` login roles from `vault_admin_emails`, and the
per-service-account `ci` login roles from `vault_ci_service_account_emails`.
After the first Vault server exists, local deploys use Google JWT login for the
admin role. For the very first policy/auth bootstrap, export a one-time
`VAULT_TOKEN`; do not store or commit root tokens:

```bash
cd terraform
terraform init -backend-config="bucket=${GCLOUD_PROJECT}-terraform" -backend-config="prefix=scribe"
terraform workspace select dev || terraform workspace new dev
terraform apply -target=module.vault
export VAULT_TOKEN=<one-time bootstrap token>
terraform apply
```

Repeat that with workspace `prod` for production.

If local Google JWT admin login is unavailable or does not yet have enough Vault policy to bootstrap the auth backends and roles, local applies can download and decrypt the stored root token instead of logging in as a user:

```bash
export VAULT_BOOTSTRAP_MODE=root-token
make tf-prod ACTION=apply
```

By default the helper reads `gs://${GCLOUD_PROJECT}-vault-server-dev-key/root-token.enc` or `gs://${GCLOUD_PROJECT}-vault-server-prod-key/root-token.enc` depending on the shared Vault workspace, base64-decodes it, and decrypts it with KMS key `vault` in key ring `vault-server-dev` or `vault-server-prod`. Override `VAULT_ROOT_TOKEN_OBJECT`, `VAULT_ROOT_TOKEN_KMS_LOCATION`, `VAULT_ROOT_TOKEN_KMS_KEYRING`, or `VAULT_ROOT_TOKEN_KMS_KEY` if your stored token path differs.

If GitHub Actions fails with an error like `role "ci-...gserviceaccount-com" could not be found`, the Vault server is up but the JWT CI role for the current deploy service account does not exist in the owning shared Vault workspace. Update `vault_ci_service_account_emails` to include the exact service account email from `secrets.GSA`, then re-apply the owner workspace:

```bash
# shared dev Vault used by previews and local dev
make tf-dev-vault BRANCH=main ACTION=apply

# production Vault
make tf-prod ACTION=apply
```

Preview environments and the preview workflow's `sync-dev-shared` job use the shared `dev` Vault. Production uses the `prod` Vault.

## Backup and restore runbook

Production currently runs the app, worker, and MariaDB on one Compute Engine VM
with persistent disk snapshots enabled by `run_snapshots=true`. Treat those
snapshots as the recovery baseline unless and until the database moves to a
managed HA service.

Recommended operating target:

- Recovery point objective: last successful scheduled disk snapshot.
- Recovery time objective: restore the VM disk, run `make tf-prod ACTION=plan`,
  then `make tf-prod ACTION=apply` once the restored instance is verified.

Restore drill:

1. In Google Cloud, create a new persistent disk from the latest production
   snapshot of the docker volume disk.
2. Stop the production VM or restore into an isolated test VM first.
3. Attach the restored disk at the same mount point expected by the
   `cloud-compose` module.
4. Start compose and verify MariaDB health, `/healthz`, item listing, editor
   load, and an annotation save/reload cycle.
5. Record the snapshot timestamp used and the elapsed restore time in the
   incident or drill notes.

The transcription Pub/Sub dead-letter topic has a persistent monitor
subscription and an alert on undelivered messages. A non-empty DLQ means a job
exceeded Pub/Sub delivery attempts; inspect and ack messages from the
`scribe-<workspace>-transcription-jobs-dlq-monitor` subscription only after the
corresponding app job state and outbox events have been reviewed.

## Notes

- The checked-in [config.yaml](/workspace/config.yaml) is the source of truth
  for non-secret runtime config, but selected values are now resolved from
  compose-injected environment variables such as `PUBLIC_BASE_URL`,
  `VAULT_ADDRESS`, `OLLAMA_URL`, `OLLAMA_AUDIENCE`,
  `OLLAMA_MODEL_ENDPOINTS_JSON`,
  `SEGMENTATION_SERVICE_URL`, `SEGMENTATION_MODEL_ENDPOINTS_JSON`,
  `IMAGE_SERVICE_URL`, `SCRIBE_UPLOADS_BUCKET`, `SCRIBE_UPLOADS_PREFIX`,
  `KRAKEN_URL`, `KRAKEN_MODEL`, and
  `KRAKEN_MODEL_ENDPOINTS_JSON` at process startup. Production injects the
  shared `glm-ocr:bf16` Ollama Cloud Run URL automatically, plus model-keyed
  Ollama and Kraken endpoint maps. The standalone image-service runs on
  Cloud Run and reads uploads from the shared GCS bucket. Non-prod workspaces
  read shared Ollama URLs from remote Terraform state.
- The root module is intentionally opinionated. Service names, Artifact
  Registry layout, Cantaloupe sizing, Ollama sizing, and the compose bootstrap
  commands are internal defaults in Terraform rather than deployer-facing
  inputs.
- Vault and Ollama both use the pre-existing shared Artifact Registry
  repository `projects/<project>/locations/us/repositories/internal`. This
  root validates that the repo exists; it does not create it.
- The Vault Cloud Run service account must be able to verify app/VM service
  account JWTs for the `auth/gcp` backend. This root grants
  `roles/iam.serviceAccountViewer` and `roles/iam.serviceAccountKeyAdmin`
  on the Scribe app and VM service accounts so Vault can read service account
  metadata and the public key material needed to verify IAM login JWTs.
- The Vault proxy no longer exposes `/v1/secret/` as a public route. Runtime
  secret access must pass both the proxy layer and Vault ACLs.
- The app Vault policy is environment-scoped: each Terraform workspace gets an
  `app-<workspace>` policy that can read only
  `secret/data/scribe/<workspace>/...` runtime secrets. Provider-secret access
  is scoped to the deployment prefix
  `secret/data/scribe/<workspace>/provider-secrets/workspaces/*`; the Go app is
  the tenancy boundary and validates that provider-secret operations only touch
  the active workspace path before calling Vault. `vault_app_workspace_id` is a
  deprecated compatibility input and does not grant Vault-side per-tenant ACLs.
- The frontend image is built with
  `SCRIBE_FRONTEND_BACKEND_ORIGIN=http://<site>.<zone>.c.<project>.internal`
  so the ppb Cloud Run proxy talks straight to the backend API on the VM.
  Local Docker Compose instead sets `SCRIBE_FRONTEND_BACKEND_ORIGIN=http://api:8080`.
  The runtime container serves on port `8888` as the image's non-root `node`
  user.
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
- The upstream Vault module bakes the seal config into the Vault server image,
  so this root pins the Vault GAR image name per environment (`vault-server-dev`
  vs `vault-server-prod`) to avoid cross-workspace image drift.
- Preview and production deploys push backend and frontend images to GHCR. The
  backend image is injected into `TF_VAR_api_image`; `TF_VAR_frontend_image` is
  retained only for local compose/build parity while the Cloud Run frontend
  sidecar uses `frontend_gar_image`. GitHub Actions resolves backend, frontend,
  and frontend-GAR tags to immutable `@sha256:` digests before Terraform apply
  so VM and Cloud Run rollback follows Terraform state instead of a mutable tag.
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
- If Terraform is interrupted while holding the state lock, inspect the active
  workflow or local process first. Only after confirming no Terraform process
  is still running, recover with `cd terraform && terraform force-unlock <LOCK_ID>`,
  then rerun `make tf-prod ACTION=plan` before applying again.
- State files are already ignored by the repo's top-level `.gitignore`.
