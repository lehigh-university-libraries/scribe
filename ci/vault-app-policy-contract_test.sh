#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Vault app policy contract failed: $*" >&2
  exit 1
}

mapfile -t database_path_variables < <(
  rg -o 'VAULT_DATABASE_[A-Z_]+_PATH' scripts/vault-init.sh | sort -u
)
if [[ "${database_path_variables[*]}" != "VAULT_DATABASE_APP_PATH" ]]; then
  fail "vault-init database reads are not exactly VAULT_DATABASE_APP_PATH: ${database_path_variables[*]}"
fi

app_policy="$({
  sed -n '/^resource "vault_policy" "app" {/,/^resource "vault_policy" "preview_app" {/p' terraform/vault.tf
})"
mapfile -t allowed_database_paths < <(
  rg -o 'path "secret/data/scribe/\$\{local\.workspace_slug\}/database/[^"]+"' <<<"$app_policy" \
    | sed -E 's#^path "secret/data/scribe/\$\{local\.workspace_slug\}/##; s#"$##' \
    | sort -u
)
if [[ "${allowed_database_paths[*]}" != "database/app" ]]; then
  fail "runtime app policy database reads are not exactly database/app: ${allowed_database_paths[*]}"
fi

rg -q 'write_secret_file "\$\{OUT_DIR\}/mariadb_password"' scripts/vault-init.sh || fail "vault-init does not materialize the authorized app credential"
if rg -q 'database/root|VAULT_DATABASE_ROOT' scripts/vault-init.sh generate-secrets.sh docker-compose.yaml terraform/vault.tf; then
  fail "runtime/bootstrap app paths still request a Vault MariaDB root credential"
fi
if rg -q 'mariadb_root_password' scripts/vault-init.sh; then
  fail "the app-identity Vault helper still materializes MariaDB's local root credential"
fi

echo "Vault app identity/read-path contracts passed."
