#!/usr/bin/env sh

set -eu

# Scribe reads runtime config from /etc/scribe/config.yaml. That YAML may
# interpolate selected container env vars such as PUBLIC_BASE_URL,
# VAULT_ADDRESS, OLLAMA_URL, and VAULT_TOKEN.

if [ "$#" -gt 0 ]; then
  exec "$@"
fi

exec /app/scribe-api
