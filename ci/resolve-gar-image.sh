#!/usr/bin/env bash

set -euo pipefail

image_ref="${1:-}"

if [ -z "$image_ref" ]; then
  echo "usage: $0 <artifact-registry-image[:tag]|artifact-registry-image@sha256:digest>" >&2
  exit 1
fi

if [[ "$image_ref" =~ ^us-docker\.pkg\.dev/[a-z0-9._/-]+@sha256:[0-9a-f]{64}$ ]]; then
  printf '%s\n' "$image_ref"
  exit 0
fi
if [[ ! "$image_ref" =~ ^us-docker\.pkg\.dev/[a-z0-9._/-]+:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$ ]]; then
  echo "expected an Artifact Registry image reference with a tag or digest, got: $image_ref" >&2
  exit 1
fi

stderr_file="$(mktemp)"
trap 'rm -f "$stderr_file"' EXIT

if ! digest="$(gcloud artifacts docker images describe "$image_ref" --format='value(image_summary.digest)' 2>"$stderr_file")"; then
  err_output="$(cat "$stderr_file")"
  if [ -n "$err_output" ]; then
    echo "$err_output" >&2
  fi
  echo "failed to resolve Artifact Registry digest for: $image_ref" >&2
  exit 1
fi

if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "failed to resolve digest for Artifact Registry image: $image_ref" >&2
  exit 1
fi

printf '%s@%s\n' "${image_ref%:*}" "$digest"
