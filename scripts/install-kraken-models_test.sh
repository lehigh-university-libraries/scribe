#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

transcription_fixture="${test_root}/transcription.mlmodel"
segmentation_fixture="${test_root}/segmentation.mlmodel"
printf 'deterministic transcription fixture\n' > "${transcription_fixture}"
printf 'deterministic segmentation fixture\n' > "${segmentation_fixture}"
transcription_sha256="$(sha256sum "${transcription_fixture}" | awk '{print $1}')"
segmentation_sha256="$(sha256sum "${segmentation_fixture}" | awk '{print $1}')"

# Each installer scenario runs in a subshell so sourced-script variables and
# function overrides cannot leak into the next acceptance case.
# shellcheck disable=SC2030,SC2031
run_installer() (
  export TRANSCRIPTION_FIXTURE_SOURCE="${transcription_fixture}"
  export SEGMENTATION_FIXTURE_SOURCE="${segmentation_fixture}"
  export KRAKEN_MODEL_DIR="${test_root}/models-$1"
  export KRAKEN_TMP_DATA_DIR="${test_root}/data-$1"
  export KRAKEN_TRANSCRIPTION_MODEL_ID="latin-handwriting-v2"
  export KRAKEN_RECOGNITION_MODEL_DOI="10.5281/zenodo.10592716"
  export KRAKEN_RECOGNITION_MODEL_FILE="transcription-engine.mlmodel"
  export KRAKEN_RECOGNITION_MODEL_SHA256="$2"
  export KRAKEN_SEGMENTATION_MODEL_ID="layout-lines-v2"
  export KRAKEN_SEGMENTATION_MODEL_DOI="10.5281/zenodo.14602569"
  export KRAKEN_SEGMENTATION_MODEL_FILE="segmentation-engine.mlmodel"
  export KRAKEN_SEGMENTATION_MODEL_SHA256="$3"
  export KRAKEN_MODEL_DOWNLOAD_RETRY_DELAY_SECONDS=0
  export KRAKEN_TEST_FAILURE_MODE="${4:-none}"
  export KRAKEN_TEST_FAILURES_BEFORE_SUCCESS="${5:-0}"
  export KRAKEN_TEST_ATTEMPT_FILE="${test_root}/attempts-$1"
  export KRAKEN_TEST_DIRECT_URL_FILE="${test_root}/direct-url-$1"

  case "${6:-none}" in
    valid)
      mkdir -p "${KRAKEN_MODEL_DIR}"
      cp "${TRANSCRIPTION_FIXTURE_SOURCE}" "${KRAKEN_MODEL_DIR}/${KRAKEN_RECOGNITION_MODEL_FILE}"
      cp "${SEGMENTATION_FIXTURE_SOURCE}" "${KRAKEN_MODEL_DIR}/${KRAKEN_SEGMENTATION_MODEL_FILE}"
      ;;
    invalid)
      mkdir -p "${KRAKEN_MODEL_DIR}"
      printf 'invalid existing model\n' > "${KRAKEN_MODEL_DIR}/${KRAKEN_RECOGNITION_MODEL_FILE}"
      ;;
  esac

  # Invoked indirectly by the sourced installer after it resolves the model.
  # shellcheck disable=SC2329
  kraken() {
    if [ "$1" != "get" ]; then
      return 2
    fi

    local attempt=0
    if [ -f "${KRAKEN_TEST_ATTEMPT_FILE}" ]; then
      attempt="$(cat "${KRAKEN_TEST_ATTEMPT_FILE}")"
    fi
    attempt=$((attempt + 1))
    printf '%s\n' "${attempt}" > "${KRAKEN_TEST_ATTEMPT_FILE}"

    mkdir -p "${XDG_DATA_HOME}/htrmopo"
    if [ "${attempt}" -le "${KRAKEN_TEST_FAILURES_BEFORE_SUCCESS}" ]; then
      case "${KRAKEN_TEST_FAILURE_MODE}" in
        command)
          printf 'partial download\n' > "${XDG_DATA_HOME}/htrmopo/${KRAKEN_RECOGNITION_MODEL_FILE}"
          return 75
          ;;
        missing)
          return 0
          ;;
        corrupt)
          printf 'upstream gateway error\n' > "${XDG_DATA_HOME}/htrmopo/${KRAKEN_RECOGNITION_MODEL_FILE}"
          return 0
          ;;
      esac
    fi

    case "$2" in
      "${KRAKEN_RECOGNITION_MODEL_DOI}")
        if [ "${KRAKEN_TEST_FAILURE_MODE}" = "unindexed" ]; then
          return 69
        fi
        cp "${TRANSCRIPTION_FIXTURE_SOURCE}" "${XDG_DATA_HOME}/htrmopo/${KRAKEN_RECOGNITION_MODEL_FILE}"
        ;;
      "${KRAKEN_SEGMENTATION_MODEL_DOI}")
        cp "${SEGMENTATION_FIXTURE_SOURCE}" "${XDG_DATA_HOME}/htrmopo/${KRAKEN_SEGMENTATION_MODEL_FILE}"
        ;;
      *)
        return 2
        ;;
    esac
  }

  # Invoked only when the HTRMoPo-backed command cannot supply a verified
  # artifact. This mock preserves the retry scenarios without network access.
  # shellcheck disable=SC2329
  curl() {
    local output_path=""
    local url=""
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --output)
          shift
          output_path="${1:-}"
          ;;
        https://*)
          url="$1"
          ;;
      esac
      shift
    done

    printf '%s\n' "${url}" >> "${KRAKEN_TEST_DIRECT_URL_FILE}"
    local attempt
    attempt="$(cat "${KRAKEN_TEST_ATTEMPT_FILE}")"
    if [ "${attempt}" -le "${KRAKEN_TEST_FAILURES_BEFORE_SUCCESS}" ]; then
      return 22
    fi

    mkdir -p "$(dirname "${output_path}")"
    case "${url}" in
      "https://zenodo.org/records/10592716/files/${KRAKEN_RECOGNITION_MODEL_FILE}?download=1")
        cp "${TRANSCRIPTION_FIXTURE_SOURCE}" "${output_path}"
        ;;
      "https://zenodo.org/records/14602569/files/${KRAKEN_SEGMENTATION_MODEL_FILE}?download=1")
        cp "${SEGMENTATION_FIXTURE_SOURCE}" "${output_path}"
        ;;
      *)
        return 22
        ;;
    esac
  }

  # shellcheck source=scripts/install-kraken-models.sh
  source "${repo_root}/scripts/install-kraken-models.sh"
)

