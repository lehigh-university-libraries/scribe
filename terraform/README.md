# Scribe Terraform

This directory deploys Scribe's `docker-compose.yaml` to a Google Compute Engine VM
through the [`cloud-compose`](https://github.com/libops/cloud-compose) module pinned
to `0.3.0`.

## What this does

- creates a GCE VM and persistent disks
- clones the Scribe repo onto the VM
- runs `test -f .env || cp sample.env .env` on first boot so the compose stack has
  a starter env file
- runs `bash generate-secrets.sh` on first boot to create any missing docker
  secret files under `./secrets/`
- merges secret environment variables from Terraform into `.env` before startup
- pulls the configured API image from GHCR, then starts Scribe with
  `docker compose up -d --no-build --remove-orphans`

The compose repo URL defaults to the current GitHub remote in HTTPS form:
`https://github.com/lehigh-university-libraries/scribe.git`

If the repository name or visibility changes, update `docker_compose_repo`.

## Local usage

Local Terraform should use the same conventions as GitHub Actions:

- production workspace: `prod`
- production image: `ghcr.io/lehigh-university-libraries/scribe:main`
- preview workspace: `pr-<number>`
- preview image: `ghcr.io/lehigh-university-libraries/scribe:<branch>`
- preview site name: `scribe-pr-<number>`

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

The underlying script is [deploy-local.sh](/workspace/scribe/terraform/deploy-local.sh). It mirrors the
GitHub deploy workflow's variable setup for:

- `TF_VAR_name`
- `TF_VAR_docker_compose_branch`
- `TF_VAR_run_snapshots`
- `TF_VAR_project_id`
- `TF_VAR_project_number`
- `TF_VAR_allowed_ips`
- `TF_VAR_allowed_ssh_ipv4`
- `TF_VAR_app_env`
- Terraform workspace selection

If you need to debug a failed GitHub deploy locally, use the same PR number and
branch name so the local run targets the same workspace and image tag.

By default, the local script uses `TF_STATE_BUCKET=${GCLOUD_PROJECT}-terraform`.
Set `TF_STATE_BUCKET` explicitly only if you need a different bucket.

## Required edits in `terraform.tfvars`

- set `project_id`
- set `project_number`
- replace the sample SSH key
- restrict `allowed_ips` and `allowed_ssh_ipv4`
- set `docker_compose_branch` to the branch you want the VM to run
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

## Notes

- The default sample env uses Ollama on `http://localhost:11434`. For a real VM
  deployment you can edit `.env` on the instance after first boot. If you do
  not want Ollama at all, provide a branch/commit with production-ready env
  values for `openai` or `gemini`.
- The recommended CI pattern for app env vars is a single JSON object mapped
  into `TF_VAR_app_env`, for example:
  `{"OLLAMA_URL":"https://ollama.example.org","SCRIBE_API_IMAGE":"ghcr.io/lehigh-university-libraries/scribe:pr-123-deadbeef","OPENAI_API_KEY":"..."}`
- Preview and production deploys push an image to GHCR first, then inject that
  exact image reference into `SCRIBE_API_IMAGE`.
- MariaDB passwords are now generated into docker secret files by
  `generate-secrets.sh` instead of being stored directly in `.env`.
- The PR preview comment includes the Cloud Run ingress URL from
  `urls["us-east5"]` after a successful apply.
- `cloud-compose` expects an HTTPS clone URL unless you also manage SSH deploy keys
  on the VM.
- GitHub Actions preview and prod deploys assume a remote GCS backend and use
  Terraform workspaces to isolate `prod` from `pr-<number>` preview environments.
- State files are already ignored by the repo's top-level `.gitignore`.
