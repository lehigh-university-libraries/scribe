#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

mkdir -p "${TEST_DIR}/bin"
terraform_log="${TEST_DIR}/terraform.log"
command_log="${TEST_DIR}/unexpected-command.log"
state_file="${TEST_DIR}/deployment-inputs.json"

cat >"${TEST_DIR}/bin/terraform" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${TF_TEST_LOG}"
case "${1:-}" in
  init) exit 0 ;;
  workspace)
    if [ "${TF_TEST_WORKSPACE_MODE:-existing}" = "missing" ] \
      && [ "${2:-}" = "select" ] && [ "${3:-}" != "default" ]; then
      exit 1
    fi
    exit 0
    ;;
  output)
    case "${TF_TEST_STATE_MODE:-valid}" in
      valid) cat "${TF_TEST_STATE_FILE}" ;;
      corrupt) printf '%s\n' '{"private":"DO-NOT-ECHO"}' ;;
      missing) exit 1 ;;
      *) exit 2 ;;
    esac
    ;;
  validate) exit 0 ;;
  plan) exit 0 ;;
  show)
    if [ "${2:-}" != "-json" ]; then
      printf 'Verified refresh-only plan.\n'
      exit 0
    fi
    case "${TF_TEST_REFRESH_PLAN_MODE:-clean}" in
      clean)
        printf '%s\n' '{"format_version":"1.2","resource_drift":[{"address":"module.current","previous_address":"module.legacy","change":{"actions":["no-op"]}}],"resource_changes":[{"change":{"actions":["no-op"]}}],"output_changes":{"deployment_inputs":{"actions":["no-op"]}}}'
        ;;
      drift)
        printf '%s\n' '{"format_version":"1.2","resource_drift":[{"change":{"actions":["update"]}}],"resource_changes":[],"output_changes":{}}'
        ;;
      non-move-drift)
        printf '%s\n' '{"format_version":"1.2","resource_drift":[{"address":"module.current","change":{"actions":["no-op"]}}],"resource_changes":[],"output_changes":{}}'
        ;;
      resource-change)
        printf '%s\n' '{"format_version":"1.2","resource_drift":[],"resource_changes":[{"change":{"actions":["update"]}}],"output_changes":{}}'
        ;;
      output-change)
        printf '%s\n' '{"format_version":"1.2","resource_drift":[],"resource_changes":[],"output_changes":{"deployment_inputs":{"actions":["update"]}}}'
        ;;
      *) exit 2 ;;
    esac
    ;;
  apply)
    printf 'refresh-env storage_max_bytes_total=%s monitoring=%s backup=%s\n' \
      "${TF_VAR_storage_max_bytes_total:-}" \
      "${TF_VAR_monitoring_notification_channels:-}" \
      "${TF_VAR_backup_restore_service_account_email:-}" >>"${TF_TEST_LOG}"
    exit 0
    ;;
  destroy) exit 0 ;;
  *) echo "unexpected terraform invocation: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "${TEST_DIR}/bin/terraform"

cat >"${TEST_DIR}/bin/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "symbolic-ref" ]; then
  printf 'main\n'
  exit 0
fi
echo "unexpected git invocation: $*" >&2
exit 2
EOF
chmod +x "${TEST_DIR}/bin/git"

for command in gcloud curl; do
  cat >"${TEST_DIR}/bin/${command}" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$(basename "$0") $*" >>"${TF_TEST_COMMAND_LOG}"
exit 99
EOF
  chmod +x "${TEST_DIR}/bin/${command}"
done

# Give teardown only the tools it actually uses. Docker is deliberately absent,
# so reintroducing an unconditional Docker/registry dependency fails this test.
for command in bash dirname jq sed awk tr cat mktemp rm; do
  ln -s "$(command -v "${command}")" "${TEST_DIR}/bin/${command}"
done

