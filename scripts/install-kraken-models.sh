#!/usr/bin/env bash

set -euo pipefail

KRAKEN_MODEL_DIR="${KRAKEN_MODEL_DIR:-/models/kraken}"
KRAKEN_TMP_DATA_DIR="${KRAKEN_TMP_DATA_DIR:-/tmp/kraken-data}"

mkdir -p "${KRAKEN_MODEL_DIR}" "${KRAKEN_TMP_DATA_DIR}"

export XDG_DATA_HOME="${KRAKEN_TMP_DATA_DIR}"

install_model() {
  local doi="$1"
  local filename="$2"
  local expected_sha256="$3"

  if [ -z "${doi}" ]; then
    return 0
  fi

  if [ -z "${filename}" ]; then
    echo "A model filename is required for DOI ${doi}" >&2
    return 1
  fi
  case "${filename}" in
    *[!A-Za-z0-9._-]* | .* | *..* | *.)
      echo "Invalid kraken model filename: ${filename}" >&2
      return 1
      ;;
    *.mlmodel) ;;
    *)
      echo "Kraken model filename must end in .mlmodel: ${filename}" >&2
      return 1
      ;;
  esac
  if [[ ! "${expected_sha256}" =~ ^[0-9a-f]{64}$ ]]; then
    echo "A lowercase SHA-256 digest is required for Kraken model ${filename}" >&2
    return 1
  fi

  kraken get "${doi}"

  local search_root="${XDG_DATA_HOME}/htrmopo"
  local source_path=""
  source_path="$(find "${search_root}" -type f -name "${filename}" -print -quit 2>/dev/null || true)"
  if [ -z "${source_path}" ]; then
    echo "Downloaded DOI ${doi} did not contain the declared model ${filename}" >&2
    return 1
  fi

  if ! printf '%s  %s\n' "${expected_sha256}" "${source_path}" | sha256sum -c - >/dev/null; then
    echo "Downloaded DOI ${doi} failed SHA-256 verification for ${filename}" >&2
    return 1
  fi

  cp "${source_path}" "${KRAKEN_MODEL_DIR}/${filename}"
}

install_model "${KRAKEN_RECOGNITION_MODEL_DOI:-}" "${KRAKEN_RECOGNITION_MODEL_FILE:-}" "${KRAKEN_RECOGNITION_MODEL_SHA256:-}" || exit 1
install_model "${KRAKEN_SEGMENTATION_MODEL_DOI:-}" "${KRAKEN_SEGMENTATION_MODEL_FILE:-}" "${KRAKEN_SEGMENTATION_MODEL_SHA256:-}" || exit 1
