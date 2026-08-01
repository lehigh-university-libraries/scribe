#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REBUILD_LOCAL_IMAGE="${REBUILD:-false}"

case "$REBUILD_LOCAL_IMAGE" in
  true|false) ;;
  *)
    echo "REBUILD must be true or false." >&2
    exit 2
    ;;
esac

compose_config="$(
  docker compose -f "$ROOT_DIR/docker-compose.yaml" --profile init config --format json
)"
vault_address="$(jq -er '.services["vault-init"].environment.VAULT_ADDRESS // "" | strings' <<<"$compose_config")"
vault_address="${vault_address#"${vault_address%%[![:space:]]*}"}"
vault_address="${vault_address%"${vault_address##*[![:space:]]}"}"
if [ -z "$vault_address" ]; then
  exit 0
fi

api_image="$(jq -er '.services.api.image | select(type == "string" and length > 0)' <<<"$compose_config")"
vault_init_image="$(jq -er '.services["vault-init"].image | select(type == "string" and length > 0)' <<<"$compose_config")"
if [ "$api_image" != "$vault_init_image" ]; then
  echo "API and vault-init must use the same rendered image." >&2
  exit 1
fi

# Cloud and operator-selected images remain pull-only. The repository-owned
# fallback is the sole tag this helper may build, so a digest-pinned deployment
# can never be replaced with local source by this development path.
if [ "$vault_init_image" != "scribe-api:local" ]; then
  exit 0
fi
if [ "$REBUILD_LOCAL_IMAGE" = "false" ] && docker image inspect "$vault_init_image" >/dev/null 2>&1; then
  exit 0
fi

echo "Preparing $vault_init_image before Vault-backed secret synchronization..."
docker build --tag "$vault_init_image" "$ROOT_DIR"