cat >"${state_file}" <<'EOF'
{
  "data_generation": "canonical-v1",
  "docker_compose_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "api_image": "ghcr.io/lehigh-university-libraries/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "configuration": {
    "allowed_ips": ["192.0.2.0/24"],
    "allowed_ssh_ipv4": ["198.51.100.1/32"],
    "allowed_ssh_ipv6": ["2001:db8::/64"],
    "backup_restore_service_account_email": "backup@example-project.iam.gserviceaccount.com",
    "compose_network_cidr": "172.30.0.0/24",
    "iiif_max_manifest_canvases": 500,
    "iiif_max_manifest_import_bytes": 67108864,
    "monitoring_notification_channels": ["projects/example-project/notificationChannels/release-alerts"],
    "network_ip_cidr_range": "10.42.0.0/24",
    "project_id": "example-project",
    "region": "us-east5",
    "storage_max_bytes_per_workspace": 5368709120,
    "storage_max_bytes_total": 32212254720,
    "storage_max_images_per_workspace": 10000,
    "storage_max_images_total": 100000,
    "storage_max_items_per_workspace": 5000,
    "storage_max_items_total": 50000,
    "storage_normalization_cache_max_age": "168h",
    "storage_normalization_cache_max_bytes": 2147483648,
    "storage_reservation_ttl": "6h",
    "transcription_max_active_jobs_per_workspace": 1000,
    "vault_admin_emails": ["operator@lehigh.edu"],
    "vault_ci_service_account_emails": ["existing-ci@example-project.iam.gserviceaccount.com"],
    "zone": "us-east5-b"
  },
  "frontend_gar_image": "us-docker.pkg.dev/example-project/internal/scribe-frontend@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "ocr_service_images": {
    "segmentor": "us-docker.pkg.dev/example-project/internal/segmentor@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
  }
}
EOF

run_destroy() {
  local mode="$1"
  local stdout_file="$2"
  local stderr_file="$3"
  PATH="${TEST_DIR}/bin" \
    GCLOUD_PROJECT=example-project \
    TF_STATE_BUCKET=example-state \
    ALLOWED_IPS='["127.0.0.1/32"]' \
    VAULT_TOKEN=test-only-token \
    TF_TEST_COMMAND_LOG="${command_log}" \
    TF_TEST_LOG="${terraform_log}" \
    TF_TEST_STATE_FILE="${state_file}" \
    TF_TEST_STATE_MODE="${mode}" \
    TF_TEST_WORKSPACE_MODE="${TF_TEST_WORKSPACE_MODE:-existing}" \
    "${ROOT_DIR}/terraform/deploy-local.sh" preview destroy \
      --branch bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
      --pr-number 75 >"${stdout_file}" 2>"${stderr_file}"
}

run_refresh() {
  local mode="$1"
  local stdout_file="$2"
  local stderr_file="$3"
  PATH="${TEST_DIR}/bin" \
    GCLOUD_PROJECT=example-project \
    TF_STATE_BUCKET=example-state \
    ALLOWED_IPS='["203.0.113.0/24"]' \
    VAULT_ADMIN_EMAILS='["caller@lehigh.edu"]' \
    VAULT_CI_SERVICE_ACCOUNT_EMAILS='["new-ci@example-project.iam.gserviceaccount.com"]' \
    SCRIBE_REGION=caller-region1 \
    SCRIBE_ZONE=caller-region1-a \
    VAULT_TOKEN=test-only-token \
    TF_TEST_COMMAND_LOG="${command_log}" \
    TF_TEST_LOG="${terraform_log}" \
    TF_TEST_STATE_FILE="${state_file}" \
    TF_TEST_STATE_MODE="${mode}" \
    TF_TEST_REFRESH_PLAN_MODE="${TF_TEST_REFRESH_PLAN_MODE:-clean}" \
    TF_TEST_WORKSPACE_MODE="${TF_TEST_WORKSPACE_MODE:-existing}" \
    TF_CLI_ARGS="${TF_CLI_ARGS:-}" \
    TF_CLI_ARGS_plan="${TF_CLI_ARGS_plan:-}" \
    "${ROOT_DIR}/terraform/deploy-local.sh" preview refresh \
      --branch mutable-caller-branch \
      --pr-number 75 >"${stdout_file}" 2>"${stderr_file}"
}

