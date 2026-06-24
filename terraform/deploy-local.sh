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
  VAULT_ADMIN_EMAILS      Optional Terraform list(string) for vault_admin_emails, e.g. ["you@example.edu"].
  VAULT_CI_SERVICE_ACCOUNT_EMAILS  Optional Terraform list(string) for vault_ci_service_account_emails, e.g. ["github@project.iam.gserviceaccount.com"].
  VAULT_TOKEN            Optional one-time Vault token. Normal local runs use Google JWT login instead.
  VAULT_BOOTSTRAP_MODE   Vault auth bootstrap mode: jwt (default), root-token, or jwt-or-root-token.
  VAULT_ROOT_TOKEN_OBJECT  GCS object holding the base64-wrapped encrypted Vault root token. Defaults to gs://${GCLOUD_PROJECT}-vault-server-dev-key/root-token.enc or gs://${GCLOUD_PROJECT}-vault-server-prod-key/root-token.enc based on the shared Vault workspace.
  VAULT_ROOT_TOKEN_KMS_LOCATION  KMS location used to decrypt the stored Vault root token. Defaults to global.
  VAULT_ROOT_TOKEN_KMS_KEYRING   KMS key ring used to decrypt the stored Vault root token. Defaults to vault-server-dev or vault-server-prod based on the shared Vault workspace.
  VAULT_ROOT_TOKEN_KMS_KEY       KMS key name used to decrypt the stored Vault root token. Defaults to vault.

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

sanitize_image_tag() {
  printf '%s' "$1" | sed 's/[^a-zA-Z0-9._-]//g' | awk '{print substr($0, length($0)-120)}' | tr '[:upper:]' '[:lower:]'
}

vault_jwt_role_slug() {
  printf '%s' "$1" | sed 's/@/-at-/g; s/\./-/g'
}

resolve_terraform_region() {
  if [ -n "${SCRIBE_REGION:-}" ]; then
    printf '%s\n' "$SCRIBE_REGION"
    return 0
  fi

  if [ -n "${TF_VAR_region:-}" ]; then
    printf '%s\n' "$TF_VAR_region"
    return 0
  fi

  printf 'us-east5\n'
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
  local zone backend_origin iiif_origin

  zone="$(resolve_terraform_zone)"
  backend_origin="http://${TF_VAR_name}.${zone}.c.${GCLOUD_PROJECT}.internal"
  iiif_origin="${backend_origin}:8081"

  echo "GAR image missing for frontend sidecar; building and pushing ${image_ref} with backend origin ${backend_origin} and IIIF origin ${iiif_origin}..." >&2
  "${repo_root}/ci/build-push-gar-image.sh" \
    --image "$image_ref" \
    --context "$repo_root" \
    --file "${repo_root}/Dockerfile.frontend" \
    --platform "linux/amd64" \
    --build-arg "SCRIBE_FRONTEND_BACKEND_ORIGIN=${backend_origin}" \
    --build-arg "SCRIBE_FRONTEND_IIIF_ORIGIN=${iiif_origin}"
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

shared_vault_kms_keyring() {
  local workspace="$1"
  shared_vault_service_name "$workspace"
}

shared_vault_root_token_object() {
  local workspace="$1"
  printf 'gs://%s-%s-key/root-token.enc' "$GCLOUD_PROJECT" "$(shared_vault_service_name "$workspace")"
}

download_vault_root_token() {
  local shared_workspace="$1"
  local object_path kms_location kms_keyring kms_key tmpdir enc_file decoded_file plain_file
  local vault_token=""

  object_path="${VAULT_ROOT_TOKEN_OBJECT:-$(shared_vault_root_token_object "$shared_workspace")}"
  kms_location="${VAULT_ROOT_TOKEN_KMS_LOCATION:-global}"
  kms_keyring="${VAULT_ROOT_TOKEN_KMS_KEYRING:-$(shared_vault_kms_keyring "$shared_workspace")}"
  kms_key="${VAULT_ROOT_TOKEN_KMS_KEY:-vault}"

  require_cmd base64

  tmpdir="$(mktemp -d)"
  enc_file="${tmpdir}/root-token.enc"
  decoded_file="${tmpdir}/root-token.dc"
  plain_file="${tmpdir}/root-token"

  if ! gcloud storage cp "$object_path" "$enc_file" >/dev/null; then
    rm -rf "$tmpdir"
    echo "Failed to download Vault root token object ${object_path}." >&2
    return 1
  fi

  if ! base64 --decode <"$enc_file" >"$decoded_file" 2>/dev/null \
    && ! base64 -D -i "$enc_file" -o "$decoded_file" 2>/dev/null; then
    rm -rf "$tmpdir"
    echo "Failed to base64-decode Vault root token object ${object_path}." >&2
    return 1
  fi

  if ! gcloud kms decrypt \
    --key "$kms_key" \
    --keyring "$kms_keyring" \
    --location "$kms_location" \
    --project "${GCLOUD_PROJECT}" \
    --ciphertext-file "$decoded_file" \
    --plaintext-file "$plain_file" >/dev/null; then
    rm -rf "$tmpdir"
    echo "Failed to decrypt Vault root token with KMS key ${kms_keyring}/${kms_key} in ${kms_location}." >&2
    return 1
  fi

  vault_token="$(tr -d '\n' < "$plain_file")"
  rm -rf "$tmpdir"

  if [ -z "$vault_token" ]; then
    echo "Downloaded Vault root token from ${object_path}, but the decrypted token was empty." >&2
    return 1
  fi

  VAULT_TOKEN="$vault_token"
  export VAULT_TOKEN
}

login_vault_jwt_token() {
  local shared_workspace="$1"
  local role_prefix="$2"
  local service_name region vault_addr account role_slug role_name access_token id_token payload response token

  service_name="$(shared_vault_service_name "$shared_workspace")"
  region="$(resolve_terraform_region)"

  if ! vault_addr="$(gcloud run services describe "$service_name" --region "$region" --format='value(status.url)' 2>/dev/null)"; then
    return 1
  fi
  if [ -z "$vault_addr" ]; then
    return 1
  fi

  account="$(gcloud config get-value account 2>/dev/null || true)"
  if [ -z "$account" ]; then
    return 1
  fi
  role_slug="$(vault_jwt_role_slug "$account")"
  role_name="${role_prefix}-${role_slug}"

  access_token="$(gcloud auth print-access-token)"
  # For local Vault login we use the active gcloud user account, not a
  # service account. gcloud only supports --audiences for service accounts, and
  # the Vault admin role already allows the Google OAuth client ID audience
  # emitted for user tokens.
  id_token="$(gcloud auth print-identity-token "$account" 2>/dev/null || true)"
  id_token="$(printf '%s' "$id_token" | tr -d '\n' | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
  if [ -z "$id_token" ]; then
    echo "Failed to mint a Google ID token for ${account}. Run gcloud auth login again or export VAULT_TOKEN explicitly." >&2
    return 1
  fi
  payload="$(jq -cn --arg role "$role_name" --arg jwt "$id_token" '{role: $role, jwt: $jwt}')"

  if ! response="$(curl -fsS \
    -H "X-Admin-Token: ${access_token}" \
    -H "Content-Type: application/json" \
    --data "$payload" \
    "${vault_addr%/}/v1/auth/google-jwt/login")"; then
    return 1
  fi
  if ! token="$(jq -er '.auth.client_token' <<<"$response")"; then
    return 1
  fi

  VAULT_TOKEN="$token"
  export VAULT_TOKEN
}

login_vault_admin_token() {
  local shared_workspace="$1"

  if login_vault_jwt_token "$shared_workspace" "break-glass-admin"; then
    return 0
  fi

  login_vault_jwt_token "$shared_workspace" "admin"
}

bootstrap_vault_token() {
  local target_workspace="$1"
  local action="$2"
  local shared_workspace="$3"
  local bootstrap_mode

  if [ -n "${VAULT_TOKEN:-}" ]; then
    return 0
  fi

  bootstrap_mode="${VAULT_BOOTSTRAP_MODE:-jwt}"
  case "$bootstrap_mode" in
    root-token)
      download_vault_root_token "$shared_workspace"
      return 0
      ;;
    jwt|jwt-or-root-token)
      ;;
    *)
      echo "Unknown VAULT_BOOTSTRAP_MODE: ${bootstrap_mode}" >&2
      echo "Expected one of: jwt, root-token, jwt-or-root-token" >&2
      return 1
      ;;
  esac

  if login_vault_admin_token "$shared_workspace"; then
    return 0
  fi

  if [ "$target_workspace" = "$shared_workspace" ] && [ "$action" = "apply" ]; then
    echo "Vault JWT login for workspace ${shared_workspace} is not ready; applying the Vault service shell first..."
    terraform apply -auto-approve -target=module.vault "${terraform_vars[@]}"
    if login_vault_admin_token "$shared_workspace"; then
      return 0
    fi
  fi

  if [ "$bootstrap_mode" = "jwt-or-root-token" ]; then
    echo "Vault JWT login failed; falling back to the stored root token for workspace ${shared_workspace}..." >&2
    download_vault_root_token "$shared_workspace"
    return 0
  fi

  echo "Vault login for shared workspace ${shared_workspace} failed." >&2
  echo "Use an existing google-jwt admin role, export a one-time VAULT_TOKEN, or rerun with VAULT_BOOTSTRAP_MODE=root-token." >&2
  return 1
}

