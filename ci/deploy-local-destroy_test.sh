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
    case "${2:-}" in
      select)
        case "${TF_TEST_WORKSPACE_MODE:-existing}" in
          missing|inventory-error|listed-but-unselectable)
            [ "${3:-}" = "default" ] || exit 1
            ;;
        esac
        ;;
      list)
        case "${TF_TEST_WORKSPACE_MODE:-existing}" in
          inventory-error) exit 1 ;;
          listed-but-unselectable) printf '  default\n  pr-75\n' ;;
          *) printf '* default\n' ;;
        esac
        ;;
    esac
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
  destroy)
    case "${TF_TEST_DESTROY_MODE:-success}" in
      success) ;;
      fail-once|always-fail|serverless-fail-once|serverless-always-fail|serverless-mixed|serverless-wrong-subnet|\
      serverless-browser-fail-once|serverless-browser-ipv6-fail-once|serverless-browser-wrong-subnet|serverless-browser-ipv6-wrong-subnet|\
      serverless-browser-v6-fail-once|serverless-browser-v6-ipv6-fail-once|serverless-browser-v6-wrong-subnet|serverless-browser-v6-ipv6-wrong-subnet|\
      serverless-prod-browser-fail-once|serverless-prod-browser-ipv6-fail-once|serverless-prod-browser-wrong-subnet|serverless-prod-browser-ipv6-wrong-subnet|\
      serverless-prod-browser-v6-fail-once|serverless-prod-browser-v6-ipv6-fail-once|serverless-prod-browser-v6-wrong-subnet|serverless-prod-browser-v6-ipv6-wrong-subnet)
        attempts=0
        if [ -f "${TF_TEST_DESTROY_ATTEMPTS_FILE}" ]; then
          read -r attempts <"${TF_TEST_DESTROY_ATTEMPTS_FILE}" || true
        fi
        attempts=$((attempts + 1))
        printf '%s\n' "$attempts" >"${TF_TEST_DESTROY_ATTEMPTS_FILE}"
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-always-fail" ] \
          || { [ "${TF_TEST_DESTROY_MODE}" = "serverless-fail-once" ] && [ "$attempts" -eq 1 ]; }; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-75', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-mixed" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-75', resourceInUseByAnotherResource" >&2
          echo "Error: permission denied while deleting an unrelated resource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-9aac94f3': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-75-browser-9aac94f3', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-ipv6-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-9aac94f3': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-pr-75-browser-9aac94f3', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-v6-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-v6-9aac94f3': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-75-browser-v6-9aac94f3', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-v6-ipv6-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-v6-9aac94f3': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-pr-75-browser-v6-9aac94f3', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-75-browser-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-ipv6-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-pr-75-browser-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-v6-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-v6-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-75-browser-v6-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-browser-v6-ipv6-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-75-browser-v6-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-pr-75-browser-v6-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-4a3bef0b': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-browser-4a3bef0b', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-ipv6-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-4a3bef0b': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-browser-4a3bef0b', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-v6-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-v6-4a3bef0b': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-browser-v6-4a3bef0b', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-v6-ipv6-fail-once" ] && [ "$attempts" -eq 1 ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-v6-4a3bef0b': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-browser-v6-4a3bef0b', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-browser-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-ipv6-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-browser-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-v6-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-v6-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-browser-v6-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-prod-browser-v6-ipv6-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-browser-v6-deadbeef': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv6-scribe-browser-v6-deadbeef', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "serverless-wrong-subnet" ]; then
          echo "Error: Error when reading or editing Subnetwork 'projects/example-project/regions/us-east5/subnetworks/scribe-pr-76': googleapi: Error 400: already used by 'projects/example-project/regions/us-east5/addresses/serverless-ipv4-scribe-pr-76', resourceInUseByAnotherResource" >&2
          exit 1
        fi
        if [ "${TF_TEST_DESTROY_MODE}" = "always-fail" ] \
          || { [ "${TF_TEST_DESTROY_MODE}" = "fail-once" ] && [ "$attempts" -eq 1 ]; }; then
          exit 1
        fi
        ;;
      *) exit 2 ;;
    esac
    exit 0
    ;;
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

