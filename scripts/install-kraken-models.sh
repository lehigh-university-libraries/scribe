#!/usr/bin/env bash

set -euo pipefail

KRAKEN_MODEL_DIR="${KRAKEN_MODEL_DIR:-/models/kraken}"
KRAKEN_TMP_DATA_DIR="${KRAKEN_TMP_DATA_DIR:-/tmp/kraken-data}"
KRAKEN_MODEL_DOWNLOAD_ATTEMPTS="${KRAKEN_MODEL_DOWNLOAD_ATTEMPTS:-4}"
KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS="${KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS:-5}"

if [[ ! "${KRAKEN_MODEL_DOWNLOAD_ATTEMPTS}" =~ ^([1-9]|10)$ ]]; then
  echo "KRAKEN_MODEL_DOWNLOAD_ATTEMPTS must be an integer from 1 through 10" >&2
  exit 1
fi
if [[ ! "${KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS}" =~ ^(0|[1-9][0-9]{0,2})$ ]] ||
  [ "${KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS}" -gt 300 ]; then
  echo "KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS must be an integer from 0 through 300" >&2
  exit 1
fi

transcription_filename="${KRAKEN_RECOGNITION_MODEL_FILE:-}"
segmentation_filename="${KRAKEN_SEGMENTATION_MODEL_FILE:-}"
if [ -n "${transcription_filename}" ] &&
  [ -n "${segmentation_filename}" ] &&
  [ "$(printf '%s' "${transcription_filename}" | tr '[:upper:]' '[:lower:]')" = \
    "$(printf '%s' "${segmentation_filename}" | tr '[:upper:]' '[:lower:]')" ]; then
  echo "Transcription and segmentation models must use distinct baked filenames: ${transcription_filename}" >&2
  exit 1
fi

if ! mkdir -p "${KRAKEN_MODEL_DIR}" "${KRAKEN_TMP_DATA_DIR}"; then
  echo "Failed to create Kraken model directories" >&2
  exit 1
fi

model_matches_checksum() {
  local path="$1"
  local expected_sha256="$2"

  [ -f "${path}" ] &&
    [ ! -L "${path}" ] &&
    printf '%s  %s\n' "${expected_sha256}" "${path}" | sha256sum -c - >/dev/null 2>&1
}

publish_model() {
  local source_path="$1"
  local destination="$2"
  local filename="$3"
  local staged_path=""

  if ! staged_path="$(mktemp "${KRAKEN_MODEL_DIR}/.${filename}.XXXXXX")"; then
    return 1
  fi
  if ! cp -- "${source_path}" "${staged_path}" ||
    ! chmod 0644 "${staged_path}" ||
    ! mv -f -- "${staged_path}" "${destination}"; then
    rm -f -- "${staged_path}"
    return 1
  fi
}

install_model() {
  local label="$1"
  local model_id="$2"
  local doi="$3"
  local filename="$4"
  local expected_sha256="$5"

  if [ -z "${doi}" ]; then
    if [ -n "${model_id}${filename}${expected_sha256}" ]; then
      echo "${label} model ID, DOI, filename, and SHA-256 must be configured together" >&2
      return 1
    fi
    return 0
  fi

  if [[ ! "${model_id}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || [[ "${model_id}" == *..* ]]; then
    echo "Invalid ${label} model ID: ${model_id}" >&2
    return 1
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

  local destination="${KRAKEN_MODEL_DIR}/${filename}"
  if model_matches_checksum "${destination}" "${expected_sha256}"; then
    if ! chmod 0644 "${destination}"; then
      echo "Checksum-valid Kraken model could not be made readable: ${destination}" >&2
      return 1
    fi
    return 0
  fi
  if [ -e "${destination}" ] || [ -L "${destination}" ]; then
    if ! rm -f -- "${destination}"; then
      echo "Invalid existing Kraken model could not be removed: ${destination}" >&2
      return 1
    fi
  fi

  local model_data_root="${KRAKEN_TMP_DATA_DIR}/${label}-${model_id}"
  local attempt
  for ((attempt = 1; attempt <= KRAKEN_MODEL_DOWNLOAD_ATTEMPTS; attempt++)); do
    # Kraken and HTRMoPo use separate XDG data and metadata caches. Give every
    # retry a clean, model-scoped root so a successful request cannot consume
    # files or metadata left by a timed-out attempt.
    if ! rm -rf -- "${model_data_root}" ||
      ! mkdir -p "${model_data_root}/data" "${model_data_root}/cache"; then
      echo "Failed to reset Kraken model scratch directory: ${model_data_root}" >&2
      return 1
    fi

    local failure_reason=""
    local source_paths=()
    if ! XDG_DATA_HOME="${model_data_root}/data" \
      XDG_CACHE_HOME="${model_data_root}/cache" \
      kraken get "${doi}"; then
      failure_reason="download command failed"
    else
      mapfile -d '' -t source_paths < <(
        find "${model_data_root}/data/htrmopo" -type f -name "${filename}" -print0 2>/dev/null
      )
      if [ "${#source_paths[@]}" -ne 1 ]; then
        failure_reason="download did not contain exactly one ${filename}"
      elif ! model_matches_checksum "${source_paths[0]}" "${expected_sha256}"; then
        failure_reason="${filename} failed SHA-256 verification"
      elif ! publish_model "${source_paths[0]}" "${destination}" "${filename}"; then
        echo "Failed to publish verified Kraken model ${filename}" >&2
        rm -rf -- "${model_data_root}"
        return 1
      else
        if ! rm -rf -- "${model_data_root}"; then
          echo "Failed to clean Kraken model scratch directory: ${model_data_root}" >&2
          return 1
        fi
        return 0
      fi
    fi

    echo "Kraken model DOI ${doi} attempt ${attempt} failed: ${failure_reason}" >&2
    if [ "${attempt}" -eq "${KRAKEN_MODEL_DOWNLOAD_ATTEMPTS}" ]; then
      rm -rf -- "${model_data_root}"
      echo "Failed to install Kraken model DOI ${doi} after ${attempt} attempts" >&2
      return 1
    fi
    if [ "${KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS}" -gt 0 ]; then
      sleep "$((KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS * attempt))"
    fi
  done
}

install_model "transcription" "${KRAKEN_TRANSCRIPTION_MODEL_ID:-}" "${KRAKEN_RECOGNITION_MODEL_DOI:-}" "${KRAKEN_RECOGNITION_MODEL_FILE:-}" "${KRAKEN_RECOGNITION_MODEL_SHA256:-}" || exit 1
install_model "segmentation" "${KRAKEN_SEGMENTATION_MODEL_ID:-}" "${KRAKEN_SEGMENTATION_MODEL_DOI:-}" "${KRAKEN_SEGMENTATION_MODEL_FILE:-}" "${KRAKEN_SEGMENTATION_MODEL_SHA256:-}" || exit 1
