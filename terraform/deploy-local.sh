#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  terraform/deploy-local.sh prod <plan|apply|destroy>
  terraform/deploy-local.sh preview <plan|apply|destroy> [--branch BRANCH] --pr-number NUMBER

Required environment:
  GCLOUD_PROJECT    GCP project ID

Optional environment:
  TF_STATE_BUCKET      GCS bucket used for Terraform remote state. Defaults to ${GCLOUD_PROJECT}-terraform
  ALLOWED_IPS         Terraform list(string), e.g. ["203.0.113.10/32"]
  ALLOWED_SSH_IPV4    Terraform list(string), e.g. ["203.0.113.10/32"]
  SCRIBE_APP_ENV      JSON object merged into TF_VAR_app_env

Notes:
  - Preview mode matches GitHub Actions naming only when --pr-number is supplied.
  - Preview images use ghcr.io/lehigh-university-libraries/scribe:<branch>
  - Production uses ghcr.io/lehigh-university-libraries/scribe:main
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

sanitize_branch() {
  printf '%s' "$1" | tr '[:upper:]' '[:lower:]'
}

merge_app_env() {
  local base_env="$1"
  local image_ref="$2"

  if [ -z "$base_env" ]; then
    base_env='{}'
  fi

  jq -cn --argjson base "$base_env" --arg image "$image_ref" '$base + {SCRIBE_API_IMAGE: $image}'
}

select_workspace() {
  local workspace="$1"

  if ! terraform workspace select "$workspace"; then
    terraform workspace new "$workspace" || terraform workspace select "$workspace"
  fi
  export TF_WORKSPACE="$workspace"
}

require_cmd git
require_cmd gcloud
require_cmd terraform
require_cmd jq

if [ $# -lt 2 ]; then
  usage
  exit 1
fi

environment="$1"
action="$2"
shift 2

if [ -z "${GCLOUD_PROJECT:-}" ]; then
  echo "GCLOUD_PROJECT is required." >&2
  exit 1
fi

TF_STATE_BUCKET="${TF_STATE_BUCKET:-${GCLOUD_PROJECT}-terraform}"
export TF_STATE_BUCKET

branch="$(git rev-parse --abbrev-ref HEAD)"
pr_number=""

while [ $# -gt 0 ]; do
  case "$1" in
    --branch)
      branch="${2:?--branch requires a value}"
      shift 2
      ;;
    --pr-number)
      pr_number="${2:?--pr-number requires a value}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

case "$environment" in
  prod)
    target_workspace="prod"
    export TF_VAR_name="scribe"
    export TF_VAR_docker_compose_branch="main"
    export TF_VAR_run_snapshots="true"
    image_tag="ghcr.io/lehigh-university-libraries/scribe:main"
    ;;
  preview)
    if [ -z "$pr_number" ]; then
      echo "--pr-number is required for preview mode so local runs match GitHub Actions." >&2
      exit 1
    fi
    target_workspace="pr-${pr_number}"
    export TF_VAR_name="scribe-pr-${pr_number}"
    export TF_VAR_docker_compose_branch="$branch"
    export TF_VAR_run_snapshots="false"
    image_tag="ghcr.io/lehigh-university-libraries/scribe:$(sanitize_branch "$branch")"
    ;;
  *)
    echo "Unknown environment: $environment" >&2
    usage
    exit 1
    ;;
esac

case "$action" in
  plan|apply|destroy) ;;
  *)
    echo "Unknown action: $action" >&2
    usage
    exit 1
    ;;
esac

cd "$(dirname "$0")"

export TF_VAR_project_id="$GCLOUD_PROJECT"
export TF_VAR_project_number
TF_VAR_project_number="$(gcloud projects describe "$GCLOUD_PROJECT" --format='value(projectNumber)')"
export TF_VAR_project_number

if [ -n "${ALLOWED_IPS:-}" ]; then
  export TF_VAR_allowed_ips="$ALLOWED_IPS"
fi

if [ -n "${ALLOWED_SSH_IPV4:-}" ]; then
  export TF_VAR_allowed_ssh_ipv4="$ALLOWED_SSH_IPV4"
fi

export TF_VAR_app_env
TF_VAR_app_env="$(merge_app_env "${SCRIBE_APP_ENV:-}" "$image_tag")"

terraform init -upgrade \
  -backend-config="bucket=${TF_STATE_BUCKET}" \
  -backend-config="prefix=scribe"

select_workspace "$target_workspace"

if [ "$action" != "destroy" ]; then
  terraform validate
fi

case "$action" in
  plan)
    terraform plan
    ;;
  apply)
    terraform apply -auto-approve
    ;;
  destroy)
    terraform destroy -auto-approve
    ;;
esac