cat >"${TEST_DIR}/bin/sleep" <<'EOF'
#!/usr/bin/env bash
printf 'sleep %s\n' "$*" >>"${TF_TEST_SLEEP_LOG}"
exit 0
EOF
chmod +x "${TEST_DIR}/bin/sleep"

# Give teardown only the tools it actually uses. Docker is deliberately absent,
# so reintroducing an unconditional Docker/registry dependency fails this test.
for command in bash dirname jq sed awk tr cat mktemp rm sha256sum; do
  ln -s "$(command -v "${command}")" "${TEST_DIR}/bin/${command}"
done

cat >"${state_file}" <<'EOF'
{
  "data_generation": "canonical-v1",
  "docker_compose_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "api_image": "ghcr.io/lehigh-university-libraries/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "browser_readiness_image": "us-docker.pkg.dev/example-project/internal/scribe-browser-readiness@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "configuration": {
    "allowed_ips": ["192.0.2.0/24"],
    "allowed_ssh_ipv4": ["198.51.100.1/32"],
    "allowed_ssh_ipv6": ["2001:db8::/64"],
    "backup_restore_service_account_email": "backup@example-project.iam.gserviceaccount.com",
    "browser_readiness_subnet_cidr": "10.43.0.0/26",
    "compose_network_cidr": "172.30.0.0/24",
    "dev_external_ocr_impersonators": [],
    "iiif_max_manifest_canvases": 500,
    "iiif_max_manifest_import_bytes": 67108864,
    "monitoring_notification_channels": ["projects/example-project/notificationChannels/release-alerts"],
    "network_ip_cidr_range": "10.42.0.0/24",
    "preview_machine_type": "n2d-standard-2",
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
    SCRIBE_REGION=caller-region1 \
    SCRIBE_PREVIEW_MACHINE_TYPE="${SCRIBE_PREVIEW_MACHINE_TYPE:-e2-medium}" \
    SCRIBE_ZONE=caller-region1-a \
    VAULT_TOKEN=test-only-token \
    TF_TEST_COMMAND_LOG="${command_log}" \
    TF_TEST_LOG="${terraform_log}" \
    TF_TEST_STATE_FILE="${TF_TEST_STATE_FILE:-$state_file}" \
    TF_TEST_STATE_MODE="${mode}" \
    TF_TEST_WORKSPACE_MODE="${TF_TEST_WORKSPACE_MODE:-existing}" \
    TF_TEST_DESTROY_MODE="${TF_TEST_DESTROY_MODE:-success}" \
    TF_TEST_DESTROY_ATTEMPTS_FILE="${TEST_DIR}/destroy-attempts" \
    TF_TEST_SLEEP_LOG="${TEST_DIR}/sleep.log" \
    SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS="${SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS:-false}" \
    "${ROOT_DIR}/terraform/deploy-local.sh" preview destroy \
      --branch bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
      --pr-number 75 >"${stdout_file}" 2>"${stderr_file}"
}

