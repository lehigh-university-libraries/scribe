#!/bin/sh

set -eu

token_file="${TRIPLET_PRESENTATION_WRITE_TOKEN_FILE:-/run/secrets/triplet_presentation_write_token}"
if [ ! -r "${token_file}" ]; then
  echo "Triplet presentation write-token file is not readable." >&2
  exit 1
fi

TRIPLET_PRESENTATION_WRITE_TOKEN="$(tr -d '\r\n' < "${token_file}")"
if [ "${#TRIPLET_PRESENTATION_WRITE_TOKEN}" -lt 32 ]; then
  echo "Triplet presentation write token must contain at least 32 characters." >&2
  exit 1
fi
export TRIPLET_PRESENTATION_WRITE_TOKEN

exec /usr/local/bin/triplet "$@"
