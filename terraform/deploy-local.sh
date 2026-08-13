#!/usr/bin/env bash

set -euo pipefail
repo_root="$(cd "$(dirname "$0")/.." && pwd)"
# shellcheck source=scripts/vault-retry.sh
source "$repo_root/scripts/vault-retry.sh"

usage() {
  cat <<'EOF'
Usage:
  terraform/deploy-local.sh dev <plan|apply|refresh|normalize-moves|destroy> [--branch BRANCH]
  terraform/deploy-local.sh prod <plan|apply|refresh|normalize-moves|destroy> [--branch GIT_REF]
  terraform/deploy-local.sh preview <plan|apply|refresh|normalize-moves|destroy> [--branch BRANCH] --pr-number NUMBER

Required environment:
  GCLOUD_PROJECT    GCP project ID

Optional environment:
  TF_STATE_BUCKET      GCS bucket used for Terraform remote state. Defaults to ${GCLOUD_PROJECT}-terraform
  ALLOWED_IPS         Terraform list(string), e.g. ["203.0.113.10/32"]
  ALLOWED_SSH_IPV4    Terraform list(string), e.g. ["203.0.113.10/32"]
  ALLOWED_SSH_IPV6    Terraform list(string), e.g. ["2001:db8::/64"]
  DEV_EXTERNAL_OCR_IMPERSONATORS  Dev-only JSON array of explicit user: or group: IAM members allowed to impersonate scribe-dev-external. Must be [] outside dev.
  SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS  Set to true only for the protected recover-destroy workflow after an interrupted preview teardown removed deployment_inputs.
  SCRIBE_API_IMAGE    Exact backend image to inject into Terraform api_image
  SCRIBE_FRONTEND_GAR_IMAGE  Exact frontend image to inject into Terraform frontend_gar_image (GAR, used by the Cloud Run sidecar). When unset, local apply resolves the default tag and auto-builds it if missing.
  SCRIBE_BROWSER_READINESS_IMAGE  Protected digest-pinned preview browser readiness image. Empty outside managed preview applies.
  SCRIBE_OCR_IMAGES_JSON  Pre-resolved JSON map of OCR service_key -> GAR digest ref. For plan/apply only; refresh and destroy reload the recorded map from Terraform state.
  SCRIBE_PREVIEW_MACHINE_TYPE  Preview-only reviewed machine profile: n2d-standard-2 (default) or e2-medium. Refresh and destroy reload the recorded value from Terraform state.
  SCRIBE_DATA_GENERATION  Reviewed persistence generation. Defaults to canonical-v2; refresh and destroy always reload the recorded value from Terraform state.
  SCRIBE_OCR_IMAGE_TAG    Tag to resolve against when generating the OCR image map locally. Defaults to the immutable --branch commit SHA for production and preview, and the branch slug for development.
  SCRIBE_ZONE            Optional zone override used when locally building the frontend GAR sidecar. Falls back to TF_VAR_zone, terraform/terraform.tfvars, then us-east5-c for previews or us-east5-b otherwise.
  SCRIBE_REGION          Optional region override. Falls back to TF_VAR_region, then us-east5.
  VAULT_ADMIN_EMAILS      Optional Terraform list(string) for vault_admin_emails, e.g. ["you@example.edu"].
  VAULT_CI_SERVICE_ACCOUNT_EMAILS  Optional Terraform list(string) for vault_ci_service_account_emails, e.g. ["github@project.iam.gserviceaccount.com"].
  VAULT_TOKEN            Optional one-time Vault token. Normal local runs use Google JWT login instead.
  VAULT_BOOTSTRAP_MODE   Vault auth bootstrap mode: jwt (local default), root-token, or jwt-or-root-token (protected CI default).
  VAULT_ROOT_TOKEN_OBJECT  GCS object holding the base64-wrapped encrypted Vault root token. Defaults to gs://${GCLOUD_PROJECT}-vault-server-dev-key/root-token.enc or gs://${GCLOUD_PROJECT}-vault-server-prod-key/root-token.enc based on the shared Vault workspace.
  VAULT_ROOT_TOKEN_KMS_LOCATION  KMS location used to decrypt the stored Vault root token. Defaults to global.
  VAULT_ROOT_TOKEN_KMS_KEYRING   KMS key ring used to decrypt the stored Vault root token. Defaults to vault-server-dev or vault-server-prod based on the shared Vault workspace.
  VAULT_ROOT_TOKEN_KMS_KEY       KMS key name used to decrypt the stored Vault root token. Defaults to vault.
Notes:
  - Dev mode uses Terraform workspace dev and site name scribe-dev.
  - Preview mode matches GitHub Actions naming only when --pr-number is supplied.
  - Preview and production require --branch to be a full commit SHA.
  - Their default backend, frontend, and OCR tags use that exact SHA and are resolved to immutable digests before Terraform runs.
  - Explicit SCRIBE_*_IMAGE values must still resolve to immutable image digests.
  - Local apply auto-builds missing frontend/OCR GAR images needed by the selected workspace.
  - Refresh replays the selected workspace's exact recorded deployment-input schema and runs a guarded full-graph refresh-only apply.
  - Normalize-moves updates only reviewed Scribe and cloud-compose state addresses and never refreshes providers or changes infrastructure.
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

sha256_text() {
  local value="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s' "$value" | sha256sum | awk '{print $1}'
    return 0
  fi
  if command -v shasum >/dev/null 2>&1; then
    printf '%s' "$value" | shasum -a 256 | awk '{print $1}'
    return 0
  fi
  echo "sha256sum or shasum is required to identify the protected preview browser subnet." >&2
  return 1
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

  if [ "${environment:-}" = "preview" ]; then
    printf 'us-east5-c\n'
    return 0
  fi

  printf 'us-east5-b\n'
}

apply_preview_terraform_with_capacity_retry() {
  # Reviewed preview_zone allowlist from terraform/variables.tf. A single
  # zone can run out of capacity for whole minutes at a time, so cycle
  # through all of them on retry instead of repeatedly hammering the same
  # exhausted one.
  local zone_candidates=(us-east5-a us-east5-b us-east5-c)
  local retry_delay_seconds=30
  local attempt=1
  local max_attempts="${#zone_candidates[@]}"
  local tmp_log zone_for_attempt
  tmp_log="$(mktemp)"
  while true; do
    zone_for_attempt="${zone_candidates[$(((attempt - 1) % ${#zone_candidates[@]}))]}"
    if terraform apply -auto-approve "$@" "-var=preview_zone=${zone_for_attempt}" 2>&1 | tee "$tmp_log"; then
      rm -f "$tmp_log"
      return 0
    fi
    local status=${PIPESTATUS[0]}
    if [ "$attempt" -ge "$max_attempts" ] || ! grep -q "does not have enough resources available" "$tmp_log"; then
      rm -f "$tmp_log"
      return "$status"
    fi
    echo "Preview zone ${zone_for_attempt} is temporarily out of GCP capacity (attempt ${attempt}/${max_attempts}); retrying in the next reviewed zone in ${retry_delay_seconds}s..." >&2
    attempt=$((attempt + 1))
    sleep "$retry_delay_seconds"
  done
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
  local action="$2"
  local listed_workspace normalized_workspace workspace_inventory

  if terraform workspace select "$workspace"; then
    export TF_WORKSPACE="$workspace"
    return 0
  fi

  if [ "$action" = "destroy" ] \
    && [ "${environment:-}" = "preview" ] \
    && [ "${recover_preview_destroy_inputs:-false}" = "true" ] \
    && [[ "$workspace" =~ ^pr-[0-9]+$ ]]; then
    if ! workspace_inventory="$(terraform workspace list -no-color)"; then
      echo "Unable to inventory Terraform workspaces after selection failed; refusing to infer that ${workspace} was destroyed." >&2
      return 1
    fi
    while IFS= read -r listed_workspace; do
      normalized_workspace="$(sed -E 's/^[*[:space:]]+//; s/[[:space:]]+$//' <<<"$listed_workspace")"
      if [ "$normalized_workspace" = "$workspace" ]; then
        echo "Terraform workspace ${workspace} still exists but could not be selected; refusing recovery." >&2
        return 1
      fi
    done <<<"$workspace_inventory"

    selected_workspace_exists=false
    echo "Terraform workspace ${workspace} is already absent; protected preview teardown recovery has no Terraform state left to destroy."
    return 0
  fi

  if [ "$action" = "refresh" ] || [ "$action" = "normalize-moves" ] || [ "$action" = "destroy" ]; then
    echo "Terraform workspace ${workspace} does not exist; refusing to ${action} by creating new state." >&2
    return 1
  fi

  terraform workspace new "$workspace" || terraform workspace select "$workspace"
  export TF_WORKSPACE="$workspace"
}

resolve_vault_owner_state() {
  local state_list

  # Capture the complete listing before matching it. With pipefail, piping this
  # command into grep -q can turn grep's early success into Terraform SIGPIPE
  # and falsely classify an existing owner workspace as empty.
  if ! state_list="$(terraform state list 2>/dev/null)"; then
    echo "Unable to inspect Terraform state for the shared Vault owner." >&2
    return 1
  fi

  if grep -Eq '^module\.vault\[0\]\.' <<<"$state_list"; then
    printf 'present\n'
  else
    printf 'absent\n'
  fi
}

reject_state_maintenance_cli_args() {
  local action_label variable_name

  action_label="Refresh"
  if [ "$action" = "normalize-moves" ]; then
    action_label="Normalize-moves"
  fi

  while IFS= read -r variable_name; do
    case "$variable_name" in
      TF_CLI_ARGS*)
        if [ -n "${!variable_name}" ]; then
          echo "${action_label} refuses non-empty ${variable_name}; remove Terraform CLI argument overrides and retry." >&2
          return 1
        fi
        ;;
    esac
  done < <(compgen -e)
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

resolve_shared_vault_address() {
  local shared_workspace="$1"
  local resolution

  resolution="$(
    env -u VAULT_TOKEN -u VAULT_ADDR \
      SCRIBE_REGION="$(resolve_terraform_region)" \
      "$repo_root/ci/resolve-shared-vault.sh" "$shared_workspace" --allow-runtime-identity-drift
  )" || return 1
  jq -er '.vault_addr' <<<"$resolution"
}