run_prod_destroy() {
  local mode="$1"
  local stdout_file="$2"
  local stderr_file="$3"
  PATH="${TEST_DIR}/bin" \
    GCLOUD_PROJECT=example-project \
    TF_STATE_BUCKET=example-state \
    ALLOWED_IPS='["127.0.0.1/32"]' \
    CONFIRM_PRODUCTION_DESTROY=scribe-prod-destroy \
    SCRIBE_REGION=caller-region1 \
    SCRIBE_ZONE=caller-region1-a \
    VAULT_TOKEN=test-only-token \
    TF_TEST_COMMAND_LOG="${command_log}" \
    TF_TEST_LOG="${terraform_log}" \
    TF_TEST_STATE_FILE="${TF_TEST_STATE_FILE:-$state_file}" \
    TF_TEST_STATE_MODE="${mode}" \
    TF_TEST_WORKSPACE_MODE=existing \
    TF_TEST_DESTROY_MODE="${TF_TEST_DESTROY_MODE:-success}" \
    TF_TEST_DESTROY_ATTEMPTS_FILE="${TEST_DIR}/destroy-attempts" \
    TF_TEST_SLEEP_LOG="${TEST_DIR}/sleep.log" \
    "${ROOT_DIR}/terraform/deploy-local.sh" prod destroy \
      --branch bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
      >"${stdout_file}" 2>"${stderr_file}"
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
    SCRIBE_PREVIEW_MACHINE_TYPE="${SCRIBE_PREVIEW_MACHINE_TYPE:-e2-medium}" \
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
grep -F -- '-var=browser_readiness_image=us-docker.pkg.dev/example-project/internal/scribe-browser-readiness@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' "${terraform_log}" >/dev/null
grep -F -- '-var=browser_readiness_subnet_cidr=10.43.0.0/26' "${terraform_log}" >/dev/null
grep -F -- '-var=frontend_gar_image=us-docker.pkg.dev/example-project/internal/scribe-frontend@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc' "${terraform_log}" >/dev/null
grep -F -- '-var=dev_external_ocr_impersonators=[]' "${terraform_log}" >/dev/null
grep -F -- '-var=region=us-east5' "${terraform_log}" >/dev/null
grep -F -- '-var=zone=us-east5-b' "${terraform_log}" >/dev/null
grep -F -- '-var=preview_machine_type=n2d-standard-2' "${terraform_log}" >/dev/null
if grep -F -- '-var=frontend_image=' "${terraform_log}" >/dev/null; then
  echo "destroy replayed an unused GHCR frontend image into Terraform" >&2
  exit 1
fi
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null
test ! -s "${command_log}"

# The preview machine profile was previously hardcoded. Absence alone must
# replay that historical e2-medium value, while caller configuration remains
# irrelevant to teardown.
legacy_machine_state="${TEST_DIR}/deployment-inputs-before-preview-machine-type.json"
jq 'del(.configuration.preview_machine_type)' "${state_file}" >"${legacy_machine_state}"
: >"${terraform_log}"
if ! SCRIBE_PREVIEW_MACHINE_TYPE=n2d-standard-2 \
  TF_TEST_STATE_FILE="$legacy_machine_state" run_destroy valid \
  "${TEST_DIR}/legacy-machine.out" "${TEST_DIR}/legacy-machine.err"; then
  echo "valid pre-preview-machine-profile state did not reach Terraform destroy" >&2
  sed -n '1,20p' "${TEST_DIR}/legacy-machine.err" >&2
  exit 1
fi
grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null
grep -F -- '-var=preview_machine_type=e2-medium' "${terraform_log}" >/dev/null
if grep -F -- '-var=preview_machine_type=n2d-standard-2' "${terraform_log}" >/dev/null; then
  echo "legacy preview destroy used the conflicting caller machine profile" >&2
  exit 1
fi
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null

for invalid_machine_type in null unreviewed; do
  invalid_state="${TEST_DIR}/deployment-inputs-machine-${invalid_machine_type}.json"
  case "$invalid_machine_type" in
    null) jq '.configuration.preview_machine_type = null' "${state_file}" >"${invalid_state}" ;;
    unreviewed) jq '.configuration.preview_machine_type = "n2d-standard-96"' "${state_file}" >"${invalid_state}" ;;
  esac
  : >"${terraform_log}"
  if TF_TEST_STATE_FILE="$invalid_state" run_destroy valid \
    "${TEST_DIR}/machine-${invalid_machine_type}.out" \
    "${TEST_DIR}/machine-${invalid_machine_type}.err"; then
    echo "destroy accepted explicitly invalid preview machine state: ${invalid_machine_type}" >&2
    exit 1
  fi
  if grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null; then
    echo "destroy ran with explicitly invalid preview machine state: ${invalid_machine_type}" >&2
    exit 1
  fi
