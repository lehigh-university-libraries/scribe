#!/usr/bin/env bash

set -euo pipefail

service_json="${1:?service JSON path is required}"
revision_json="${2:?revision JSON path is required}"
expected_frontend="${3:?expected frontend image is required}"

[[ "$expected_frontend" =~ @sha256:[0-9a-f]{64}$ ]] || {
  echo "expected frontend image must be digest-pinned" >&2
  exit 2
}

jq -e '
  (.status.latestReadyRevisionName) as $revision |
  ($revision != null and $revision != "") and
  any(.status.conditions[]?; .type == "Ready" and .status == "True") and
  ($revision == .status.latestCreatedRevisionName) and
  (([.status.traffic[]? | (.percent // 0)] | add // 0) == 100) and
  any(.status.traffic[]?;
    (.percent // 0) == 100 and
    (.revisionName == $revision or .latestRevision == true)
  )
' "$service_json" >/dev/null || {
  echo "Cloud Run service is not Ready with 100% traffic on its latest revision" >&2
  exit 1
}

revision="$(jq -r '.status.latestReadyRevisionName' "$service_json")"
jq -e --arg revision "$revision" --arg expected "$expected_frontend" '
  .metadata.name == $revision and
  any(.status.conditions[]?; .type == "Ready" and .status == "True") and
  any(.spec.containers[]?; .name == "frontend" and .image == $expected)
' "$revision_json" >/dev/null || {
  echo "Cloud Run revision is not Ready with the expected digest-pinned frontend image" >&2
  exit 1
}

printf '%s\n' "$revision"
