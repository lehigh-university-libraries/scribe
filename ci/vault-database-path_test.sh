#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Vault database path test failed: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"


render_workspace() {
  local workspace="$1"
  VAULT_SECRET_PREFIX="scribe/${workspace}" \
    VAULT_DATABASE_APP_PATH="scribe/${workspace}/database/app" \
    docker compose -f docker-compose.yaml --profile init config --format json
}

for workspace in prod dev; do
  rendered="$(render_workspace "$workspace")"
  jq -e --arg prefix "scribe/${workspace}" --arg database "scribe/${workspace}/database/app" '
    .services["vault-init"].environment.VAULT_SECRET_PREFIX == $prefix and
    .services["vault-init"].environment.VAULT_DATABASE_APP_PATH == $database
  ' <<<"$rendered" >/dev/null || fail "rendered ${workspace} Vault paths are not workspace-scoped"
done

preview_email="scribe-pr-75@example-project.iam.gserviceaccount.com"
preview_rendered="$(
  VAULT_SECRET_PREFIX="scribe/previews/${preview_email}" \
    VAULT_DATABASE_APP_PATH="scribe/previews/${preview_email}/database/app" \
    docker compose -f docker-compose.yaml --profile init config --format json
)"
jq -e --arg prefix "scribe/previews/${preview_email}" --arg database "scribe/previews/${preview_email}/database/app" '
  .services["vault-init"].environment.VAULT_SECRET_PREFIX == $prefix and
  .services["vault-init"].environment.VAULT_DATABASE_APP_PATH == $database
' <<<"$preview_rendered" >/dev/null || fail "rendered preview Vault path is not identity-scoped"

local_rendered="$(
  VAULT_SECRET_PREFIX=scribe/dev \
    VAULT_DATABASE_APP_PATH='' \
    docker compose -f docker-compose.yaml --profile init config --format json
)"
jq -e '.services["vault-init"].environment.VAULT_DATABASE_APP_PATH == "scribe/dev/database/app"' \
  <<<"$local_rendered" >/dev/null || fail "local development no longer defaults to the dev database path"

echo "Vault database paths render per deployment workspace."