export_preview_vault_reconciliation_inputs() {
  local shared_workspace="$1"
  local resolution

  resolution="$(
    env -u VAULT_TOKEN -u VAULT_ADDR \
      SCRIBE_REGION="$(resolve_terraform_region)" \
      "$repo_root/ci/resolve-shared-vault.sh" "$shared_workspace"
  )" || return 1
  VAULT_ADDR="$(jq -er '.vault_addr' <<<"$resolution")" || return 1
  GCLOUD_PROJECT_NUMBER="$(jq -er '.project_number' <<<"$resolution")" || return 1
  export VAULT_ADDR GCLOUD_PROJECT_NUMBER
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

register_vault_token_mask() {
  if [ "${GITHUB_ACTIONS:-}" != "true" ]; then
    return 0
  fi
  if [ -z "${VAULT_TOKEN:-}" ]; then
    echo "Cannot register an empty Vault token with the GitHub runner." >&2
    return 1
  fi

  # capture-command-log.sh gives the wrapped deployment a runner-only file
  # descriptor. Register there first, then repeat the record on captured
  # stdout so the helper can redact the same literal from the artifact. The
  # leading newline makes both records independent of unterminated output from
  # gcloud, curl, or a provider.
  case "${SCRIBE_GITHUB_COMMAND_FD:-}" in
    "") ;;
    9) printf '\n::add-mask::%s\n' "$VAULT_TOKEN" >&9 ;;
    *)
      echo "SCRIBE_GITHUB_COMMAND_FD must be unset or the protected descriptor 9." >&2
      return 1
      ;;
  esac
  printf '\n::add-mask::%s\n' "$VAULT_TOKEN"
}