if ! run_destroy valid "${TEST_DIR}/valid.out" "${TEST_DIR}/valid.err"; then
  echo "valid immutable deployment inputs did not reach Terraform destroy" >&2
  sed -n '1,20p' "${TEST_DIR}/valid.err" >&2
  exit 1
fi
grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null
grep -F -- '-var=docker_compose_branch=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "${terraform_log}" >/dev/null
grep -F -- '-var=data_generation=canonical-v1' "${terraform_log}" >/dev/null
grep -F -- '-var=api_image=ghcr.io/lehigh-university-libraries/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "${terraform_log}" >/dev/null
grep -F -- '-var=frontend_gar_image=us-docker.pkg.dev/example-project/internal/scribe-frontend@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' "${terraform_log}" >/dev/null
if grep -F -- '-var=frontend_image=' "${terraform_log}" >/dev/null; then
  echo "destroy replayed an unused GHCR frontend image into Terraform" >&2
  exit 1
fi
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null
test ! -s "${command_log}"

for mode in corrupt missing; do
  : >"${terraform_log}"
  if run_destroy "${mode}" "${TEST_DIR}/${mode}.out" "${TEST_DIR}/${mode}.err"; then
    echo "destroy unexpectedly accepted ${mode} deployment state" >&2
    exit 1
  fi
  if grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null; then
    echo "destroy ran with ${mode} deployment state" >&2
    exit 1
  fi
  grep -F 'Inspect and recover the remote workspace state before retrying destroy.' "${TEST_DIR}/${mode}.err" >/dev/null
  if grep -F 'DO-NOT-ECHO' "${TEST_DIR}/${mode}.err" >/dev/null; then
    echo "corrupt state content leaked to stderr" >&2
    exit 1
  fi
done

: >"${terraform_log}"
if ! run_refresh valid "${TEST_DIR}/refresh.out" "${TEST_DIR}/refresh.err"; then
  echo "valid immutable deployment inputs did not reach Terraform refresh" >&2
  sed -n '1,20p' "${TEST_DIR}/refresh.err" >&2
  exit 1
fi
grep -F 'plan -refresh-only -out=' "${terraform_log}" >/dev/null
refresh_plan_path="$(sed -nE 's/^plan -refresh-only -out=([^ ]+).*/\1/p' "${terraform_log}")"
test -n "$refresh_plan_path"
grep -F "show -json ${refresh_plan_path}" "${terraform_log}" >/dev/null
grep -F "show ${refresh_plan_path}" "${terraform_log}" >/dev/null
grep -F "apply -auto-approve ${refresh_plan_path}" "${terraform_log}" >/dev/null
grep -F 'Verified refresh-only plan.' "${TEST_DIR}/refresh.out" >/dev/null
test ! -e "$refresh_plan_path"
grep -F -- '-var=docker_compose_branch=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "${terraform_log}" >/dev/null
grep -F -- '-var=api_image=ghcr.io/lehigh-university-libraries/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' "${terraform_log}" >/dev/null
grep -F -- '-var=allowed_ips=["192.0.2.0/24"]' "${terraform_log}" >/dev/null
grep -F -- '-var=vault_ci_service_account_emails=["existing-ci@example-project.iam.gserviceaccount.com"]' "${terraform_log}" >/dev/null
grep -F -- '-var=region=us-east5' "${terraform_log}" >/dev/null
grep -F -- '-var=zone=us-east5-b' "${terraform_log}" >/dev/null
grep -F 'refresh-env storage_max_bytes_total=32212254720 monitoring=["projects/example-project/notificationChannels/release-alerts"] backup=backup@example-project.iam.gserviceaccount.com' "${terraform_log}" >/dev/null
if grep -F -- '-target=' "${terraform_log}" >/dev/null || grep -F 'workspace delete' "${terraform_log}" >/dev/null; then
  echo "refresh was targeted or deleted a workspace" >&2
  exit 1