done

# A provider can report a short-lived dependency after its parent service has
# completed deletion. Retry in the same process so the already validated,
# state-derived inputs remain available even after a partial destroy.
rm -f "${TEST_DIR}/destroy-attempts"
: >"${terraform_log}"
if ! TF_TEST_DESTROY_MODE=fail-once run_destroy valid \
  "${TEST_DIR}/destroy-retry.out" "${TEST_DIR}/destroy-retry.err"; then
  echo "transient Terraform destroy failure was not retried" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -F 'retrying in 15s with the same state-derived inputs' "${TEST_DIR}/destroy-retry.err" >/dev/null
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null

# Cloud Run Direct VPC teardown can leave a provider-managed address holding
# the subnet for up to two hours. Only that exact preview diagnostic receives
# the longer five-minute retry window; it still reuses the one state snapshot.
rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
: >"${terraform_log}"
if ! TF_TEST_DESTROY_MODE=serverless-fail-once run_destroy valid \
  "${TEST_DIR}/destroy-serverless-retry.out" "${TEST_DIR}/destroy-serverless-retry.err"; then
  echo "Google-managed serverless subnet cleanup delay was not retried" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
grep -F 'waiting for Google to release its serverless IPv4/IPv6 subnet reservation' \
  "${TEST_DIR}/destroy-serverless-retry.err" >/dev/null
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null

# The protected browser job has a second exact Direct VPC subnet. Its
# deterministic hashed name receives the same bounded provider-cleanup path.
rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
: >"${terraform_log}"
if ! TF_TEST_DESTROY_MODE=serverless-browser-fail-once run_destroy valid \
  "${TEST_DIR}/destroy-browser-serverless-retry.out" "${TEST_DIR}/destroy-browser-serverless-retry.err"; then
  echo "Google-managed browser subnet cleanup delay was not retried" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
grep -F 'waiting for Google to release its serverless IPv4/IPv6 subnet reservation' \
  "${TEST_DIR}/destroy-browser-serverless-retry.err" >/dev/null
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null

# The same dual-stack browser subnet can be held by the provider's IPv6
# reservation. Address family never broadens the exact-subnet classifier.
rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
: >"${terraform_log}"
if ! TF_TEST_DESTROY_MODE=serverless-browser-ipv6-fail-once run_destroy valid \
  "${TEST_DIR}/destroy-browser-ipv6-serverless-retry.out" "${TEST_DIR}/destroy-browser-ipv6-serverless-retry.err"; then
  echo "Google-managed dual-stack browser subnet cleanup delay was not retried" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
grep -F 'waiting for Google to release its serverless IPv4/IPv6 subnet reservation' \
  "${TEST_DIR}/destroy-browser-ipv6-serverless-retry.err" >/dev/null
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null

# Production has the same dedicated browser Direct VPC subnet. An explicitly
# confirmed production destroy must classify only its deterministic subnet and
# retain the same state-derived retry behavior.
rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
: >"${terraform_log}"
if ! TF_TEST_DESTROY_MODE=serverless-prod-browser-fail-once run_prod_destroy valid \
  "${TEST_DIR}/destroy-prod-browser-serverless-retry.out" \
  "${TEST_DIR}/destroy-prod-browser-serverless-retry.err"; then
  echo "Google-managed production browser subnet cleanup delay was not retried" >&2
  sed -n '1,20p' "${TEST_DIR}/destroy-prod-browser-serverless-retry.err" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
grep -F 'Terraform prod destroy attempt 1/25 is waiting for Google to release' \
  "${TEST_DIR}/destroy-prod-browser-serverless-retry.err" >/dev/null