login_vault_jwt_token() {
  local shared_workspace="$1"
  local role_prefix="$2"
  local vault_addr account role_slug role_name access_token id_token payload response token

  if ! vault_addr="$(resolve_shared_vault_address "$shared_workspace")"; then
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
  local bootstrap_mode vault_owner_state=""
  local first_owner_apply=false

  if [ "${target_set:-}" = "vault-preview-runtime" ] \
    && [ "$target_workspace" = "$shared_workspace" ] \
    && [ "$action" = "apply" ]; then
    vault_owner_state="$(resolve_vault_owner_state)" || return 1
    if [ "$vault_owner_state" = "absent" ]; then
      echo "The shared dev Vault owner is absent; preview-runtime maintenance cannot bootstrap it outside its reviewed reconciliation boundary." >&2
      echo "Apply and initialize the complete dev owner workspace before retrying." >&2
      return 1
    fi
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

  # A clean owner workspace has no Vault endpoint, init object, or JWT auth
  # role yet. Materialize the service and wait for the init job before making
  # any Vault login or root-token recovery request. This targeted apply is only
  # a first-owner bootstrap; routine applies go directly to the full graph.
  if [ "$target_workspace" = "$shared_workspace" ] && [ "$action" = "apply" ]; then
    if [ -z "$vault_owner_state" ]; then
      vault_owner_state="$(resolve_vault_owner_state)" || return 1
    fi
    if [ "$vault_owner_state" = "absent" ]; then
      first_owner_apply=true
      echo "Vault owner workspace ${shared_workspace} is empty; applying and initializing the Vault service shell first..."
      terraform apply -auto-approve -target=module.vault "${terraform_vars[@]}"
    fi
  fi

  if [ -n "${VAULT_TOKEN:-}" ]; then
    return 0
  fi

  if [ "$first_owner_apply" = "true" ] && [ "$bootstrap_mode" = "root-token" ]; then
    download_vault_root_token "$shared_workspace"
    return 0
  fi

  if login_vault_admin_token "$shared_workspace"; then
    return 0
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

bootstrap_browser_readiness_ipv6() {
  local target_workspace="$1"
  local action="$2"

  # Scoped to preview workspaces only: prod's browser_readiness subnet
  # already completed this transition in an earlier apply, and prod's apply
  # sequence is covered by a strict mocked-terraform invocation-order
  # contract (ci/vault-first-bootstrap-contract_test.sh) that a prod-scoped
  # extra targeted apply here would need to be taught about.
  case "$target_workspace" in
    pr-*) ;;
    *) return 0 ;;
  esac

  if [ "$action" != "apply" ] \
    && { [ "$action" != "plan" ] || [ "${TF_APPLY_PENDING:-}" != "true" ]; }; then
    return 0
  fi

  if [ -n "$target_set" ]; then
    return 0
  fi

  # The Google provider does not mark external_ipv6_prefix as pending
  # recomputation when stack_type/ipv6_access_type change in place, so the
  # postcondition guarding it in readiness.tf evaluates against stale
  # (pre-transition) state during planning itself -- on every Terraform
  # invocation, targeted or not, plan or apply, since Terraform always halts
  # on a failing plan-time postcondition before ever calling the provider's
  # real update. There is no way to reach a genuine post-apply read through
  # Terraform alone on an environment's first IPV4_ONLY -> IPV4_IPV6
  # transition. Instead, read the subnet's existing recorded name straight
  # from state (a plain state read; no plan, no provider call, no
  # postcondition) and, only when it still needs the transition, apply it
  # directly via gcloud so real infrastructure is already migrated by the
  # time Terraform's own plan runs and refreshes. A no-op once the subnet has
  # already transitioned, and a no-op when the subnet doesn't exist in state
  # yet (a brand-new resource is created with the right stack_type/
  # ipv6_access_type from the start; only the in-place update path hits this
  # provider gap). TF_APPLY_PENDING scopes this to plan calls CI knows are
  # immediately followed by a real apply, so a standalone review-only plan
  # never mutates infrastructure.
  local subnet_state existing_name existing_stack_type region

  subnet_state="$(terraform show -json 2>/dev/null | jq -r '
    .values.root_module.resources[]? |
    select(.address == "google_compute_subnetwork.browser_readiness[0]") |
    [.values.name, .values.stack_type] | @tsv
  ')"
  [ -n "$subnet_state" ] || return 0

  existing_name="$(cut -f1 <<<"$subnet_state")"
  existing_stack_type="$(cut -f2 <<<"$subnet_state")"
  [ "$existing_stack_type" != "IPV4_IPV6" ] || return 0

  region="$(resolve_terraform_region)"
  echo "Transitioning ${existing_name} to dual-stack external IPv6 directly via gcloud before Terraform plans it..." >&2
  gcloud compute networks subnets update "$existing_name" \
    --project="$GCLOUD_PROJECT" \
    --region="$region" \
    --stack-type=IPV4_IPV6 \
    --ipv6-access-type=EXTERNAL
}

vault_token_ready_once() {
  local vault_addr="$1"
  local access_token="$2"

  # Discard both Vault's response and curl diagnostics. The shared retry helper
  # emits only a stable operation label and attempt count, so neither token nor
  # an upstream response body can reach deployment logs.
  curl -fsS \
    --connect-timeout 5 \
    --max-time 10 \
    -o /dev/null \
    -H "X-Vault-Token: ${VAULT_TOKEN}" \
    -H "X-Admin-Token: ${access_token}" \
    "${vault_addr%/}/v1/auth/token/lookup-self" >/dev/null 2>&1
}

