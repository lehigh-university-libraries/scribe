#!/bin/sh

set -eu

: "${KMS_KEY_RING:?KMS_KEY_RING is required}"
: "${KMS_CRYPTO_KEY:?KMS_CRYPTO_KEY is required}"

escape_sed_replacement() {
  printf '%s' "$1" | sed 's/[&|]/\\&/g'
}

config_path="${VAULT_CONFIG_PATH:-/tmp/vault-config.hcl}"

sed \
  -e "s|__KMS_KEY_RING__|$(escape_sed_replacement "$KMS_KEY_RING")|g" \
  -e "s|__KMS_CRYPTO_KEY__|$(escape_sed_replacement "$KMS_CRYPTO_KEY")|g" \
  /etc/vault/config.hcl.tmpl > "$config_path"

exec /vault server -config "$config_path"
