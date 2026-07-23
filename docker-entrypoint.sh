#!/usr/bin/env sh

set -eu

# Scribe reads runtime config from /etc/scribe/config.yaml. That YAML may
# interpolate selected container env vars such as PUBLIC_BASE_URL,
# VAULT_ADDRESS, OLLAMA_URL, and VAULT_TOKEN.

if [ -n "${TRIPLET_SOURCE_READ_TOKEN_FILE:-}" ]; then
  if [ ! -r "${TRIPLET_SOURCE_READ_TOKEN_FILE}" ]; then
    echo "Triplet source read-token file is not readable." >&2
    exit 1
  fi
  TRIPLET_SOURCE_READ_TOKEN="$(tr -d '\r\n' < "${TRIPLET_SOURCE_READ_TOKEN_FILE}")"
  if [ "${#TRIPLET_SOURCE_READ_TOKEN}" -lt 32 ] || [ "${#TRIPLET_SOURCE_READ_TOKEN}" -gt 1024 ]; then
    echo "Triplet source read token must contain between 32 and 1024 characters." >&2
    exit 1
  fi
  export TRIPLET_SOURCE_READ_TOKEN
fi

if [ -n "${TRIPLET_PRESENTATION_WRITE_TOKEN_FILE:-}" ]; then
  if [ ! -r "${TRIPLET_PRESENTATION_WRITE_TOKEN_FILE}" ]; then
    echo "Triplet presentation write-token file is not readable." >&2
    exit 1
  fi
  TRIPLET_PRESENTATION_WRITE_TOKEN="$(tr -d '\r\n' < "${TRIPLET_PRESENTATION_WRITE_TOKEN_FILE}")"
  if [ "${#TRIPLET_PRESENTATION_WRITE_TOKEN}" -lt 32 ]; then
    echo "Triplet presentation write token must contain at least 32 characters." >&2
    exit 1
  fi
  export TRIPLET_PRESENTATION_WRITE_TOKEN
fi

if [ -n "${SCRIBE_PAGE_TOKEN_SIGNING_KEY_FILE:-}" ]; then
  if [ ! -r "${SCRIBE_PAGE_TOKEN_SIGNING_KEY_FILE}" ]; then
    echo "Scribe page-token signing-key file is not readable." >&2
    exit 1
  fi
  SCRIBE_PAGE_TOKEN_SIGNING_KEY="$(tr -d '\r\n' < "${SCRIBE_PAGE_TOKEN_SIGNING_KEY_FILE}")"
  if [ "${#SCRIBE_PAGE_TOKEN_SIGNING_KEY}" -lt 32 ] || [ "${#SCRIBE_PAGE_TOKEN_SIGNING_KEY}" -gt 1024 ]; then
    echo "Scribe page-token signing key must contain between 32 and 1024 characters." >&2
    exit 1
  fi
  export SCRIBE_PAGE_TOKEN_SIGNING_KEY
fi

if [ "$#" -gt 0 ]; then
  exec "$@"
fi

exec /app/scribe-api
