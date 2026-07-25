#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

main_sha="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
head_sha="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
mkdir -p "${TEST_DIR}/bin"
cat >"${TEST_DIR}/bin/gh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
if [ "\${1:-}" = api ] && [ "\${2:-}" = repos/example/scribe/commits/main ]; then
  printf '%s\\n' '${main_sha}'
  exit 0
fi
echo "unexpected gh invocation: \$*" >&2
exit 1
EOF
chmod +x "${TEST_DIR}/bin/gh"

run_event_case() {
  local action="$1"
  local base_ref="$2"
  local previous_base_ref="$3"
  local expected_mode="$4"
  local output_file="${TEST_DIR}/output-${expected_mode}"

  PATH="${TEST_DIR}/bin:${PATH}" \
    GITHUB_OUTPUT="${output_file}" \
    GITHUB_REPOSITORY="example/scribe" \
    GCLOUD_PROJECT="example-project" \
    SCRIBE_REGION="us-east5" \
    SCRIBE_ZONE="us-east5-c" \
    EVENT_ACTION="${action}" \
    EVENT_BASE_REF="${base_ref}" \
    EVENT_PREVIOUS_BASE_REF="${previous_base_ref}" \
    EVENT_HEAD_REPO="example/scribe" \
    EVENT_HEAD_SHA="${head_sha}" \
    EVENT_PR="75" \
    "${ROOT_DIR}/ci/resolve-preview-inputs.sh"

  grep -Fx "mode=${expected_mode}" "${output_file}" >/dev/null
  grep -Fx "base_sha=${main_sha}" "${output_file}" >/dev/null
  grep -Fx "zone=us-east5-c" "${output_file}" >/dev/null
  grep -Fx "backend_origin=http://scribe-pr-75.us-east5-c.c.example-project.internal" "${output_file}" >/dev/null
  grep -Fx "frontend_gar_image_tag=us-docker.pkg.dev/example-project/internal/scribe-frontend:pr-75" "${output_file}" >/dev/null
  if grep -q '^frontend_gar_image=' "${output_file}"; then
    echo "Preview resolver emitted an unused untagged frontend GAR repository" >&2
    exit 1
  fi
}

run_event_case synchronize main "" apply
run_event_case edited feature main destroy

workflow="${ROOT_DIR}/.github/workflows/terraform-preview.yaml"
grep -F './ci/resolve-preview-inputs.sh' "${workflow}" >/dev/null || {
  echo "Terraform Preview must use the tested trusted-input resolver" >&2
  exit 1
}
grep -F "vars.SCRIBE_PREVIEW_ZONE != '' && vars.SCRIBE_PREVIEW_ZONE || 'us-east5-c'" "${workflow}" >/dev/null || {
  echo "Terraform Preview must use its protected preview-only zone default" >&2
  exit 1
}
grep -F "vars.SCRIBE_ZONE != '' && vars.SCRIBE_ZONE || 'us-east5-b'" \
  "${ROOT_DIR}/.github/workflows/terraform-apply.yaml" >/dev/null || {
  echo "Terraform Apply must retain the production zone default" >&2
  exit 1
}
preview_local_fallback="$(
  # shellcheck disable=SC2016 # Match the deploy helper's literal preview-mode condition.
  sed -n '/if \[ "${environment:-}" = "preview" \]; then/,/^[[:space:]]*fi$/p' \
    "${ROOT_DIR}/terraform/deploy-local.sh"
)"
grep -F "printf 'us-east5-c\\n'" <<<"$preview_local_fallback" >/dev/null || {
  echo "Local preview deploys must share the GitHub preview zone default" >&2
  exit 1
}
if grep -F 'github.event.pull_request.base.sha' "${workflow}" >/dev/null; then
  echo "Terraform Preview must not trust pull-request base SHA for privileged checkouts" >&2
  exit 1
fi
if grep -F 'steps.resolve.outputs.frontend_gar_image }}' "${workflow}" >/dev/null; then
  echo "Terraform Preview must not re-export an unused frontend GAR repository" >&2
  exit 1
fi

echo "Preview workflow security contracts passed."
