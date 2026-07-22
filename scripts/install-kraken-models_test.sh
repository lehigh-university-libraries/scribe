#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_root="$(mktemp -d)"
cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

fixture="${test_root}/verified.mlmodel"
printf 'deterministic kraken fixture\n' > "${fixture}"
fixture_sha256="$(sha256sum "${fixture}" | awk '{print $1}')"

run_installer() (
  export MODEL_FIXTURE_SOURCE="${fixture}"
  export KRAKEN_MODEL_DIR="${test_root}/models-$1"
  export KRAKEN_TMP_DATA_DIR="${test_root}/data-$1"
  export KRAKEN_RECOGNITION_MODEL_DOI="10.5281/zenodo.10592716"
  export KRAKEN_RECOGNITION_MODEL_FILE="verified.mlmodel"
  export KRAKEN_RECOGNITION_MODEL_SHA256="$2"
  export KRAKEN_SEGMENTATION_MODEL_DOI=""
  export KRAKEN_SEGMENTATION_MODEL_FILE=""
  export KRAKEN_SEGMENTATION_MODEL_SHA256=""

  # Invoked indirectly by the sourced installer after it resolves the model.
  # shellcheck disable=SC2329
  kraken() {
    if [ "$1" != "get" ] || [ "$2" != "${KRAKEN_RECOGNITION_MODEL_DOI}" ]; then
      return 2
    fi
    mkdir -p "${XDG_DATA_HOME}/htrmopo"
    cp "${MODEL_FIXTURE_SOURCE}" "${XDG_DATA_HOME}/htrmopo/${KRAKEN_RECOGNITION_MODEL_FILE}"
  }

  # shellcheck source=scripts/install-kraken-models.sh
  source "${repo_root}/scripts/install-kraken-models.sh"
)

run_installer valid "${fixture_sha256}"
cmp "${fixture}" "${test_root}/models-valid/verified.mlmodel"

if run_installer tampered "$(printf '0%.0s' {1..64})" >/dev/null 2>&1; then
  echo "Kraken installer accepted a model with the wrong digest" >&2
  exit 1
fi
if [ -e "${test_root}/models-tampered/verified.mlmodel" ]; then
  echo "Kraken installer copied a model before verifying its digest" >&2
  exit 1
fi

echo "Kraken model checksum enforcement passed"