wait_for_vault_token_readiness() {
  local shared_workspace="$1"
  local vault_addr access_token

  if [ -z "${VAULT_TOKEN:-}" ]; then
    echo "Vault token readiness requires a non-empty Vault token." >&2
    return 1
  fi

  vault_addr="$(resolve_shared_vault_address "$shared_workspace")" || return 1

  if ! access_token="$(
    unset VAULT_TOKEN VAULT_ADDR
    gcloud auth print-access-token 2>/dev/null
  )"; then
    echo "Failed to mint the Google access token required by the Vault service." >&2
    return 1
  fi
  access_token="$(printf '%s' "$access_token" | tr -d '\r\n')"
  if [ -z "$access_token" ]; then
    echo "The Google access token required by the Vault service was empty." >&2
    return 1
  fi

  if ! vault_retry "Vault token readiness" \
    vault_token_ready_once "$vault_addr" "$access_token"; then
    echo "Vault did not accept the deployment token; response body withheld." >&2
    return 1
  fi
}

# A preview workflow can supply a Vault token before this process starts.
# Register that inherited value before any external command, including
# Terraform initialization, has an opportunity to mention its environment.
if [ -n "${VAULT_TOKEN:-}" ]; then
  register_vault_token_mask
fi

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
recover_preview_destroy_inputs="${SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS:-false}"

case "$recover_preview_destroy_inputs" in
  true|false) ;;
  *)
    echo "SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS must be true or false." >&2
    exit 1
    ;;
esac

if [ "$action" = "refresh" ] || [ "$action" = "normalize-moves" ]; then
  reject_state_maintenance_cli_args
fi

if [ "$action" = "refresh" ] && [ -n "$target_set" ]; then
  echo "Refresh always runs the full Terraform graph; TF_TARGET_SET must be empty." >&2
  exit 1
fi

if [ "$action" = "normalize-moves" ] && [ -n "$target_set" ]; then
  echo "Normalize-moves manages its own exact Terraform state scope; TF_TARGET_SET must be empty." >&2
  exit 1
fi

terraform_targets=()
needs_frontend_gar_image=true
needs_api_image=true
needs_ocr_images=true
target_plan_verifier=""
preview_runtime_reconciler=false
case "$target_set" in
  "")
    ;;
  vault-ci-identities)
    needs_frontend_gar_image=false
    needs_ocr_images=false
    target_plan_verifier="$repo_root/ci/verify-vault-target-plan.sh"
    terraform_targets+=(
      "-target=module.vault[0].google_storage_bucket_iam_member.bootstrap_key_reader"
      "-target=module.vault[0].google_kms_crypto_key_iam_member.bootstrap_root_token_decrypter"
      "-target=vault_jwt_auth_backend_role.ci"
      "-target=vault_gcp_auth_backend_role.ci"
    )
    ;;
  vault-preview-runtime)
    if [ "$environment" != "dev" ]; then
      echo "The vault-preview-runtime target set is owned only by Terraform workspace dev." >&2
      exit 1
    fi
    if [ "$action" != "plan" ] && [ "$action" != "apply" ]; then
      echo "The vault-preview-runtime target set supports only plan or apply." >&2
      exit 1
    fi
    needs_api_image=false
    needs_frontend_gar_image=false
    needs_ocr_images=false
    preview_runtime_reconciler=true
    ;;
  ocr)
    needs_frontend_gar_image=false
    terraform_targets+=(
      "-target=terraform_data.dev_external_ocr_workspace_guard"
      "-target=google_service_account.dev_external_ocr"
      "-target=google_service_account_iam_member.dev_external_ocr_token_creator"
      "-target=module.kraken"
      "-target=module.ollama_services"
      "-target=google_artifact_registry_repository_iam_member.cloud_run_reader"
      "-target=google_cloud_run_v2_service_iam_member.kraken_invoker"
      "-target=google_cloud_run_v2_service_iam_member.ollama_invoker"
      "-target=google_cloud_run_v2_service_iam_member.ollama_preview_invoker"
    )
    ;;
  *)
    echo "Unknown TF_TARGET_SET: ${target_set}" >&2
    exit 1
    ;;
esac

branch="$(git symbolic-ref --quiet --short HEAD 2>/dev/null || git rev-parse HEAD)"
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
    export TF_VAR_docker_compose_branch="$branch"
    export TF_VAR_run_snapshots="true"
    fallback_image_tag="ghcr.io/lehigh-university-libraries/scribe:${branch}"
    ocr_image_tag_default="$branch"
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
    case "$action" in
      destroy|refresh|normalize-moves)
        unset TF_VAR_preview_machine_type
        ;;
      *)
        case "${SCRIBE_PREVIEW_MACHINE_TYPE:-n2d-standard-2}" in
          e2-medium|n2d-standard-2)
            export TF_VAR_preview_machine_type="${SCRIBE_PREVIEW_MACHINE_TYPE:-n2d-standard-2}"
            ;;
          *)
            echo "SCRIBE_PREVIEW_MACHINE_TYPE must be an explicitly reviewed preview profile: e2-medium or n2d-standard-2." >&2
            exit 1
            ;;
        esac
        ;;
    esac
    fallback_image_tag="ghcr.io/lehigh-university-libraries/scribe:$(sanitize_image_tag "$branch")"
    ocr_image_tag_default="$branch"
    ;;
  *)
    echo "Unknown environment: $environment" >&2
    usage
    exit 1
    ;;
esac

