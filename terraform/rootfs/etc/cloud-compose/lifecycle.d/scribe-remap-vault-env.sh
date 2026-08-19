#!/usr/bin/env bash
set -euo pipefail

# Vault configuration cannot travel through extra_env: cloud-compose reserves
# the VAULT_ prefix for its own Vault Agent control-plane keys. scribe-sync-env
# already wrote these under an SCRIBE_VAULT_ prefix via the generic
# application-env mechanism; re-emit them under the names the application
# actually expects.

# shellcheck disable=SC1091
source /home/cloud-compose/profile.sh
set -a
# shellcheck disable=SC1091
source ./.env
set +a

update_compose_env VAULT_ADDRESS "$SCRIBE_VAULT_ADDRESS"
update_compose_env VAULT_WORKSPACE "$SCRIBE_VAULT_WORKSPACE"
update_compose_env VAULT_SECRET_PREFIX "$SCRIBE_VAULT_SECRET_PREFIX"
update_compose_env VAULT_DATABASE_APP_PATH "$SCRIBE_VAULT_DATABASE_APP_PATH"
update_compose_env VAULT_GCP_AUTH_ROLE "$SCRIBE_VAULT_GCP_AUTH_ROLE"
