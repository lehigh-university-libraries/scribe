#!/usr/bin/env bash

set -euo pipefail

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"

zero_sha="0000000000000000000000000000000000000000"
zero_digest="sha256:0000000000000000000000000000000000000000000000000000000000000000"

# Destroy must use the immutable values already committed to the selected
# workspace. Do not echo a corrupt payload: state output can contain operational
# identifiers that do not belong in an Actions error or artifact.
if ! jq -ceS \
  --arg project "$GCLOUD_PROJECT" \
  --arg zero_sha "$zero_sha" \
  --arg zero_digest "$zero_digest" '
    select(
    type == "object" and
    (.configuration as $configuration |
      $configuration | type == "object" and
      (.project_id == $project) and
      (.region | type == "string" and test("^[a-z]+-[a-z]+[0-9]+$")) and
      (.zone | type == "string" and test("^[a-z]+-[a-z]+[0-9]+-[a-z]$")) and
      (.zone | startswith($configuration.region + "-")) and
      (.dev_external_ocr_impersonators |
        type == "array" and
        all(.[]; type == "string" and test("^(user|group):[^@[:space:]]+@[^@[:space:]]+$")))) and
    (.data_generation | type == "string" and test("^[a-z][a-z0-9-]{0,31}$")) and
    (.docker_compose_sha | type == "string" and test("^[0-9a-f]{40}$") and . != $zero_sha) and
    (.api_image |
      type == "string" and
      test("^ghcr\\.io/lehigh-university-libraries/scribe@sha256:[0-9a-f]{64}$") and
      endswith($zero_digest) == false) and
    (.frontend_gar_image |
      type == "string" and
      startswith("us-docker.pkg.dev/" + $project + "/internal/scribe-frontend@sha256:") and
      test("@sha256:[0-9a-f]{64}$") and
      endswith($zero_digest) == false) and
    (.ocr_service_images |
      type == "object" and
      length > 0 and
      all(.[];
        type == "string" and
        startswith("us-docker.pkg.dev/" + $project + "/internal/") and
        test("@sha256:[0-9a-f]{64}$") and
        endswith($zero_digest) == false))
    )
  ' 2>/dev/null; then
  echo "Stored deployment_inputs are missing or invalid; refusing to destroy with guessed or mutable release inputs." >&2
  exit 1
fi
