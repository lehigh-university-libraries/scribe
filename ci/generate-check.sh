#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

generated_paths=(
  docs/api
  internal/db
  proto/buf.lock
  proto/scribe
  web/src/proto
)

generated_manifest() {
  find "${generated_paths[@]}" -type f -print \
    | LC_ALL=C sort \
    | while IFS= read -r generated_file; do
        printf '%s  %s\n' "$(git hash-object "${generated_file}")" "${generated_file}"
      done
}

before_manifest="$(mktemp)"
after_manifest="$(mktemp)"
trap 'rm -f "${before_manifest}" "${after_manifest}"' EXIT
generated_manifest > "${before_manifest}"

make generate

generated_manifest > "${after_manifest}"
if ! cmp -s "${before_manifest}" "${after_manifest}"; then
  echo "Generated files are stale. Run 'make generate' and commit the result:" >&2
  diff -u "${before_manifest}" "${after_manifest}" >&2 || true
  git status --short --untracked-files=all -- "${generated_paths[@]}" >&2
  exit 1
fi
