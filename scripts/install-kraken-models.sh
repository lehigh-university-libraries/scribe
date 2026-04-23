#!/usr/bin/env bash

set -euo pipefail

KRAKEN_MODEL_DIR="${KRAKEN_MODEL_DIR:-/models/kraken}"
KRAKEN_TMP_DATA_DIR="${KRAKEN_TMP_DATA_DIR:-/tmp/kraken-data}"

mkdir -p "${KRAKEN_MODEL_DIR}" "${KRAKEN_TMP_DATA_DIR}"

export XDG_DATA_HOME="${KRAKEN_TMP_DATA_DIR}"

install_model() {
  local doi="$1"
  local filename="$2"

  if [ -z "${doi}" ]; then
    return 0
  fi

  kraken get "${doi}"

  local search_root="${XDG_DATA_HOME}/htrmopo"
  local source_path=""
  if [ -n "${filename}" ]; then
    source_path="$(find "${search_root}" -type f -name "${filename}" | head -n 1 || true)"
  fi
  if [ -z "${source_path}" ]; then
    source_path="$(find "${search_root}" -type f -name '*.mlmodel' | sort | tail -n 1 || true)"
  fi
  if [ -z "${source_path}" ]; then
    echo "Unable to find downloaded kraken model for DOI ${doi}" >&2
    return 1
  fi

  local target_name
  target_name="${filename:-$(basename "${source_path}")}"
  cp "${source_path}" "${KRAKEN_MODEL_DIR}/${target_name}"
}

install_model "${KRAKEN_RECOGNITION_MODEL_DOI:-}" "${KRAKEN_RECOGNITION_MODEL_FILE:-}"
install_model "${KRAKEN_SEGMENTATION_MODEL_DOI:-}" "${KRAKEN_SEGMENTATION_MODEL_FILE:-}"