require_cmd git
require_cmd gcloud
require_cmd curl
require_cmd jq
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
export DOCKER_DEFAULT_PLATFORM="${DOCKER_DEFAULT_PLATFORM:-linux/amd64}"
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
      "-target=google_service_account_iam_member.vault_gcp_auth_app_service_account_viewer"
      "-target=google_service_account_iam_member.vault_gcp_auth_instance_service_account_viewer"
      "-target=google_service_account_iam_member.vault_gcp_auth_app_service_account_key_admin"
      "-target=google_service_account_iam_member.vault_gcp_auth_instance_service_account_key_admin"
      "-target=vault_mount.secret"
      "-target=vault_policy.vault"
      "-target=vault_audit.stdout"
      "-target=vault_auth_backend.gcp"
      "-target=vault_jwt_auth_backend.google_jwt"
      "-target=vault_jwt_auth_backend_role.ci"
      "-target=vault_jwt_auth_backend_role.admin"
      "-target=vault_jwt_auth_backend_role.admin_break_glass"
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

if [ -n "${VAULT_ADMIN_EMAILS:-}" ]; then
  terraform_vars+=("-var=vault_admin_emails=${VAULT_ADMIN_EMAILS}")
fi

if [ -n "${VAULT_CI_SERVICE_ACCOUNT_EMAILS:-}" ]; then
  terraform_vars+=("-var=vault_ci_service_account_emails=${VAULT_CI_SERVICE_ACCOUNT_EMAILS}")
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
    if [ "${TF_PLAN_DETAILED_EXITCODE:-}" = "true" ]; then
      terraform plan -detailed-exitcode "${terraform_vars[@]}" "${terraform_targets[@]}"
    else
      terraform plan "${terraform_vars[@]}" "${terraform_targets[@]}"
    fi
    ;;
  apply)
    terraform apply -auto-approve "${terraform_vars[@]}" "${terraform_targets[@]}"
    ;;
  destroy)
    terraform destroy -auto-approve "${terraform_vars[@]}" "${terraform_targets[@]}"
    ;;
esac