# Production's same deterministic dual-stack subnet receives the matching IPv6
# cleanup classification.
rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
: >"${terraform_log}"
if ! TF_TEST_DESTROY_MODE=serverless-prod-browser-ipv6-fail-once run_prod_destroy valid \
  "${TEST_DIR}/destroy-prod-browser-ipv6-serverless-retry.out" \
  "${TEST_DIR}/destroy-prod-browser-ipv6-serverless-retry.err"; then
  echo "Google-managed production dual-stack browser subnet cleanup delay was not retried" >&2
  sed -n '1,20p' "${TEST_DIR}/destroy-prod-browser-ipv6-serverless-retry.err" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
grep -F 'Terraform prod destroy attempt 1/25 is waiting for Google to release its serverless IPv4/IPv6 subnet reservation' \
  "${TEST_DIR}/destroy-prod-browser-ipv6-serverless-retry.err" >/dev/null

# Workspaces that reached the retired additive network rollout can still own a
# deterministic `-browser-v6-` subnet. Keep that exact historical name on the
# bounded cleanup path for either managed address family, without making it a
# current resource name again. A partial rollout can predate the recorded
# browser image, so teardown classification must not depend on that output.
historical_empty_browser_state="${TEST_DIR}/deployment-inputs-empty-browser.json"
jq '.browser_readiness_image = ""' "${state_file}" >"${historical_empty_browser_state}"
for historical_environment in preview prod; do
  for historical_route in ipv4 ipv6; do
    rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
    : >"${terraform_log}"
    if [ "$historical_environment" = preview ]; then
      historical_mode=serverless-browser-v6-fail-once
      [ "$historical_route" = ipv4 ] || historical_mode=serverless-browser-v6-ipv6-fail-once
      historical_error="${TEST_DIR}/destroy-historical-preview-browser-${historical_route}.err"
      TF_TEST_STATE_FILE="$historical_empty_browser_state" \
        TF_TEST_DESTROY_MODE="$historical_mode" run_destroy valid \
          "${TEST_DIR}/destroy-historical-preview-browser-${historical_route}.out" \
          "$historical_error" || {
        echo "historical preview ${historical_route} browser-v6 subnet cleanup delay was not classified" >&2
        exit 1
      }
    else
      historical_mode=serverless-prod-browser-v6-fail-once
      [ "$historical_route" = ipv4 ] || historical_mode=serverless-prod-browser-v6-ipv6-fail-once
      historical_error="${TEST_DIR}/destroy-historical-prod-browser-${historical_route}.err"
      TF_TEST_STATE_FILE="$historical_empty_browser_state" \
        TF_TEST_DESTROY_MODE="$historical_mode" run_prod_destroy valid \
          "${TEST_DIR}/destroy-historical-prod-browser-${historical_route}.out" \
          "$historical_error" || {
        echo "historical production ${historical_route} browser-v6 subnet cleanup delay was not classified" >&2
        exit 1
      }
    fi
    [ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
    [ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
    grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
    grep -F 'waiting for Google to release its serverless IPv4/IPv6 subnet reservation' \
      "$historical_error" >/dev/null
  done
done

# A partial first browser rollout can create the deterministic Direct VPC graph
# before the additive lifecycle output records its image. Historical empty
# image state must still classify that exact managed-subnet release delay.
for partial_environment in preview prod; do
  for partial_route in ipv4 ipv6; do
    rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
    : >"${terraform_log}"
    if [ "$partial_environment" = preview ]; then
      partial_mode=serverless-browser-fail-once
      [ "$partial_route" = ipv4 ] || partial_mode=serverless-browser-ipv6-fail-once
      partial_error="${TEST_DIR}/destroy-partial-preview-browser-${partial_route}.err"
      TF_TEST_STATE_FILE="$historical_empty_browser_state" \
        TF_TEST_DESTROY_MODE="$partial_mode" run_destroy valid \
          "${TEST_DIR}/destroy-partial-preview-browser-${partial_route}.out" \
          "$partial_error" || {
        echo "partial preview ${partial_route} browser subnet cleanup delay was not classified" >&2
        exit 1
      }
    else
      partial_mode=serverless-prod-browser-fail-once
      [ "$partial_route" = ipv4 ] || partial_mode=serverless-prod-browser-ipv6-fail-once
      partial_error="${TEST_DIR}/destroy-partial-prod-browser-${partial_route}.err"
      TF_TEST_STATE_FILE="$historical_empty_browser_state" \
        TF_TEST_DESTROY_MODE="$partial_mode" run_prod_destroy valid \
          "${TEST_DIR}/destroy-partial-prod-browser-${partial_route}.out" \
          "$partial_error" || {
        echo "partial production ${partial_route} browser subnet cleanup delay was not classified" >&2
        exit 1
      }
    fi
    [ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 2 ]
    grep -Fx 'sleep 300' "${TEST_DIR}/sleep.log" >/dev/null
    grep -F 'waiting for Google to release its serverless IPv4/IPv6 subnet reservation' \
      "$partial_error" >/dev/null
  done
done

rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
: >"${terraform_log}"
if TF_TEST_DESTROY_MODE=serverless-always-fail run_destroy valid \
  "${TEST_DIR}/destroy-serverless-failed.out" "${TEST_DIR}/destroy-serverless-failed.err"; then
  echo "Google-managed serverless subnet cleanup delay exceeded its two-hour retry bound" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 25 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
[ "$(grep -Fxc 'sleep 300' "${TEST_DIR}/sleep.log")" -eq 24 ]
grep -F 'serverless IPv4/IPv6 subnet reservation after 25 bounded attempts over two hours' \
  "${TEST_DIR}/destroy-serverless-failed.err" >/dev/null
if grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null; then
  echo "failed serverless subnet destroy deleted its workspace" >&2
  exit 1
fi

for destroy_mode in \
  serverless-mixed \
  serverless-wrong-subnet \
  serverless-browser-wrong-subnet \
  serverless-browser-ipv6-wrong-subnet \
  serverless-browser-v6-wrong-subnet \
  serverless-browser-v6-ipv6-wrong-subnet; do
  rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
  : >"${terraform_log}"
  if TF_TEST_DESTROY_MODE="$destroy_mode" run_destroy valid \
    "${TEST_DIR}/destroy-${destroy_mode}.out" "${TEST_DIR}/destroy-${destroy_mode}.err"; then
    echo "non-exclusive serverless subnet diagnostic received the extended retry path" >&2
    exit 1
  fi
  [ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 3 ]
  [ "$(grep -Fxc 'sleep 15' "${TEST_DIR}/sleep.log")" -eq 1 ]
  [ "$(grep -Fxc 'sleep 30' "${TEST_DIR}/sleep.log")" -eq 1 ]
  grep -F 'Terraform destroy failed after 3 bounded attempts' \
    "${TEST_DIR}/destroy-${destroy_mode}.err" >/dev/null
  if grep -F 'waiting for Google to release' "${TEST_DIR}/destroy-${destroy_mode}.err" >/dev/null \
    || grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null; then
    echo "non-exclusive serverless subnet failure used unsafe cleanup behavior" >&2
    exit 1
  fi
done

# A production reservation with the wrong deterministic hash must remain on
# the ordinary retry path for current and retired subnet names and for either
# address family.
for destroy_mode in \
  serverless-prod-browser-wrong-subnet \
  serverless-prod-browser-ipv6-wrong-subnet \
  serverless-prod-browser-v6-wrong-subnet \
  serverless-prod-browser-v6-ipv6-wrong-subnet; do
  rm -f "${TEST_DIR}/destroy-attempts" "${TEST_DIR}/sleep.log"
  : >"${terraform_log}"
  if TF_TEST_DESTROY_MODE="$destroy_mode" run_prod_destroy valid \
    "${TEST_DIR}/destroy-${destroy_mode}.out" \
    "${TEST_DIR}/destroy-${destroy_mode}.err"; then
    echo "wrong production browser subnet received the extended retry path: ${destroy_mode}" >&2
    exit 1
  fi
  [ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 3 ]
  [ "$(grep -Fxc 'sleep 15' "${TEST_DIR}/sleep.log")" -eq 1 ]
  [ "$(grep -Fxc 'sleep 30' "${TEST_DIR}/sleep.log")" -eq 1 ]
  grep -F 'Terraform destroy failed after 3 bounded attempts' \
    "${TEST_DIR}/destroy-${destroy_mode}.err" >/dev/null
  if grep -F 'waiting for Google to release' \
    "${TEST_DIR}/destroy-${destroy_mode}.err" >/dev/null; then
    echo "wrong production browser subnet used unsafe cleanup behavior: ${destroy_mode}" >&2
    exit 1
  fi
done

rm -f "${TEST_DIR}/destroy-attempts"
: >"${terraform_log}"
if TF_TEST_DESTROY_MODE=always-fail run_destroy valid \
  "${TEST_DIR}/destroy-failed.out" "${TEST_DIR}/destroy-failed.err"; then
  echo "permanent Terraform destroy failure exceeded its retry bound" >&2
  exit 1
fi
[ "$(grep -Fc 'destroy -auto-approve' "${terraform_log}")" -eq 3 ]
[ "$(grep -Fc 'output -json deployment_inputs' "${terraform_log}")" -eq 1 ]
grep -F 'Terraform destroy failed after 3 bounded attempts' "${TEST_DIR}/destroy-failed.err" >/dev/null
if grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null; then
  echo "failed Terraform destroy deleted its workspace" >&2
  exit 1
fi

# Protected previews are applied from the trusted base branch. A preview made
# before the dev-only OCR IAM output was added therefore has no such state key,
# and teardown must replay its unambiguous empty default without caller input.
legacy_state="${TEST_DIR}/deployment-inputs-before-dev-ocr-iam.json"
jq 'del(.configuration.dev_external_ocr_impersonators)' "${state_file}" >"${legacy_state}"
: >"${terraform_log}"
if ! TF_TEST_STATE_FILE="$legacy_state" run_destroy valid \
  "${TEST_DIR}/legacy.out" "${TEST_DIR}/legacy.err"; then
  echo "valid pre-dev-OCR-IAM state did not reach Terraform destroy" >&2
  sed -n '1,20p' "${TEST_DIR}/legacy.err" >&2
  exit 1
fi
grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null
grep -F -- '-var=dev_external_ocr_impersonators=[]' "${terraform_log}" >/dev/null
grep -F 'workspace delete pr-75' "${terraform_log}" >/dev/null
test ! -s "${command_log}"

for invalid_impersonators in null string nonempty; do
  invalid_state="${TEST_DIR}/deployment-inputs-impersonators-${invalid_impersonators}.json"
  case "$invalid_impersonators" in
    null) jq '.configuration.dev_external_ocr_impersonators = null' "${state_file}" >"${invalid_state}" ;;
    string) jq '.configuration.dev_external_ocr_impersonators = "user:developer@example.edu"' "${state_file}" >"${invalid_state}" ;;
    nonempty) jq '.configuration.dev_external_ocr_impersonators = ["user:developer@example.edu"]' "${state_file}" >"${invalid_state}" ;;
  esac
  : >"${terraform_log}"
  if TF_TEST_STATE_FILE="$invalid_state" run_destroy valid \
    "${TEST_DIR}/impersonators-${invalid_impersonators}.out" \
    "${TEST_DIR}/impersonators-${invalid_impersonators}.err"; then
    echo "destroy accepted explicitly invalid impersonator state: ${invalid_impersonators}" >&2
    exit 1
  fi
  if grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null; then
    echo "destroy ran with explicitly invalid impersonator state: ${invalid_impersonators}" >&2
    exit 1
  fi
  if [ "$invalid_impersonators" = "nonempty" ]; then
    grep -F 'DEV_EXTERNAL_OCR_IMPERSONATORS must be empty outside the dev Terraform workspace.' \
      "${TEST_DIR}/impersonators-${invalid_impersonators}.err" >/dev/null
  fi
