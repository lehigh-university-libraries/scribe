#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  terraform/deploy-local.sh dev <plan|apply|destroy> [--branch BRANCH]
  terraform/deploy-local.sh prod <plan|apply|destroy>
  terraform/deploy-local.sh preview <plan|apply|destroy> [--branch BRANCH] --pr-number NUMBER

Required environment:
  GCLOUD_PROJECT    GCP project ID

Optional environment:
  TF_STATE_BUCKET      GCS bucket used for Terraform remote state. Defaults to ${GCLOUD_PROJECT}-terraform
  ALLOWED_IPS         Terraform list(string), e.g. ["203.0.113.10/32"]
  ALLOWED_SSH_IPV4    Terraform list(string), e.g. ["203.0.113.10/32"]
  SCRIBE_API_IMAGE    Exact backend image to inject into Terraform api_image
  SCRIBE_FRONTEND_IMAGE  Optional GHCR frontend image reference to inject into Terraform frontend_image for local parity
  SCRIBE_FRONTEND_GAR_IMAGE  Exact frontend image to inject into Terraform frontend_gar_image (GAR, used by the Cloud Run sidecar). When unset, local apply resolves the default tag and auto-builds it if missing.
  SCRIBE_OCR_IMAGES_JSON  Pre-resolved JSON map of OCR service_key -> GAR digest ref. When unset (and the action is not destroy), deploy-local.sh calls ci/generate-ocr-images-map.sh to resolve the digests from existing GAR tags.
  SCRIBE_OCR_IMAGE_TAG    Tag to resolve against when generating the OCR image map locally. Defaults to main for prod and the branch slug otherwise.
  SCRIBE_ZONE            Optional zone override used when locally building the frontend GAR sidecar. Falls back to TF_VAR_zone, terraform/terraform.tfvars, then us-east5-b.

Notes:
  - Dev mode uses Terraform workspace dev and site name scribe-dev.
  - Preview mode matches GitHub Actions naming only when --pr-number is supplied.
  - Preview images use ghcr.io/lehigh-university-libraries/scribe:<branch>
  - Preview frontend images use ghcr.io/lehigh-university-libraries/scribe-frontend:<branch>
  - Production uses ghcr.io/lehigh-university-libraries/scribe:main
  - Production frontend uses ghcr.io/lehigh-university-libraries/scribe-frontend:main
  - Local apply auto-builds missing frontend/OCR GAR images needed by the selected workspace.
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

decode_base64_file() {
  local input="$1"
  local output="$2"

  if base64 --decode <"$input" >"$output" 2>/dev/null; then
    return 0
  fi
  if base64 -d <"$input" >"$output" 2>/dev/null; then
    return 0
  fi
  if base64 -D <"$input" >"$output" 2>/dev/null; then
    return 0
  fi

  echo "Failed to base64 decode $input" >&2
  return 1
}

sanitize_image_tag() {
  printf '%s' "$1" | sed 's/[^a-zA-Z0-9._-]//g' | awk '{print substr($0, length($0)-120)}' | tr '[:upper:]' '[:lower:]'
}