fi
test ! -s "${command_log}"

for mode in corrupt missing; do
  : >"${terraform_log}"
  if run_refresh "${mode}" "${TEST_DIR}/refresh-${mode}.out" "${TEST_DIR}/refresh-${mode}.err"; then
    echo "refresh unexpectedly accepted ${mode} deployment state" >&2
    exit 1
  fi
  if grep -F 'plan -refresh-only' "${terraform_log}" >/dev/null; then
    echo "refresh ran with ${mode} deployment state" >&2
    exit 1
  fi
  grep -F "Inspect and recover the remote workspace state before retrying refresh." "${TEST_DIR}/refresh-${mode}.err" >/dev/null
done

for plan_mode in drift non-move-drift resource-change output-change; do
  : >"${terraform_log}"
  if TF_TEST_REFRESH_PLAN_MODE="$plan_mode" run_refresh valid \
    "${TEST_DIR}/plan-${plan_mode}.out" "${TEST_DIR}/plan-${plan_mode}.err"; then
    echo "refresh unexpectedly accepted ${plan_mode}" >&2
    exit 1
  fi
  grep -F 'plan -refresh-only -out=' "${terraform_log}" >/dev/null
  grep -F 'show -json ' "${terraform_log}" >/dev/null
  if grep -Eq '^show /|^apply ' "${terraform_log}"; then
    echo "refresh printed or applied an unverified ${plan_mode} plan" >&2
    exit 1
  fi
  grep -F 'Refresh-only plan contains non-move drift or a non-no-op resource/output action' \
    "${TEST_DIR}/plan-${plan_mode}.err" >/dev/null
done

: >"${terraform_log}"
if TF_TARGET_SET=ocr run_refresh valid "${TEST_DIR}/target.out" "${TEST_DIR}/target.err"; then
  echo "refresh unexpectedly accepted a target set" >&2
  exit 1
fi
grep -F 'Refresh always runs the full Terraform graph; TF_TARGET_SET must be empty.' "${TEST_DIR}/target.err" >/dev/null
test ! -s "${terraform_log}"

: >"${terraform_log}"
if TF_CLI_ARGS_plan='-target=module.vault' run_refresh valid \
  "${TEST_DIR}/cli-args.out" "${TEST_DIR}/cli-args.err"; then
  echo "refresh unexpectedly accepted TF_CLI_ARGS_plan" >&2
  exit 1
fi
grep -F 'Refresh refuses non-empty TF_CLI_ARGS_plan' "${TEST_DIR}/cli-args.err" >/dev/null
test ! -s "${terraform_log}"

for maintenance_action in refresh destroy; do
  : >"${terraform_log}"
  if [ "$maintenance_action" = "refresh" ]; then
    if TF_TEST_WORKSPACE_MODE=missing run_refresh valid \
      "${TEST_DIR}/workspace-refresh.out" "${TEST_DIR}/workspace-refresh.err"; then
      echo "refresh unexpectedly created a missing workspace" >&2
      exit 1
    fi
  elif TF_TEST_WORKSPACE_MODE=missing run_destroy valid \
    "${TEST_DIR}/workspace-destroy.out" "${TEST_DIR}/workspace-destroy.err"; then
    echo "destroy unexpectedly created a missing workspace" >&2
    exit 1
  fi
  grep -F "workspace select pr-75" "${terraform_log}" >/dev/null
  if grep -F 'workspace new pr-75' "${terraform_log}" >/dev/null; then
    echo "${maintenance_action} attempted to create a missing workspace" >&2
    exit 1
  fi
  grep -F "Terraform workspace pr-75 does not exist; refusing to ${maintenance_action} by creating new state." \
    "${TEST_DIR}/workspace-${maintenance_action}.err" >/dev/null
done

echo "Immutable preview teardown and full-graph refresh input contracts passed."
