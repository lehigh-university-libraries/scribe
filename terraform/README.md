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
- starts Scribe with `docker compose up -d --remove-orphans`

The compose repo URL defaults to the current GitHub remote in HTTPS form:
`https://github.com/lehigh-university-libraries/scribe.git`

If the repository name or visibility changes, update `docker_compose_repo`.

## Local usage

```bash
cd /workspace/scribe/terraform
cp terraform.tfvars.example terraform.tfvars
terraform init -backend-config="bucket=YOUR_TF_STATE_BUCKET" -backend-config="prefix=scribe"
terraform workspace select prod || terraform workspace new prod
terraform plan
terraform apply
```

For a local preview environment, use a separate workspace and name:

```bash
cd /workspace/scribe/terraform
terraform init -backend-config="bucket=YOUR_TF_STATE_BUCKET" -backend-config="prefix=scribe"
terraform workspace select pr-123 || terraform workspace new pr-123
terraform plan \
  -var="name=scribe-pr-123" \
  -var="docker_compose_branch=my-feature-branch" \
  -var="run_snapshots=false"
terraform apply \
  -var="name=scribe-pr-123" \
  -var="docker_compose_branch=my-feature-branch" \
  -var="run_snapshots=false"
```

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

## Notes

- The default sample env uses Ollama on `http://localhost:11434`. For a real VM
  deployment you can edit `.env` on the instance after first boot. If you do
  not want Ollama at all, provide a branch/commit with production-ready env
  values for `openai` or `gemini`.
- The recommended CI pattern for app env vars is a single JSON object mapped
  into `TF_VAR_app_env`, for example:
  `{"OLLAMA_URL":"https://ollama.example.org","OPENAI_API_KEY":"..."}`
- MariaDB passwords are now generated into docker secret files by
  `generate-secrets.sh` instead of being stored directly in `.env`.
- The PR preview comment includes the Cloud Run ingress URL from
  `urls["us-east5"]` after a successful apply.
- `cloud-compose` expects an HTTPS clone URL unless you also manage SSH deploy keys
  on the VM.
- GitHub Actions preview and prod deploys assume a remote GCS backend and use
  Terraform workspaces to isolate `prod` from `pr-<number>` preview environments.
- State files are already ignored by the repo's top-level `.gitignore`.
