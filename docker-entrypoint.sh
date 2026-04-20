#!/usr/bin/env sh

set -eu

# Scribe reads nearly all runtime config from /etc/scribe/config.yaml. The only
# optional env fallback left is VAULT_TOKEN for local development.

if [ "$#" -gt 0 ]; then
  exec "$@"
fi

exec /app/scribe-api
