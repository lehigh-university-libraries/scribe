#!/usr/bin/env bash

set -euo pipefail

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"

zero_sha="0000000000000000000000000000000000000000"
zero_digest="sha256:0000000000000000000000000000000000000000000000000000000000000000"

# Destroy must use the immutable values already committed to the selected
# workspace. Do not echo a corrupt payload: state output can contain operational
# identifiers that do not belong in an Actions error or artifact.
# State recorded before the dev-only external OCR IAM feature has no
# dev_external_ocr_impersonators key. Its only valid preview/production value
# is the Terraform default ([]), so normalize that missing key alone. An
# explicit null or malformed value must still fail validation below. The
# browser-only /26 was introduced later and likewise has one reviewed legacy
# default; normalize absence alone so historical preview state remains
# destroyable without accepting an explicit null or malformed range. The
# preview-only machine profile is structurally ignored outside pr-* workspaces,
# so its missing historical value unambiguously normalizes to the former
# preview default, e2-medium.
if ! jq -ceS \
  --arg project "$GCLOUD_PROJECT" \
  --arg zero_sha "$zero_sha" \
  --arg zero_digest "$zero_digest" '
    if (.configuration | type) == "object" then
      if (.configuration | has("dev_external_ocr_impersonators")) then
        .
      else
        .configuration.dev_external_ocr_impersonators = []
      end
    else
      .
    end
    | if type == "object" and (has("browser_readiness_image") | not) then
        .browser_readiness_image = ""
      else
        .
      end
    | if (.configuration | type) == "object" and (.configuration | has("browser_readiness_subnet_cidr") | not) then
        .configuration.browser_readiness_subnet_cidr = "10.43.0.0/26"
      else
        .
      end
    | if (.configuration | type) == "object" and (.configuration | has("preview_machine_type") | not) then
        .configuration.preview_machine_type = "e2-medium"
      else
        .
      end
    | select(
    type == "object" and
    (.configuration as $configuration |
      $configuration | type == "object" and
      (.project_id == $project) and
      (.region | type == "string" and test("^[a-z]+-[a-z]+[0-9]+$")) and
      (.zone | type == "string" and test("^[a-z]+-[a-z]+[0-9]+-[a-z]$")) and
      (.zone | startswith($configuration.region + "-")) and
      (.preview_machine_type == "e2-medium" or .preview_machine_type == "n2d-standard-2") and
      (.browser_readiness_subnet_cidr |
        type == "string" and
        test("^[0-9]{1,3}([.][0-9]{1,3}){3}/26$") and
        startswith("169.254.") == false) and
      (.dev_external_ocr_impersonators |
        type == "array" and
        all(.[]; type == "string" and test("^(user|group):[^@[:space:]]+@[^@[:space:]]+$")))) and
    (.data_generation | type == "string" and test("^canonical-v(1|2)$")) and
    (.docker_compose_sha | type == "string" and test("^[0-9a-f]{40}$") and . != $zero_sha) and
    (.api_image |
      type == "string" and
      test("^ghcr\\.io/lehigh-university-libraries/scribe@sha256:[0-9a-f]{64}$") and
      endswith($zero_digest) == false) and
    (.browser_readiness_image |
      type == "string" and
      (. == "" or (
        startswith("us-docker.pkg.dev/" + $project + "/internal/scribe-browser-readiness@sha256:") and
        test("@sha256:[0-9a-f]{64}$") and
        endswith($zero_digest) == false))) and
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
