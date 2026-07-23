#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR
readonly HOST_ROOTFS="$ROOT_DIR/terraform/rootfs"
readonly ONIGURUMA_FILTER='(^|[^[:alnum:]_])(test|match|capture|scan|sub|gsub|splits)[[:space:]]*\('
readonly NUL_CONTAINS_FILTER='contains[[:space:]]*\([[:space:]]*"\\u0000"[[:space:]]*\)'
readonly -a HOST_EXECUTED_SOURCES=(
  "$HOST_ROOTFS"
  "$ROOT_DIR/scripts/update-env.sh"
  "$ROOT_DIR/generate-secrets.sh"
)

fail() {
  echo "COS host jq portability contract failed: $*" >&2
  exit 1
}

[[ -d "$HOST_ROOTFS" ]] || fail "Terraform host rootfs is missing."
for source in "${HOST_EXECUTED_SOURCES[@]}"; do
  [[ -e "$source" ]] || fail "host lifecycle source is missing: ${source#"$ROOT_DIR/"}"
done

if matches="$(rg -n --glob '*.sh' "$ONIGURUMA_FILTER" "${HOST_EXECUTED_SOURCES[@]}" || true)" &&
  [[ -n "$matches" ]]; then
  printf '%s\n' "$matches" >&2
  fail "host-executed jq filters require Oniguruma functions unavailable on COS."
fi
if matches="$(rg -n --glob '*.sh' "$NUL_CONTAINS_FILTER" "${HOST_EXECUTED_SOURCES[@]}" || true)" &&
  [[ -n "$matches" ]]; then
  printf '%s\n' "$matches" >&2
  fail 'host-executed jq filters use contains("\u0000"), which is unreliable with jq 1.6.'
fi

if ! rg -q 'Container-Optimized OS \(COS\) is the only supported host for every' \
  "$ROOT_DIR/docs/reference/engineering-contract.md" ||
  ! rg -q 'Scribe-managed GCP VM, including previews and production' \
    "$ROOT_DIR/docs/reference/engineering-contract.md"; then
  fail "the durable engineering contract does not identify COS as Scribe's sole GCP VM host."
fi

echo "COS host jq filters use only the jq feature set shipped by Container-Optimized OS."