run_installer valid "${transcription_sha256}" "${segmentation_sha256}"
cmp "${transcription_fixture}" "${test_root}/models-valid/transcription-engine.mlmodel"
cmp "${segmentation_fixture}" "${test_root}/models-valid/segmentation-engine.mlmodel"

run_installer unindexed "${transcription_sha256}" "${segmentation_sha256}" unindexed
cmp "${transcription_fixture}" "${test_root}/models-unindexed/transcription-engine.mlmodel"
if [ "$(cat "${test_root}/direct-url-unindexed")" != \
  "https://zenodo.org/records/10592716/files/transcription-engine.mlmodel?download=1" ]; then
  echo "Kraken installer did not use the exact configured Zenodo record and filename" >&2
  exit 1
fi
if [ "$(cat "${test_root}/attempts-unindexed")" -ne 2 ]; then
  echo "Kraken installer retried an unindexed model after its direct download succeeded" >&2
  exit 1
fi

run_installer command-retry "${transcription_sha256}" "${segmentation_sha256}" command 1
cmp "${transcription_fixture}" "${test_root}/models-command-retry/transcription-engine.mlmodel"
cmp "${segmentation_fixture}" "${test_root}/models-command-retry/segmentation-engine.mlmodel"
if [ "$(cat "${test_root}/attempts-command-retry")" -ne 3 ]; then
  echo "Kraken installer did not retry a failed download command exactly once" >&2
  exit 1
fi
if find "${test_root}/data-command-retry" -mindepth 1 -print -quit | grep -q .; then
  echo "Kraken installer retained model download scratch data" >&2
  exit 1
fi

run_installer missing-retry "${transcription_sha256}" "${segmentation_sha256}" missing 1
cmp "${transcription_fixture}" "${test_root}/models-missing-retry/transcription-engine.mlmodel"
if [ "$(cat "${test_root}/attempts-missing-retry")" -ne 3 ]; then
  echo "Kraken installer did not retry a successful download with a missing model" >&2
  exit 1
fi

run_installer corrupt-retry "${transcription_sha256}" "${segmentation_sha256}" corrupt 1
cmp "${transcription_fixture}" "${test_root}/models-corrupt-retry/transcription-engine.mlmodel"
if [ "$(cat "${test_root}/attempts-corrupt-retry")" -ne 3 ]; then
  echo "Kraken installer did not retry a corrupt model download" >&2
  exit 1
fi

run_installer cached "${transcription_sha256}" "${segmentation_sha256}" none 0 valid
if [ -e "${test_root}/attempts-cached" ]; then
  echo "Kraken installer downloaded models that were already checksum-valid" >&2
  exit 1
fi

run_installer replace-invalid "${transcription_sha256}" "${segmentation_sha256}" none 0 invalid
cmp "${transcription_fixture}" "${test_root}/models-replace-invalid/transcription-engine.mlmodel"
if [ "$(cat "${test_root}/attempts-replace-invalid")" -ne 2 ]; then
  echo "Kraken installer accepted an invalid existing model" >&2
  exit 1
fi

if run_installer exhausted "${transcription_sha256}" "${segmentation_sha256}" command 4 >/dev/null 2>&1; then
  echo "Kraken installer accepted a model after exhausting download retries" >&2
  exit 1