done

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

for invalid_zone in not-a-zone us-west1-a; do
  invalid_state="${TEST_DIR}/deployment-inputs-${invalid_zone}.json"
  jq --arg zone "$invalid_zone" '.configuration.zone = $zone' \
    "${state_file}" >"${invalid_state}"
  : >"${terraform_log}"
  if TF_TEST_STATE_FILE="$invalid_state" run_destroy valid \
    "${TEST_DIR}/invalid-zone-${invalid_zone}.out" \
    "${TEST_DIR}/invalid-zone-${invalid_zone}.err"; then
    echo "destroy unexpectedly accepted invalid recorded zone ${invalid_zone}" >&2
    exit 1
  fi
  if grep -F 'destroy -auto-approve' "${terraform_log}" >/dev/null; then
    echo "destroy ran with invalid recorded zone ${invalid_zone}" >&2
    exit 1
  fi
  grep -F 'Inspect and recover the remote workspace state before retrying destroy.' \
    "${TEST_DIR}/invalid-zone-${invalid_zone}.err" >/dev/null
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

# A protected recovery rerun may arrive after Terraform destroy already
# deleted the exact preview workspace but before Vault/evidence cleanup ran.
# Only a successful workspace inventory proving absence may turn that narrow
# recovery case into an idempotent Terraform success.
: >"${terraform_log}"
if ! SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS=true TF_TEST_WORKSPACE_MODE=missing \
  run_destroy valid "${TEST_DIR}/workspace-recovered.out" \
    "${TEST_DIR}/workspace-recovered.err"; then
  echo "protected recovery did not accept an already absent preview workspace" >&2
  exit 1
