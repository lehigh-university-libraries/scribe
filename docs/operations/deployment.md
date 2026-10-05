# Deployment

Deploying Scribe is `terraform apply` in the right workspace. Terraform resolves
image tags to digests itself, derives each environment's names from the
workspace, and owns every cloud resource. CI builds images and runs the same
Make targets an operator runs locally.

| Environment | Workspace | Site | Images | Vault |
| --- | --- | --- | --- | --- |
| Production | `prod` | `scribe` | `:main` | owns `vault-server-prod` |
| Development | `dev` | `scribe-dev` | `:main` unless `TF_VAR_image_tag` is set | owns `vault-server-dev` |
| Preview | `pr-<number>` | `scribe-pr-<number>` | `:pr-<number>` backend and frontend, `:main` OCR | uses dev's Vault |

## How production deploys

Every push to `main` runs [Terraform Apply](https://github.com/lehigh-university-libraries/scribe/actions/workflows/terraform-apply.yaml):

1. The CI gate runs.
2. The backend image is pushed to GHCR, and the frontend image to Artifact
   Registry, as `:main` and `:<commit>`.
3. OCR images are rebuilt only when a path listed by `ci/ocr-source-paths.sh`
   changed in the push, or when one of their `:main` images is missing. They
   are built from `go run ./cmd/ocr-matrix` and pushed as `:main` and
   `:<commit>`.
4. The foundation root (`terraform/foundation`) is applied.
5. `make tf-prod ACTION=apply` runs, then the backend and OCR readiness
   Cloud Run jobs run with `gcloud run jobs execute --wait`, up to six times
   two minutes apart while a replaced VM boots.

The VM checks out `main` (`docker_compose_branch`) on each rollout. Because new
images change their digests, every push replaces the VM's boot disk and
re-runs bootstrap. The data and Docker-volume disks are kept.

A manual run of the workflow can `plan` or `apply` production without building
anything.

To roll back, revert the commit on `main` and let it deploy.

## Previews

Same-repository pull requests get a preview at `scribe-pr-<number>`; closing
the PR destroys it. Fork pull requests run CI only.

The PR head's backend and frontend are built as OCI archives in a job with no
credentials. A separate job, which never runs pull-request code, pushes the
archives to `ghcr.io/lehigh-university-libraries/scribe:pr-<number>` and
`<GAR>/scribe-frontend:pr-<number>`. Terraform then runs from `main` with
`TF_VAR_image_tag=pr-<number>`. Previews reuse production's `:main` OCR
images and production's Ollama service.

Each preview stores its own generated database password in dev's Vault under
`scribe/previews/scribe-pr-<number>@<project>.iam.gserviceaccount.com/`.
Destroying the preview removes it. Previews run in `<region>-c` (or
`SCRIBE_PREVIEW_ZONE`) on
`n2d-standard-2` with standard persistent disks; set the
`SCRIBE_PREVIEW_MACHINE_TYPE` repository variable to change the machine type.
To destroy a preview by hand, dispatch the Terraform Preview workflow with the
PR number.

## Running Terraform locally

```bash
export GCLOUD_PROJECT=your-project
make tf-dev                          # plan
make tf-dev ACTION=apply
make tf-prod ACTION=plan
make tf-preview PR=123 ACTION=destroy
make tf-prod ACTION=apply ARGS='-target=module.kraken'
```

Each target initializes the `scribe` state prefix in
`${TF_STATE_BUCKET:-$GCLOUD_PROJECT-terraform}`, selects or creates the
workspace, and runs `terraform $ACTION $ARGS`. Terraform variables come from
`terraform/terraform.tfvars` and `TF_VAR_*` environment variables. CI exports
`TF_VAR_*` from the environment's GitHub variables and secrets.

To deploy a branch to dev, push images tagged with the branch name and set
`TF_VAR_image_tag` to that tag.

## Vault

Terraform's Vault provider needs a token. Unless `VAULT_TOKEN` is set,
`scripts/vault-token.sh` gets one:

- It logs in through Google JWT as the active gcloud account, using the
  `break-glass-admin-<account>` or `admin-<account>` role.
- If that fails (a new Vault has no roles yet, and CI has none), it decrypts the
  stored root token from `gs://<project>-vault-server-<dev|prod>-key/root-token.enc`
  with the `vault` key in the `vault-server-<dev|prod>` KMS key ring.

When dev or prod has no Vault yet (a new project, or Vault was deleted),
`make tf-dev ACTION=apply` or `make tf-prod ACTION=apply` first runs
`terraform apply -target=module.vault`, which creates Vault and runs its init
job, then gets the root token and runs the full apply. Terraform can't do this
in one apply because the Vault provider needs a token from that Vault.

Set `vault_admin_emails` and `vault_ci_service_account_emails` in tfvars, or the
matching `TF_VAR_*` variables, before the first apply of an owner workspace.

Application secrets (Google OAuth, OpenAI, Gemini, database password) are set
interactively:

```bash
make vault-secrets WORKSPACE=prod
make vault-secrets WORKSPACE=prod CMD=show
```

## New project setup

1. Create the state bucket and the production deploy service account by hand,
   and give that account the roles Terraform needs.
2. Run `make bootstrap-gcp-identities` with `GCLOUD_PROJECT` and
   `MONITORING_NOTIFICATION_EMAIL` set. It creates the preview deploy and
   production OCR identities, turns on state bucket versioning and soft
   delete, and creates the alert email channel.
3. Configure the `production` and `preview` GitHub environments: secrets
   `GCLOUD_OIDC_POOL`, `GSA`, `TF_STATE_BUCKET`; variables
   `GCLOUD_PROJECT`, `ALLOWED_IPS`, `VAULT_ADMIN_EMAILS`,
   `MONITORING_NOTIFICATION_CHANNELS`, `OCR_GCLOUD_OIDC_POOL`, `OCR_GSA`.
4. Push to `main`.

## Images and registry cleanup

Terraform reads `scribe:<image_tag>` from GHCR and `scribe-frontend:<image_tag>`
and every OCR image (`:<ocr_image_tag>`, default `main`) from the internal
Artifact Registry repository, and deploys them by digest.

The internal repository keeps every `main`-tagged image and the 10 most
recent versions of each image. It deletes untagged versions after 7 days and
anything else after 30 days.

## Persistence generations

`data_generation` (default `canonical-v2`) scopes every Compose volume, the GCS
upload prefix, and the transcription Pub/Sub topics. Changing it is an explicit
cutover: the new generation starts empty, and the old volumes, prefix, and
queues are kept for inspection. Terraform keeps every generation in its ordered
`reviewed_data_generations` list through the selected one.

## State-lock recovery

If an interrupted run leaves a state lock, first confirm that no workflow or
local Terraform process is still running, then:

```bash
terraform -chdir=terraform force-unlock <LOCK_ID>
make tf-prod ACTION=plan
```

## Runtime notes

- Cloud Compose's pinned release selects the COS image. Upgrade COS by pinning a
  newer Cloud Compose release; Terraform replaces the boot disk and keeps the
  data disks.
- The Compose checkout lives at `/mnt/disks/data/scribe/<workspace>`. Use
  `cloud-compose.service` and `/home/cloud-compose/{init,up,down}` on the VM;
  do not `git pull` there.
- Containers drop all capabilities, run with read-only root filesystems, and
  handle `SIGTERM` with a bounded drain.
- The API and worker mint identity tokens for every configured OCR audience
  before they start listening, so a bad credential fails startup rather than
  the first upload.

See [troubleshooting](troubleshooting.md) for VM, Compose, and readiness
diagnostics.