if { [ "$environment" = "prod" ] || [ "$environment" = "preview" ]; } \
  && [ "$action" != "destroy" ] && [ "$action" != "refresh" ] && [ "$action" != "normalize-moves" ]; then
  if [[ ! "$branch" =~ ^[0-9a-f]{40}$ ]]; then
    echo "Production and preview --branch values must be immutable 40-character commit SHAs." >&2
    exit 1
  fi
fi

image_tag="${SCRIBE_API_IMAGE:-$fallback_image_tag}"
if [ "$needs_api_image" != "true" ]; then
  image_tag="ghcr.io/lehigh-university-libraries/scribe@sha256:0000000000000000000000000000000000000000000000000000000000000000"
fi
data_generation="${SCRIBE_DATA_GENERATION:-canonical-v2}"
dev_external_ocr_impersonators="${DEV_EXTERNAL_OCR_IMPERSONATORS:-}"
frontend_tag="$(sanitize_image_tag "$branch")"
frontend_gar_image_tag="${SCRIBE_FRONTEND_GAR_IMAGE:-us-docker.pkg.dev/${GCLOUD_PROJECT}/internal/scribe-frontend:${frontend_tag}}"
browser_readiness_image="${SCRIBE_BROWSER_READINESS_IMAGE:-}"
browser_readiness_subnet_cidr="${TF_VAR_browser_readiness_subnet_cidr:-10.43.0.0/26}"
if [ "$needs_frontend_gar_image" != "true" ]; then
  frontend_gar_image_tag=""
fi

case "$action" in
  plan|apply|refresh|normalize-moves|destroy) ;;
  *)
    echo "Unknown action: $action" >&2
    usage
    exit 1
    ;;
esac

if [ "$recover_preview_destroy_inputs" = "true" ] && {
  [ "$environment" != "preview" ] || [ "$action" != "destroy" ] || [ -n "$target_set" ];
}; then
  echo "Historical destroy-input recovery is restricted to an untargeted preview destroy." >&2
  exit 1
fi

if [ "$action" != "destroy" ] && [ "$action" != "refresh" ] && [ "$action" != "normalize-moves" ] \
  && { [ "$needs_api_image" = "true" ] || [ "$needs_frontend_gar_image" = "true" ] || [ "$needs_ocr_images" = "true" ]; }; then
  require_cmd docker
fi

if [ "$environment" = "prod" ] && [ "$action" = "destroy" ] && [ "${CONFIRM_PRODUCTION_DESTROY:-}" != "scribe-prod-destroy" ]; then
  echo "Production destroy requires CONFIRM_PRODUCTION_DESTROY=scribe-prod-destroy after an approved recovery backup." >&2
  exit 1
fi

if [ "$action" != "destroy" ] && [ "$action" != "refresh" ] && [ "$action" != "normalize-moves" ] \
  && [ "$needs_frontend_gar_image" = "true" ] && [ -n "$frontend_gar_image_tag" ]; then
  frontend_gar_image_tag="$(resolve_frontend_gar_image "$frontend_gar_image_tag" "$action")"
fi

if [ "$action" != "destroy" ] && [ "$action" != "refresh" ] && [ "$action" != "normalize-moves" ] \
  && [ "$needs_api_image" = "true" ]; then
  image_tag="$("${repo_root}/ci/resolve-ghcr-image.sh" "$image_tag")"
fi
if [ "$action" != "destroy" ] && [ "$action" != "refresh" ] && [ "$action" != "normalize-moves" ]; then
  BACKUP_AUDIT_SCOPE=state "${repo_root}/ci/verify-cloud-backups.sh"
  export TF_VAR_terraform_state_backup_audited=true
fi

cd "$(dirname "$0")"

terraform init -lockfile=readonly \
  -backend-config="bucket=${TF_STATE_BUCKET}" \
  -backend-config="prefix=scribe"

selected_workspace_exists=true
select_workspace "$target_workspace" "$action"
if [ "$selected_workspace_exists" = "false" ]; then
  exit 0
fi

if [ "$action" = "normalize-moves" ]; then
  BACKUP_AUDIT_SCOPE=state "$repo_root/ci/verify-cloud-backups.sh"
  "$repo_root/ci/normalize-terraform-moved-state.sh" "$repo_root/terraform"
  exit 0
fi

