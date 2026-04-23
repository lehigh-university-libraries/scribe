#!/usr/bin/env bash

set -euo pipefail

image_ref="${1:-}"

if [ -z "$image_ref" ]; then
  echo "usage: $0 <artifact-registry-image[:tag]|artifact-registry-image@sha256:digest>" >&2
  exit 1
fi

case "$image_ref" in
  *@sha256:*)
    printf '%s\n' "$image_ref"
    exit 0
    ;;
  us-docker.pkg.dev/*:*)
    ;;
  *)
    echo "expected an Artifact Registry image reference with a tag or digest, got: $image_ref" >&2
    exit 1
    ;;
esac

digest="$(gcloud artifacts docker images describe "$image_ref" --format='value(image_summary.digest)')"

if [ -z "$digest" ]; then
  echo "failed to resolve digest for Artifact Registry image: $image_ref" >&2
  exit 1
fi

printf '%s@%s\n' "${image_ref%:*}" "$digest"
