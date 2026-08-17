#!/usr/bin/env bash

set -euo pipefail

readonly state_path="/tmp/scribe-browser-session-state.json"

case "${SCRIBE_BROWSER_MODE:-}" in
  production)
    : "${SCRIBE_BROWSER_STORAGE_STATE_JSON:?production browser storage state is required}"
    umask 077
    set -o noclobber
    printf '%s' "$SCRIBE_BROWSER_STORAGE_STATE_JSON" >"$state_path"
    unset SCRIBE_BROWSER_STORAGE_STATE_JSON
    ;;
  "" | preview)
    [ -z "${SCRIBE_BROWSER_STORAGE_STATE_JSON:-}" ]
    ;;
  *)
    exit 2
    ;;
esac

exec node /app/deployed-readiness.mjs