ocr_images_json="${SCRIBE_OCR_IMAGES_JSON:-}"
if [ "$action" = "destroy" ] || [ "$action" = "refresh" ]; then
  if ! stored_deployment_inputs="$(terraform output -json deployment_inputs 2>/dev/null)"; then
    if [ "$action" = "destroy" ] && [ "$environment" = "preview" ] \
      && [ "$recover_preview_destroy_inputs" = "true" ]; then
      echo "Current preview state has no deployment_inputs; recovering the newest valid same-lineage value from versioned state history." >&2
      if ! stored_deployment_inputs="$("$repo_root/ci/recover-preview-destroy-inputs.sh")"; then
        echo "Historical preview destroy-input recovery failed; current state was not modified." >&2
        exit 1
      fi
    else
      echo "The selected workspace has no readable deployment_inputs; refusing to ${action} without its prior immutable release inputs." >&2
      echo "Inspect and recover the remote workspace state before retrying ${action}." >&2
      exit 1
    fi
  fi
  deployment_inputs_resolver="$repo_root/ci/resolve-destroy-inputs.sh"
  if [ "$action" = "refresh" ]; then
    deployment_inputs_resolver="$repo_root/ci/resolve-refresh-inputs.sh"
  fi
  if ! stored_deployment_inputs="$({
    printf '%s\n' "$stored_deployment_inputs" |
      GCLOUD_PROJECT="$GCLOUD_PROJECT" "$deployment_inputs_resolver"
  })"; then
    echo "Inspect and recover the remote workspace state before retrying ${action}." >&2
    exit 1
  fi
  TF_VAR_docker_compose_branch="$(jq -r '.docker_compose_sha' <<<"$stored_deployment_inputs")"
  export TF_VAR_docker_compose_branch
  image_tag="$(jq -r '.api_image' <<<"$stored_deployment_inputs")"
  browser_readiness_image="$(jq -r '.browser_readiness_image' <<<"$stored_deployment_inputs")"
  browser_readiness_subnet_cidr="$(jq -r '.configuration.browser_readiness_subnet_cidr' <<<"$stored_deployment_inputs")"
  frontend_gar_image_tag="$(jq -r '.frontend_gar_image' <<<"$stored_deployment_inputs")"
  ocr_images_json="$(jq -c '.ocr_service_images' <<<"$stored_deployment_inputs")"
  data_generation="$(jq -r '.data_generation' <<<"$stored_deployment_inputs")"
  dev_external_ocr_impersonators="$(jq -c '.configuration.dev_external_ocr_impersonators' <<<"$stored_deployment_inputs")"
  TF_VAR_region="$(jq -r '.configuration.region' <<<"$stored_deployment_inputs")"
  TF_VAR_zone="$(jq -r '.configuration.zone' <<<"$stored_deployment_inputs")"
  TF_VAR_preview_machine_type="$(jq -r '.configuration.preview_machine_type' <<<"$stored_deployment_inputs")"
  SCRIBE_REGION="$TF_VAR_region"
  SCRIBE_ZONE="$TF_VAR_zone"
  export SCRIBE_REGION SCRIBE_ZONE TF_VAR_region TF_VAR_zone TF_VAR_preview_machine_type
  if [ "$action" = "refresh" ]; then
    ALLOWED_IPS="$(jq -c '.configuration.allowed_ips' <<<"$stored_deployment_inputs")"
    ALLOWED_SSH_IPV4="$(jq -c '.configuration.allowed_ssh_ipv4' <<<"$stored_deployment_inputs")"
    ALLOWED_SSH_IPV6="$(jq -c '.configuration.allowed_ssh_ipv6' <<<"$stored_deployment_inputs")"
    VAULT_ADMIN_EMAILS="$(jq -c '.configuration.vault_admin_emails' <<<"$stored_deployment_inputs")"
    VAULT_CI_SERVICE_ACCOUNT_EMAILS="$(jq -c '.configuration.vault_ci_service_account_emails' <<<"$stored_deployment_inputs")"
    TF_VAR_backup_restore_service_account_email="$(jq -r '.configuration.backup_restore_service_account_email' <<<"$stored_deployment_inputs")"
    TF_VAR_compose_network_cidr="$(jq -r '.configuration.compose_network_cidr' <<<"$stored_deployment_inputs")"
    TF_VAR_iiif_max_manifest_canvases="$(jq -r '.configuration.iiif_max_manifest_canvases' <<<"$stored_deployment_inputs")"
    TF_VAR_iiif_max_manifest_import_bytes="$(jq -r '.configuration.iiif_max_manifest_import_bytes' <<<"$stored_deployment_inputs")"
    TF_VAR_monitoring_notification_channels="$(jq -c '.configuration.monitoring_notification_channels' <<<"$stored_deployment_inputs")"
    TF_VAR_network_ip_cidr_range="$(jq -r '.configuration.network_ip_cidr_range' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_max_bytes_per_workspace="$(jq -r '.configuration.storage_max_bytes_per_workspace' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_max_bytes_total="$(jq -r '.configuration.storage_max_bytes_total' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_max_images_per_workspace="$(jq -r '.configuration.storage_max_images_per_workspace' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_max_images_total="$(jq -r '.configuration.storage_max_images_total' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_max_items_per_workspace="$(jq -r '.configuration.storage_max_items_per_workspace' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_max_items_total="$(jq -r '.configuration.storage_max_items_total' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_normalization_cache_max_age="$(jq -r '.configuration.storage_normalization_cache_max_age' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_normalization_cache_max_bytes="$(jq -r '.configuration.storage_normalization_cache_max_bytes' <<<"$stored_deployment_inputs")"
    TF_VAR_storage_reservation_ttl="$(jq -r '.configuration.storage_reservation_ttl' <<<"$stored_deployment_inputs")"
    TF_VAR_transcription_max_active_jobs_per_workspace="$(jq -r '.configuration.transcription_max_active_jobs_per_workspace' <<<"$stored_deployment_inputs")"
    export ALLOWED_IPS ALLOWED_SSH_IPV4 ALLOWED_SSH_IPV6 VAULT_ADMIN_EMAILS VAULT_CI_SERVICE_ACCOUNT_EMAILS
    export TF_VAR_backup_restore_service_account_email TF_VAR_compose_network_cidr
    export TF_VAR_iiif_max_manifest_canvases TF_VAR_iiif_max_manifest_import_bytes
    export TF_VAR_monitoring_notification_channels TF_VAR_network_ip_cidr_range
    export TF_VAR_storage_max_bytes_per_workspace TF_VAR_storage_max_bytes_total
    export TF_VAR_storage_max_images_per_workspace TF_VAR_storage_max_images_total
    export TF_VAR_storage_max_items_per_workspace TF_VAR_storage_max_items_total
    export TF_VAR_storage_normalization_cache_max_age TF_VAR_storage_normalization_cache_max_bytes
    export TF_VAR_storage_reservation_ttl TF_VAR_transcription_max_active_jobs_per_workspace
    if [ "$environment" = "prod" ]; then
      BACKUP_AUDIT_SCOPE=state "$repo_root/ci/verify-cloud-backups.sh"
      export TF_VAR_terraform_state_backup_audited=true
    fi
  fi
elif [ "$needs_ocr_images" != "true" ]; then
  ocr_images_json='{}'
elif [ -z "$ocr_images_json" ]; then
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
    make --no-print-directory -C "$repo_root" ocr-images
  )"
fi
if [ -z "$ocr_images_json" ]; then
  ocr_images_json='{}'
fi

if [[ ! "$data_generation" =~ ^canonical-v(1|2)$ ]]; then
  echo "SCRIBE_DATA_GENERATION must be an explicitly reviewed canonical generation: canonical-v1 or canonical-v2." >&2
  exit 1
fi

browser_readiness_subnet_name=""
historical_browser_readiness_ipv6_subnet_name=""
if [ "$action" = "destroy" ] && [ "$environment" = "preview" ]; then
  browser_readiness_name_hash="$(sha256_text "${TF_VAR_name}:${target_workspace}")"
  browser_readiness_name_prefix="${TF_VAR_name:0:46}"
  browser_readiness_name_prefix="${browser_readiness_name_prefix%-}"
  browser_readiness_subnet_name="${browser_readiness_name_prefix}-browser-${browser_readiness_name_hash:0:8}"
  # Retain only the deterministic name of the retired dedicated-IPv6 subnet.
  # A partially deployed historical workspace can still be waiting for a
  # Google-managed Direct VPC address to release it during destroy.
  historical_browser_readiness_ipv6_name_prefix="${TF_VAR_name:0:43}"
  historical_browser_readiness_ipv6_name_prefix="${historical_browser_readiness_ipv6_name_prefix%-}"
  historical_browser_readiness_ipv6_subnet_name="${historical_browser_readiness_ipv6_name_prefix}-browser-v6-${browser_readiness_name_hash:0:8}"
fi

if [ -n "$dev_external_ocr_impersonators" ]; then
  if ! jq -e '
    type == "array" and
    length == (unique | length) and
    all(.[]; type == "string" and test("^(user|group):[^@[:space:]]+@[^@[:space:]]+$"))
  ' <<<"$dev_external_ocr_impersonators" >/dev/null; then
    echo "DEV_EXTERNAL_OCR_IMPERSONATORS must be a JSON array of unique user: or group: email IAM members." >&2
    exit 1
  fi
  if [ "$environment" != "dev" ] && [ "$(jq 'length' <<<"$dev_external_ocr_impersonators")" -ne 0 ]; then
    echo "DEV_EXTERNAL_OCR_IMPERSONATORS must be empty outside the dev Terraform workspace." >&2
    exit 1
  fi
fi

terraform_vars=(
  "-var=project_id=${GCLOUD_PROJECT}"
  "-var=terraform_state_bucket=${TF_STATE_BUCKET}"
  "-var=name=${TF_VAR_name}"
  "-var=docker_compose_branch=${TF_VAR_docker_compose_branch}"
  "-var=data_generation=${data_generation}"
  "-var=run_snapshots=${TF_VAR_run_snapshots}"
  "-var=api_image=${image_tag}"
  "-var=browser_readiness_image=${browser_readiness_image}"
  "-var=browser_readiness_subnet_cidr=${browser_readiness_subnet_cidr}"
  "-var=frontend_gar_image=${frontend_gar_image_tag}"
  "-var=ocr_service_images=${ocr_images_json}"
  "-var=region=$(resolve_terraform_region)"
  "-var=zone=$(resolve_terraform_zone)"
)

if [ -n "${TF_VAR_preview_machine_type:-}" ]; then
  terraform_vars+=("-var=preview_machine_type=${TF_VAR_preview_machine_type}")
fi

if [ -n "$dev_external_ocr_impersonators" ]; then
  terraform_vars+=("-var=dev_external_ocr_impersonators=${dev_external_ocr_impersonators}")
fi

if [ -n "${ALLOWED_IPS:-}" ]; then
  terraform_vars+=("-var=allowed_ips=${ALLOWED_IPS}")
fi

if [ -n "${ALLOWED_SSH_IPV4:-}" ]; then
  terraform_vars+=("-var=allowed_ssh_ipv4=${ALLOWED_SSH_IPV4}")
fi

if [ -n "${ALLOWED_SSH_IPV6:-}" ]; then
  terraform_vars+=("-var=allowed_ssh_ipv6=${ALLOWED_SSH_IPV6}")
fi

if [ -n "${VAULT_ADMIN_EMAILS:-}" ]; then
  terraform_vars+=("-var=vault_admin_emails=${VAULT_ADMIN_EMAILS}")
fi

if [ -n "${VAULT_CI_SERVICE_ACCOUNT_EMAILS:-}" ]; then
  terraform_vars+=("-var=vault_ci_service_account_emails=${VAULT_CI_SERVICE_ACCOUNT_EMAILS}")
fi

shared_vault_workspace_name="$(shared_vault_workspace "$target_workspace")"
bootstrap_vault_token "$target_workspace" "$action" "$shared_vault_workspace_name"
register_vault_token_mask
if [ "$action" = "plan" ] || [ "$action" = "apply" ]; then
  wait_for_vault_token_readiness "$shared_vault_workspace_name"
fi

if [ "$preview_runtime_reconciler" = "true" ]; then
  env -u VAULT_TOKEN -u VAULT_ADDR "$repo_root/ci/toolchain-check.sh" --go
  preview_reconciler_binary="$(mktemp)"
  trap 'rm -f "$preview_reconciler_binary"' EXIT
  (
    cd "$repo_root"
    env -u VAULT_TOKEN -u VAULT_ADDR \
      go build -trimpath -o "$preview_reconciler_binary" ./cmd/vault-preview-runtime
  )
  export_preview_vault_reconciliation_inputs "$shared_vault_workspace_name"
  reconcile_mode="apply"
  if [ "$action" = "plan" ]; then
    reconcile_mode="check"
  fi
  "$preview_reconciler_binary" -mode="$reconcile_mode"
  rm -f "$preview_reconciler_binary"
  trap - EXIT
  exit 0
fi

if [ "$action" != "destroy" ]; then
  terraform validate
fi

if [ "$action" = "plan" ] || [ "$action" = "apply" ]; then
  bootstrap_browser_readiness_ipv6 "$target_workspace" "$action"
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
    if [ "$environment" = "prod" ] || [ -n "$target_plan_verifier" ]; then
      apply_plan_path="$(mktemp)"
      apply_plan_json="$(mktemp)"
      trap 'rm -f "$apply_plan_path" "$apply_plan_json"' EXIT
      terraform plan -out="$apply_plan_path" "${terraform_vars[@]}" "${terraform_targets[@]}"
      terraform show -json "$apply_plan_path" >"$apply_plan_json"
      if [ "$environment" = "prod" ]; then
        "$repo_root/ci/verify-production-persistent-disk-plan.sh" "$data_generation" <"$apply_plan_json"
      fi
      if [ -n "$target_plan_verifier" ]; then
        "$target_plan_verifier" "$target_set" <"$apply_plan_json"
      fi
      terraform apply -auto-approve "$apply_plan_path"
      rm -f "$apply_plan_path" "$apply_plan_json"
      trap - EXIT
    else
      apply_preview_terraform_with_capacity_retry "${terraform_vars[@]}" "${terraform_targets[@]}"
    fi
    ;;
  refresh)
    refresh_plan_path="$(mktemp)"
    trap 'rm -f "$refresh_plan_path"' EXIT
    terraform plan -refresh-only -out="$refresh_plan_path" "${terraform_vars[@]}"
    terraform show -json "$refresh_plan_path" | "$repo_root/ci/verify-terraform-refresh-plan.sh"
    terraform show "$refresh_plan_path"
    terraform apply -auto-approve "$refresh_plan_path"
    rm -f "$refresh_plan_path"
    trap - EXIT
    ;;
  destroy)
    destroy_attempt=1
    destroy_attempt_limit=3
    serverless_destroy_attempt_limit=25
    serverless_retry_delay_seconds=300
    destroy_diagnostics_file="$(mktemp)"
    trap 'rm -f "$destroy_diagnostics_file"' EXIT
    while true; do
      : >"$destroy_diagnostics_file"
      if terraform destroy -auto-approve "${terraform_vars[@]}" "${terraform_targets[@]}" 2>"$destroy_diagnostics_file"; then
        cat "$destroy_diagnostics_file" >&2
        break
      fi
      cat "$destroy_diagnostics_file" >&2
      destroy_diagnostics="$(<"$destroy_diagnostics_file")"
      serverless_subnet_delay=false
      if [ "$environment" = "preview" ]; then
        serverless_subnet_delay=true
        serverless_error_seen=false
        while IFS= read -r destroy_diagnostic_line; do
          [[ "$destroy_diagnostic_line" == *'Error:'* ]] || continue
          serverless_error_seen=true
          app_subnet_marker="/subnetworks/${TF_VAR_name}'"
          browser_subnet_marker="/subnetworks/${browser_readiness_subnet_name}'"
          historical_browser_ipv6_subnet_marker="/subnetworks/${historical_browser_readiness_ipv6_subnet_name}'"
          known_serverless_subnet=false
          if [[ "$destroy_diagnostic_line" == *"$app_subnet_marker"* ]] \
            || { [ -n "$browser_readiness_subnet_name" ] \
              && [[ "$destroy_diagnostic_line" == *"$browser_subnet_marker"* ]]; } \
            || { [ -n "$historical_browser_readiness_ipv6_subnet_name" ] \
              && [[ "$destroy_diagnostic_line" == *"$historical_browser_ipv6_subnet_marker"* ]]; }; then
            known_serverless_subnet=true
          fi
          if [[ "$destroy_diagnostic_line" != *resourceInUseByAnotherResource* ]] \
            || [ "$known_serverless_subnet" != "true" ] \
            || { [[ "$destroy_diagnostic_line" != *'/addresses/serverless-ipv4-'* ]] \
              && [[ "$destroy_diagnostic_line" != *'/addresses/serverless-ipv6-'* ]]; }; then
            serverless_subnet_delay=false
            break
          fi
        done <<<"$destroy_diagnostics"
        if [ "$serverless_error_seen" != "true" ]; then
          serverless_subnet_delay=false
        fi
      fi
      if [ "$serverless_subnet_delay" = "true" ]; then
        if [ "$destroy_attempt" -ge "$serverless_destroy_attempt_limit" ]; then
          echo "Terraform preview destroy still has a Google-managed serverless IPv4/IPv6 subnet reservation after ${serverless_destroy_attempt_limit} bounded attempts over two hours; leaving the workspace in place for operator recovery." >&2
          exit 1
        fi
        echo "Terraform preview destroy attempt ${destroy_attempt}/${serverless_destroy_attempt_limit} is waiting for Google to release its serverless IPv4/IPv6 subnet reservation; retrying in ${serverless_retry_delay_seconds}s with the same state-derived inputs." >&2
        sleep "$serverless_retry_delay_seconds"
      else
        if [ "$destroy_attempt" -ge "$destroy_attempt_limit" ]; then
          echo "Terraform destroy failed after ${destroy_attempt_limit} bounded attempts; leaving the workspace in place for operator recovery." >&2
          exit 1
        fi
        retry_delay_seconds=$((destroy_attempt * 15))
        echo "Terraform destroy attempt ${destroy_attempt}/${destroy_attempt_limit} failed; retrying in ${retry_delay_seconds}s with the same state-derived inputs." >&2
        sleep "$retry_delay_seconds"
      fi
      destroy_attempt=$((destroy_attempt + 1))
    done
    rm -f "$destroy_diagnostics_file"
    trap - EXIT
    if [ "$environment" = "preview" ]; then
      unset TF_WORKSPACE
      terraform workspace select default
      terraform workspace delete "$target_workspace"
    fi
    ;;
esac
