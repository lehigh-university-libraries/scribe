#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-local-vault-image-test.XXXXXX")"
trap 'rm -rf "$TEST_DIR"' EXIT
mkdir -p "$TEST_DIR/bin"

cat >"$TEST_DIR/bin/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$FAKE_DOCKER_LOG"
case "${1:-} ${2:-}" in
  "compose -f")
    printf '{"services":{"api":{"image":"%s"},"vault-init":{"image":"%s","environment":{"VAULT_ADDRESS":"%s"}}}}\n' \
      "$FAKE_API_IMAGE" "$FAKE_VAULT_IMAGE" "$FAKE_VAULT_ADDRESS"
    ;;
  "image inspect")
    [ "$FAKE_IMAGE_EXISTS" = "true" ]
    ;;
  "build --tag") ;;
  *) ;;
esac
SH
chmod 0755 "$TEST_DIR/bin/docker"

run_helper() {
  : >"$FAKE_DOCKER_LOG"
  PATH="$TEST_DIR/bin:$PATH" \
    "$ROOT_DIR/ci/ensure-local-vault-init-image.sh"
}

export FAKE_DOCKER_LOG="$TEST_DIR/docker.log"
export FAKE_API_IMAGE=scribe-api:local
export FAKE_VAULT_IMAGE=scribe-api:local
export FAKE_VAULT_ADDRESS=https://vault.example
export FAKE_IMAGE_EXISTS=false

REBUILD=false run_helper
grep -Fq 'image inspect scribe-api:local' "$FAKE_DOCKER_LOG" || {
  echo "local Vault startup did not inspect the shared image" >&2
  exit 1
}
grep -Fq 'build --tag scribe-api:local' "$FAKE_DOCKER_LOG" || {
  echo "missing local Vault image was not built" >&2
  exit 1
}
PATH="$TEST_DIR/bin:$PATH" docker compose -f docker-compose.yaml --profile init run --rm -T vault-init >/dev/null
build_line="$(grep -nF 'build --tag scribe-api:local' "$FAKE_DOCKER_LOG" | cut -d: -f1)"
vault_run_line="$(grep -nF 'run --rm -T vault-init' "$FAKE_DOCKER_LOG" | cut -d: -f1)"
if [ -z "$build_line" ] || [ -z "$vault_run_line" ] || [ "$build_line" -ge "$vault_run_line" ]; then
  echo "fresh local image was not built before the Vault init run" >&2
  exit 1
fi

FAKE_IMAGE_EXISTS=true REBUILD=false run_helper
if grep -Fq 'build --tag' "$FAKE_DOCKER_LOG"; then
  echo "existing local Vault image was rebuilt without REBUILD=true" >&2
  exit 1
fi

FAKE_IMAGE_EXISTS=true REBUILD=true run_helper
grep -Fq 'build --tag scribe-api:local' "$FAKE_DOCKER_LOG" || {
  echo "REBUILD=true did not refresh the Vault image before synchronization" >&2
  exit 1
}

FAKE_API_IMAGE='us-docker.pkg.dev/example/internal/scribe@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
FAKE_VAULT_IMAGE="$FAKE_API_IMAGE"
FAKE_IMAGE_EXISTS=false
REBUILD=true run_helper
if grep -Eq 'image inspect|build --tag' "$FAKE_DOCKER_LOG"; then
  echo "digest-pinned cloud image entered the local build path" >&2
  exit 1
fi

FAKE_API_IMAGE=scribe-api:local
FAKE_VAULT_IMAGE=scribe-api:local
FAKE_VAULT_ADDRESS=''
REBUILD=true run_helper
if grep -Eq 'image inspect|build --tag' "$FAKE_DOCKER_LOG"; then
  echo "Vault-disabled startup prepared an unnecessary backend image" >&2
  exit 1
fi

echo "Local Vault image preparation behavior passed."
