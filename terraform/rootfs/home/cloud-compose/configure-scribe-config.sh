#!/usr/bin/env bash

set -euo pipefail

config_path="${1:?config path required}"
vault_address="${2:?vault address required}"
vault_role="${3:-}"

if [ ! -f "$config_path" ]; then
  echo "config file not found: $config_path" >&2
  exit 1
fi

escaped_address="$(printf '%s' "$vault_address" | sed 's/[\/&]/\\&/g')"
sed -i "s/^  address: .*/  address: \"${escaped_address}\"/" "$config_path"

if [ -n "$vault_role" ]; then
  escaped_role="$(printf '%s' "$vault_role" | sed 's/[\/&]/\\&/g')"
  sed -i "s/^  gcp_auth_role: .*/  gcp_auth_role: ${escaped_role}/" "$config_path"
fi