resolve_terraform_zone() {
  local tfvars_path="$repo_root/terraform/terraform.tfvars"
  local from_tfvars=""

  if [ -n "${SCRIBE_ZONE:-}" ]; then
    printf '%s\n' "$SCRIBE_ZONE"
    return 0
  fi

  if [ -n "${TF_VAR_zone:-}" ]; then
    printf '%s\n' "$TF_VAR_zone"
    return 0
  fi

  if [ -f "$tfvars_path" ]; then
    from_tfvars="$(sed -nE 's/^[[:space:]]*zone[[:space:]]*=[[:space:]]*"([^"]+)"[[:space:]]*$/\1/p' "$tfvars_path" | tail -n1)"
    if [ -n "$from_tfvars" ]; then
      printf '%s\n' "$from_tfvars"
      return 0
    fi
  fi

  printf 'us-east5-b\n'
}

build_frontend_gar_image() {
  local image_ref="$1"
  local zone backend_origin

  zone="$(resolve_terraform_zone)"
  backend_origin="http://${TF_VAR_name}.${zone}.c.${GCLOUD_PROJECT}.internal"

  echo "GAR image missing for frontend sidecar; building and pushing ${image_ref} with backend origin ${backend_origin}..." >&2
  "${repo_root}/ci/build-push-gar-image.sh" \
    --image "$image_ref" \
    --context "$repo_root" \
    --file "${repo_root}/Dockerfile.frontend" \
    --platform "linux/amd64" \
    --build-arg "SCRIBE_FRONTEND_BACKEND_ORIGIN=${backend_origin}"
}

resolve_frontend_gar_image() {
  local image_ref="$1"
  local action="$2"
  local resolved=""

  if resolved="$("${repo_root}/ci/resolve-gar-image.sh" "$image_ref" 2>&1)"; then
    printf '%s\n' "$resolved"
    return 0
  fi

  echo "$resolved" >&2

  if [ "$action" != "apply" ]; then
    echo "Missing frontend GAR image: ${image_ref}" >&2
    echo "Rerun with ACTION=apply to auto-build it locally, or set SCRIBE_FRONTEND_GAR_IMAGE to an existing tag/digest." >&2
    return 1
  fi

  build_frontend_gar_image "$image_ref"
  "${repo_root}/ci/resolve-gar-image.sh" "$image_ref"
}

select_workspace() {
  local workspace="$1"

  if ! terraform workspace select "$workspace"; then
    terraform workspace new "$workspace" || terraform workspace select "$workspace"
  fi
  export TF_WORKSPACE="$workspace"
}

shared_vault_workspace() {
  local workspace="$1"
  if [ "$workspace" = "prod" ]; then
    printf 'prod'
    return
  fi
  printf 'dev'
}

shared_vault_service_name() {
  local workspace="$1"
  if [ "$workspace" = "prod" ]; then
    printf 'vault-server-prod'
    return
  fi
  printf 'vault-server-dev'
}

fetch_vault_root_token() {
  local shared_workspace="$1"
  local service_name key_bucket kms_key_ring tmpdir

  service_name="$(shared_vault_service_name "$shared_workspace")"
  key_bucket="$(printf '%s-%s-key' "$GCLOUD_PROJECT" "$service_name" | tr '[:upper:]' '[:lower:]' | sed 's/_/-/g; s/\./-/g; s/ /-/g')"
  kms_key_ring="$service_name"
  tmpdir="$(mktemp -d)"

  if ! gcloud storage cp "gs://${key_bucket}/root-token.enc" "$tmpdir/root-token.enc" >/dev/null 2>&1; then
    rm -rf "$tmpdir"
    return 1
  fi

  if ! decode_base64_file "$tmpdir/root-token.enc" "$tmpdir/root-token.ciphertext"; then
    rm -rf "$tmpdir"
    return 1
  fi

  gcloud kms decrypt \
    --key=vault \
    --keyring="$kms_key_ring" \
    --location=global \
    --project="$GCLOUD_PROJECT" \
    --ciphertext-file="$tmpdir/root-token.ciphertext" \
    --plaintext-file="$tmpdir/root-token" >/dev/null

  VAULT_TOKEN="$(tr -d '\r\n' < "$tmpdir/root-token")"
  export VAULT_TOKEN
  rm -rf "$tmpdir"

  if [ -z "${VAULT_TOKEN}" ]; then
    echo "Decrypted Vault token was empty." >&2
    return 1
  fi
}

bootstrap_vault_token() {
  local target_workspace="$1"
  local action="$2"
  local shared_workspace="$3"

  if [ -n "${VAULT_TOKEN:-}" ]; then
    return 0
  fi

  if fetch_vault_root_token "$shared_workspace"; then
    return 0
  fi

  if [ "$target_workspace" = "$shared_workspace" ] && [ "$action" != "destroy" ]; then
    echo "Vault root token for workspace ${shared_workspace} not found; bootstrapping Vault first..."
    terraform apply -auto-approve -target=module.vault "${terraform_vars[@]}"

    echo "Waiting for Vault init to publish the encrypted root token..."
    for _ in $(seq 1 18); do
      if fetch_vault_root_token "$shared_workspace"; then
        return 0
      fi
      sleep 10
    done

    echo "Vault bootstrap finished, but the root token still could not be fetched from GCS/KMS." >&2
    echo "Ensure the current identity can read gs://${GCLOUD_PROJECT}-$(shared_vault_service_name "$shared_workspace")-key/root-token.enc and decrypt KMS key ring $(shared_vault_service_name "$shared_workspace")/vault." >&2
    return 1
  fi

  echo "Vault root token for shared workspace ${shared_workspace} is unavailable." >&2
  echo "Apply the ${shared_workspace} workspace first, or ensure the current identity can read the shared root token object and decrypt its KMS key." >&2
  return 1
}

require_cmd git
require_cmd gcloud
require_cmd terraform
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
target_set="${TF_TARGET_SET:-}"

terraform_targets=()
needs_frontend_gar_image=true
needs_ocr_images=true
case "$target_set" in
  "")
    ;;
  vault)
    needs_frontend_gar_image=false
    needs_ocr_images=false
    terraform_targets+=(
      "-target=module.vault"
      "-target=google_project_iam_member.vault_gcp_auth_service_account_viewer"
      "-target=google_project_iam_member.vault_gcp_auth_service_account_key_admin"
      "-target=vault_mount.secret"
      "-target=vault_mount.keys"
      "-target=vault_policy.vault"
      "-target=vault_auth_backend.gcp"
      "-target=vault_jwt_auth_backend.google_jwt"
      "-target=vault_jwt_auth_backend_role.ci"
      "-target=vault_jwt_auth_backend_role.admin"
      "-target=vault_gcp_auth_backend_role.app"
      "-target=vault_gcp_auth_backend_role.ci"
    )
    ;;
  ocr)
    needs_frontend_gar_image=false
    terraform_targets+=(
      "-target=module.kraken"
      "-target=google_artifact_registry_repository_iam_member.cloud_run_reader"
    )
    ;;
  *)
    echo "Unknown TF_TARGET_SET: ${target_set}" >&2
    exit 1
    ;;