fi
grep -F 'workspace select pr-75' "${terraform_log}" >/dev/null
grep -F 'workspace list -no-color' "${terraform_log}" >/dev/null
grep -F 'Terraform workspace pr-75 is already absent; protected preview teardown recovery has no Terraform state left to destroy.' \
  "${TEST_DIR}/workspace-recovered.out" >/dev/null
if grep -Eq '^(output|destroy|workspace new|workspace delete)' "${terraform_log}"; then
  echo "already-complete recovery inspected, changed, or recreated Terraform state" >&2
  exit 1
fi

for recovery_mode in inventory-error listed-but-unselectable; do
  : >"${terraform_log}"
  if SCRIBE_RECOVER_PREVIEW_DESTROY_INPUTS=true \
    TF_TEST_WORKSPACE_MODE="$recovery_mode" run_destroy valid \
      "${TEST_DIR}/workspace-${recovery_mode}.out" \
      "${TEST_DIR}/workspace-${recovery_mode}.err"; then
    echo "protected recovery accepted unsafe workspace result ${recovery_mode}" >&2
    exit 1
  fi
  if grep -Eq '^(output|destroy|workspace new|workspace delete)' "${terraform_log}"; then
    echo "unsafe workspace recovery ${recovery_mode} changed Terraform state" >&2
    exit 1
  fi
done
grep -F 'Unable to inventory Terraform workspaces after selection failed; refusing to infer that pr-75 was destroyed.' \
  "${TEST_DIR}/workspace-inventory-error.err" >/dev/null
grep -F 'Terraform workspace pr-75 still exists but could not be selected; refusing recovery.' \
  "${TEST_DIR}/workspace-listed-but-unselectable.err" >/dev/null

echo "Immutable preview teardown and full-graph refresh input contracts passed."