fi
if [ "$(cat "${test_root}/attempts-exhausted")" -ne 4 ]; then
  echo "Kraken installer did not stop after the bounded download attempts" >&2
  exit 1
fi
if [ -e "${test_root}/models-exhausted/transcription-engine.mlmodel" ]; then
  echo "Kraken installer copied a partial model after exhausting download retries" >&2
  exit 1
fi

if run_installer tampered "$(printf '0%.0s' {1..64})" "${segmentation_sha256}" >/dev/null 2>&1; then
  echo "Kraken installer accepted a model with the wrong digest" >&2
  exit 1
fi
if [ -e "${test_root}/models-tampered/transcription-engine.mlmodel" ]; then
  echo "Kraken installer copied a model before verifying its digest" >&2
  exit 1
fi

# shellcheck disable=SC2030,SC2031
if (
  export KRAKEN_MODEL_DIR="${test_root}/models-collision"
  export KRAKEN_TMP_DATA_DIR="${test_root}/data-collision"
  export KRAKEN_TRANSCRIPTION_MODEL_ID="latin-handwriting-v2"
  export KRAKEN_RECOGNITION_MODEL_DOI="10.5281/zenodo.10592716"
  export KRAKEN_RECOGNITION_MODEL_FILE="shared-model.mlmodel"
  export KRAKEN_RECOGNITION_MODEL_SHA256="${transcription_sha256}"
  export KRAKEN_SEGMENTATION_MODEL_ID="layout-lines-v2"
  export KRAKEN_SEGMENTATION_MODEL_DOI="10.5281/zenodo.14602569"
  export KRAKEN_SEGMENTATION_MODEL_FILE="SHARED-MODEL.mlmodel"
  export KRAKEN_SEGMENTATION_MODEL_SHA256="${segmentation_sha256}"
  # shellcheck source=scripts/install-kraken-models.sh
  source "${repo_root}/scripts/install-kraken-models.sh"
) >/dev/null 2>&1; then
  echo "Kraken installer accepted transcription and segmentation artifacts with colliding filenames" >&2
  exit 1
fi
if [ -e "${test_root}/models-collision" ] || [ -e "${test_root}/data-collision" ]; then
  echo "Kraken installer mutated the model filesystem before rejecting colliding filenames" >&2
  exit 1
fi

# shellcheck disable=SC2030,SC2031
if (
  export KRAKEN_MODEL_DIR="${test_root}/models-invalid-doi"
  export KRAKEN_TMP_DATA_DIR="${test_root}/data-invalid-doi"
  export KRAKEN_TRANSCRIPTION_MODEL_ID="latin-handwriting-v2"
  export KRAKEN_RECOGNITION_MODEL_DOI="https://example.invalid/model"
  export KRAKEN_RECOGNITION_MODEL_FILE="transcription-engine.mlmodel"
  export KRAKEN_RECOGNITION_MODEL_SHA256="${transcription_sha256}"
  export KRAKEN_SEGMENTATION_MODEL_ID=""
  export KRAKEN_SEGMENTATION_MODEL_DOI=""
  export KRAKEN_SEGMENTATION_MODEL_FILE=""
  export KRAKEN_SEGMENTATION_MODEL_SHA256=""
  # shellcheck source=scripts/install-kraken-models.sh
  source "${repo_root}/scripts/install-kraken-models.sh"
) >/dev/null 2>&1; then
  echo "Kraken installer accepted a model DOI outside the exact Zenodo record namespace" >&2
  exit 1
fi
if [ -e "${test_root}/models-invalid-doi/transcription-engine.mlmodel" ]; then
  echo "Kraken installer published a model from an invalid DOI" >&2
  exit 1
fi

# shellcheck disable=SC2031
if (
  export KRAKEN_MODEL_DIR="${test_root}/models-missing-id"
  export KRAKEN_TMP_DATA_DIR="${test_root}/data-missing-id"
  export KRAKEN_TRANSCRIPTION_MODEL_ID=""
  export KRAKEN_RECOGNITION_MODEL_DOI="10.5281/zenodo.10592716"
  export KRAKEN_RECOGNITION_MODEL_FILE="transcription-engine.mlmodel"
  export KRAKEN_RECOGNITION_MODEL_SHA256="${transcription_sha256}"
  export KRAKEN_SEGMENTATION_MODEL_ID=""
  export KRAKEN_SEGMENTATION_MODEL_DOI=""
  export KRAKEN_SEGMENTATION_MODEL_FILE=""
  export KRAKEN_SEGMENTATION_MODEL_SHA256=""
  # shellcheck source=scripts/install-kraken-models.sh
  source "${repo_root}/scripts/install-kraken-models.sh"
) >/dev/null 2>&1; then
  echo "Kraken installer accepted a baked model without a public model ID" >&2
  exit 1
fi

echo "Kraken two-model ID and checksum enforcement passed"
