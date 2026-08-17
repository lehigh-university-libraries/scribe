#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Vault init image test failed: $*" >&2
  exit 1
}


image='ghcr.io/lehigh-university-libraries/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
rendered="$(
  SCRIBE_API_IMAGE="$image" \
    docker compose --profile init -f docker-compose.yaml config --format json
)"

jq -e --arg image "$image" '
  .services.api.image == $image and
  .services.worker.image == $image and
  .services["vault-init"].image == $image and
  .services["vault-init"].read_only == true and
  .services["vault-init"].security_opt == ["no-new-privileges:true"] and
  (.services["vault-init"].cap_drop | index("ALL")) != null and
  (.services["vault-init"].cap_add | index("CHOWN")) != null and
  (.services["vault-init"].cap_add | index("DAC_OVERRIDE")) != null
' <<<"$rendered" >/dev/null || fail "Compose does not pin and harden the Vault helper with the API image digest"

if jq -e '.services["vault-init"].volumes[]?.source | test("scripts/vault-(init|retry)\\.sh$")' \
  <<<"$rendered" >/dev/null; then
  fail "Compose overrides the reviewed Vault helper with host source"
fi

echo "Vault init image rendering behavior passed."
