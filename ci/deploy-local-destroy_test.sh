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
  workspace) exit 0 ;;
  output)
    case "${TF_TEST_STATE_MODE:-valid}" in
      valid) cat "${TF_TEST_STATE_FILE}" ;;
      corrupt) printf '%s\n' '{"private":"DO-NOT-ECHO"}' ;;
      missing) exit 1 ;;
      *) exit 2 ;;
    esac
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
for command in bash dirname jq sed awk tr cat; do
  ln -s "$(command -v "${command}")" "${TEST_DIR}/bin/${command}"
done

cat >"${state_file}" <<'EOF'
{
  "data_generation": "canonical-v1",
  "docker_compose_sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "api_image": "ghcr.io/lehigh-university-libraries/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
    "${ROOT_DIR}/terraform/deploy-local.sh" preview destroy \
      --branch bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb \
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
  grep -F 'Inspect and recover the remote workspace state before retrying teardown.' "${TEST_DIR}/${mode}.err" >/dev/null
  if grep -F 'DO-NOT-ECHO' "${TEST_DIR}/${mode}.err" >/dev/null; then
    echo "corrupt state content leaked to stderr" >&2
    exit 1
  fi
done

echo "Immutable preview teardown input contracts passed."
