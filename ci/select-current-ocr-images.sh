#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
input_path="${1:--}"

if [ "$#" -gt 1 ]; then
  echo "usage: $0 [recorded-ocr-images.json|-]" >&2
  exit 2
fi

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
: "${WORKSPACE_SLUG:?WORKSPACE_SLUG is required}"

include_ollama="${INCLUDE_OLLAMA:-true}"
case "${include_ollama}" in
  true | false) ;;
  *)
    echo "INCLUDE_OLLAMA must be true or false" >&2
    exit 2
    ;;
esac

matrix="$(
  IMAGE_TAG=rollback-selection \
    make --no-print-directory -C "${repo_root}" ocr-matrix
)"
required_keys="$(
  jq -c --argjson include_ollama "${include_ollama}" '
    [
      .include[].key
      | select($include_ollama or (startswith("ollama/") | not))
    ]
    | unique
    | sort
  ' <<<"${matrix}"
)"

if ! selected="$(
  jq -ceS --argjson required "${required_keys}" '
    . as $recorded
    | if
        type == "object" and
        all($required[]; $recorded[.] | type == "string" and test("@sha256:[0-9a-f]{64}$"))
      then
        reduce $required[] as $key ({}; . + {($key): $recorded[$key]})
      else
        error("recorded OCR images do not cover the current service matrix")
      end
  ' "${input_path}"
)"; then
  echo "Recorded OCR images are missing or invalid for the current service matrix." >&2
  exit 1
fi

printf '%s\n' "${selected}"
