#!/usr/bin/env bash

set -euo pipefail

readonly state_path="/tmp/scribe-browser-session-state.json"

[ "$#" -eq 1 ]
[ "$1" = "/app/deployed-readiness.mjs" ]
[ -z "${SCRIBE_BROWSER_STORAGE_STATE_JSON:-}" ]
case "${TEST_EXPECT_STATE:-}" in
  production)
    [ -f "$state_path" ]
    [ ! -L "$state_path" ]
    [ "$(stat -c '%a' "$state_path")" = "600" ]
    read -r state_sha _ < <(sha256sum -- "$state_path")
    [ "$state_sha" = "${TEST_EXPECTED_STATE_SHA256:?}" ]
    rm -f -- "$state_path"
    ;;
  preview)
    [ ! -e "$state_path" ]
    ;;
  *)
    exit 2
    ;;
esac
