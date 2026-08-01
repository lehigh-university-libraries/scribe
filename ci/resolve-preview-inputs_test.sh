#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

run_preview_unit_tests() {
  if command -v go >/dev/null 2>&1 &&
    [ "$(go env GOVERSION)" = "go$(tr -d '\n' <"$ROOT_DIR/.go-version")" ]; then
    (cd "$ROOT_DIR" && go test ./internal/deployer -run '^TestResolvePreview|^TestPreviewInputs')
    return
  fi

  docker run --rm --network none --read-only \
    --tmpfs /tmp:rw,noexec,nosuid,size=256m \
    -e GOCACHE=/tmp/go-build \
    -e GOMODCACHE=/tmp/go-mod \
    -v "$ROOT_DIR:/app:ro" \
    -w /app \
    golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 \
    go test ./internal/deployer -run '^TestResolvePreview|^TestPreviewInputs'
}

run_preview_unit_tests

workflow="${ROOT_DIR}/.github/workflows/terraform-preview.yaml"
grep -F './ci/resolve-preview-inputs.sh' "${workflow}" >/dev/null || {
  echo "Terraform Preview must use the tested trusted-input resolver" >&2
  exit 1
}
grep -F 'exec go run ./cmd/deployer preview-inputs' \
  "${ROOT_DIR}/ci/resolve-preview-inputs.sh" >/dev/null || {
  echo "Preview input shell entrypoint must remain a thin typed-deployer caller" >&2
  exit 1
}
go_setup_line="$(rg -n 'name: Set up Go for typed preview input resolution' "$workflow" | cut -d: -f1)"
resolve_line="$(rg -n 'name: Resolve immutable preview inputs' "$workflow" | cut -d: -f1)"
[[ "$go_setup_line" =~ ^[0-9]+$ && "$resolve_line" =~ ^[0-9]+$ && "$go_setup_line" -lt "$resolve_line" ]] || {
  echo "Terraform Preview must install the pinned Go toolchain before resolving inputs" >&2
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