esac

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
  dev)
    target_workspace="dev"
    export TF_VAR_name="scribe-dev"
    export TF_VAR_docker_compose_branch="$branch"
    export TF_VAR_run_snapshots="false"
    fallback_image_tag="ghcr.io/lehigh-university-libraries/scribe:$(sanitize_image_tag "$branch")"
    ocr_image_tag_default="$(sanitize_image_tag "$branch")"
    ;;
  prod)
    target_workspace="prod"
    export TF_VAR_name="scribe"
    export TF_VAR_docker_compose_branch="main"
    export TF_VAR_run_snapshots="true"
    fallback_image_tag="ghcr.io/lehigh-university-libraries/scribe:main"
    ocr_image_tag_default="main"
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
    fallback_image_tag="ghcr.io/lehigh-university-libraries/scribe:$(sanitize_image_tag "$branch")"
    ocr_image_tag_default="$(sanitize_image_tag "$branch")"
    ;;
  *)
    echo "Unknown environment: $environment" >&2
    usage
    exit 1
    ;;
esac

image_tag="${SCRIBE_API_IMAGE:-$fallback_image_tag}"
frontend_tag="$(printf '%s' "${image_tag##*:}" | tr '[:upper:]' '[:lower:]')"
frontend_image_tag="${SCRIBE_FRONTEND_IMAGE:-ghcr.io/lehigh-university-libraries/scribe-frontend:${frontend_tag}}"
frontend_gar_image_tag="${SCRIBE_FRONTEND_GAR_IMAGE:-us-docker.pkg.dev/${GCLOUD_PROJECT}/internal/scribe-frontend:${frontend_tag}}"
if [ "$needs_frontend_gar_image" != "true" ]; then
  frontend_gar_image_tag=""
fi

case "$action" in
  plan|apply|destroy) ;;
  *)
    echo "Unknown action: $action" >&2
    usage
    exit 1
    ;;
esac

if [ "$action" != "destroy" ] && [ "$needs_frontend_gar_image" = "true" ] && [ -n "$frontend_gar_image_tag" ]; then
  frontend_gar_image_tag="$(resolve_frontend_gar_image "$frontend_gar_image_tag" "$action")"
fi

cd "$(dirname "$0")"

ocr_images_json="${SCRIBE_OCR_IMAGES_JSON:-}"
if [ "$needs_ocr_images" != "true" ]; then
  ocr_images_json='{}'
elif [ "$action" != "destroy" ] && [ -z "$ocr_images_json" ]; then
  include_ollama="false"
  if [ "$environment" = "prod" ]; then
    include_ollama="true"
  fi
  ocr_auto_build_missing="false"
  if [ "$action" = "apply" ]; then
    ocr_auto_build_missing="true"
  fi
  ocr_images_json="$(
    WORKSPACE_SLUG="$target_workspace" \
    IMAGE_TAG="${SCRIBE_OCR_IMAGE_TAG:-$ocr_image_tag_default}" \
    INCLUDE_OLLAMA="$include_ollama" \
    AUTO_BUILD_MISSING="$ocr_auto_build_missing" \
    "$repo_root/ci/generate-ocr-images-map.sh"
  )"
fi
if [ -z "$ocr_images_json" ]; then
  ocr_images_json='{}'
fi

terraform_vars=(
  "-var=project_id=${GCLOUD_PROJECT}"
  "-var=terraform_state_bucket=${TF_STATE_BUCKET}"
  "-var=name=${TF_VAR_name}"
  "-var=docker_compose_branch=${TF_VAR_docker_compose_branch}"
  "-var=run_snapshots=${TF_VAR_run_snapshots}"
  "-var=api_image=${image_tag}"
  "-var=frontend_image=${frontend_image_tag}"
  "-var=frontend_gar_image=${frontend_gar_image_tag}"
  "-var=ocr_service_images=${ocr_images_json}"
)

if [ -n "${ALLOWED_IPS:-}" ]; then
  terraform_vars+=("-var=allowed_ips=${ALLOWED_IPS}")
fi

if [ -n "${ALLOWED_SSH_IPV4:-}" ]; then
  terraform_vars+=("-var=allowed_ssh_ipv4=${ALLOWED_SSH_IPV4}")
fi

terraform init -upgrade \
  -backend-config="bucket=${TF_STATE_BUCKET}" \
  -backend-config="prefix=scribe"

select_workspace "$target_workspace"

shared_vault_workspace_name="$(shared_vault_workspace "$target_workspace")"
bootstrap_vault_token "$target_workspace" "$action" "$shared_vault_workspace_name"

if [ "$action" != "destroy" ]; then
  terraform validate
fi

case "$action" in
  plan)
    terraform plan "${terraform_vars[@]}" "${terraform_targets[@]}"
    ;;
  apply)
    terraform apply -auto-approve "${terraform_vars[@]}" "${terraform_targets[@]}"
    ;;
  destroy)
    terraform destroy -auto-approve "${terraform_vars[@]}" "${terraform_targets[@]}"
    ;;
esac
