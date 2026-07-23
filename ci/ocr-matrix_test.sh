#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="${repo_root}/ci/fixtures/ocr-multi-model.yaml"
test_root="$(mktemp -d)"
cleanup() {
  rm -rf "${test_root}"
}
trap cleanup EXIT

fail() {
  echo "OCR matrix route contract failed: $*" >&2
  exit 1
}

bash "${repo_root}/ci/ocr-local-defaults-contract_test.sh"

matrix="$(
  GCLOUD_PROJECT=scribe-test \
    WORKSPACE_SLUG=prod \
    IMAGE_TAG=0123456789abcdef \
    CONFIG_PATH="${fixture}" \
    "${repo_root}/ci/ocr-matrix.sh"
)"

jq -e '.include | length == 6' <<<"${matrix}" >/dev/null ||
  fail "two segmentation models, two transcription models, the generic segmentor, and Ollama were not emitted"

entry_has_build_arg() {
  local key="$1"
  local expected="$2"
  jq -e --arg key "${key}" --arg expected "${expected}" '
    any(
      .include[];
      .key == $key and any((.build_args | split("\n"))[]; . == $expected)
    )
  ' <<<"${matrix}" >/dev/null
}

entry_has_build_arg "segmentor" "KRAKEN_SEGMENTATION_MODEL_ID=layout-default" ||
  fail "generic segmentor does not bake the default segmentation route ID"
entry_has_build_arg "segmentor" "KRAKEN_TRANSCRIPTION_MODEL_ID=handwriting-default" ||
  fail "generic segmentor does not bake the default transcription route ID"
entry_has_build_arg "kraken-seg/layout-lines-v2" "KRAKEN_SEGMENTATION_MODEL_ID=layout-lines-v2" ||
  fail "additional segmentation image does not bake its public route ID"
entry_has_build_arg "kraken-seg/layout-lines-v2" "KRAKEN_SEGMENTATION_MODEL_FILE=segmentation-engine.mlmodel" ||
  fail "additional segmentation route does not retain its distinct baked filename"
entry_has_build_arg "kraken-ocr/latin-handwriting-v2" "KRAKEN_TRANSCRIPTION_MODEL_ID=latin-handwriting-v2" ||
  fail "additional transcription image does not bake its public model ID"
entry_has_build_arg "kraken-ocr/latin-handwriting-v2" "KRAKEN_RECOGNITION_MODEL_FILE=transcription-engine.mlmodel" ||
  fail "additional transcription route does not retain its distinct baked filename"
while IFS= read -r transcription_route; do
  for empty_segmentation_arg in \
    "KRAKEN_SEGMENTATION_MODEL_ID=" \
    "KRAKEN_SEGMENTATION_MODEL_FILE=" \
    "KRAKEN_SEGMENTATION_MODEL_DOI=" \
    "KRAKEN_SEGMENTATION_MODEL_SHA256="; do
    entry_has_build_arg "$transcription_route" "$empty_segmentation_arg" ||
      fail "$transcription_route unexpectedly configures $empty_segmentation_arg"
  done
done < <(jq -r '.include[].key | select(startswith("kraken-ocr/"))' <<<"${matrix}")

transcription_terraform="$(
  sed -n \
    '/^  kraken_transcription_service_defs = {$/,/^  ocr_services = merge(/p' \
    "${repo_root}/terraform/kraken.tf"
)"
for required in \
  '{ name = "KRAKEN_SEGMENTATION_MODEL_ID", value = "" }' \
  '{ name = "KRAKEN_SEGMENTATION_MODEL", value = "" }'; do
  grep -Fq "$required" <<<"${transcription_terraform}" ||
    fail "dedicated transcription services do not disable the segmentation route"
done
if grep -Fq 'local.kraken_default_segmentation' <<<"${transcription_terraform}"; then
  fail "dedicated transcription services still configure the default segmentation route"
fi

jq -e '
  all(
    .include[] | select(.key | startswith("kraken-seg/"));
    (.key | ltrimstr("kraken-seg/")) as $route |
    any((.build_args | split("\n"))[]; . == "KRAKEN_SEGMENTATION_MODEL_ID=\($route)")
  ) and
  all(
    .include[] | select(.key | startswith("kraken-ocr/"));
    (.key | ltrimstr("kraken-ocr/")) as $route |
    any((.build_args | split("\n"))[]; . == "KRAKEN_TRANSCRIPTION_MODEL_ID=\($route)")
  )
' <<<"${matrix}" >/dev/null ||
  fail "a Terraform image key and its baked public model ID diverged"

invalid_default="${test_root}/invalid-default.yaml"
sed 's/^  default_model: "vision-default:latest"$/  default_model: "not-installed"/' "${fixture}" >"${invalid_default}"
if GCLOUD_PROJECT=scribe-test WORKSPACE_SLUG=prod IMAGE_TAG=test \
  CONFIG_PATH="${invalid_default}" "${repo_root}/ci/ocr-matrix.sh" >/dev/null 2>&1; then
  fail "an undeclared Ollama default model was accepted"
fi

missing_artifact="${test_root}/missing-artifact.yaml"
sed '/doi: "10.5281\/zenodo.10000004"/d' "${fixture}" >"${missing_artifact}"
if GCLOUD_PROJECT=scribe-test WORKSPACE_SLUG=prod IMAGE_TAG=test \
  CONFIG_PATH="${missing_artifact}" "${repo_root}/ci/ocr-matrix.sh" >/dev/null 2>&1; then
  fail "an advertised Kraken model without a complete baked artifact was accepted"
fi

colliding_model_files="${test_root}/colliding-model-files.yaml"
sed 's/file: default-layout\.mlmodel/file: DEFAULT-TRANSCRIBER.mlmodel/' \
  "${fixture}" >"${colliding_model_files}"
if GCLOUD_PROJECT=scribe-test WORKSPACE_SLUG=prod IMAGE_TAG=test \
  CONFIG_PATH="${colliding_model_files}" "${repo_root}/ci/ocr-matrix.sh" >/dev/null 2>&1; then
  fail "transcription and segmentation artifacts with colliding filenames were accepted"
fi

nondefault_shared_filename="${test_root}/nondefault-shared-filename.yaml"
sed 's/file: transcription-engine\.mlmodel/file: default-layout.mlmodel/' \
  "${fixture}" >"${nondefault_shared_filename}"
GCLOUD_PROJECT=scribe-test WORKSPACE_SLUG=prod IMAGE_TAG=test \
  CONFIG_PATH="${nondefault_shared_filename}" "${repo_root}/ci/ocr-matrix.sh" >/dev/null ||
  fail "an isolated nondefault transcription image could not reuse a filename from another image"

deploy_default="$(yq -r '.ollama.default_model // ""' "${repo_root}/config/ocr.yaml")"
runtime_default="$(yq -r '.llm.ollama.model // ""' "${repo_root}/config.yaml")"
embedded_default="$(yq -r '.llm.ollama.model // ""' "${repo_root}/internal/config/defaults/config.yaml")"
if [ -z "${deploy_default}" ] || [ "${deploy_default}" != "${runtime_default}" ] || [ "${deploy_default}" != "${embedded_default}" ]; then
  fail "deploy-time, runtime, and embedded Ollama defaults diverged"
fi

echo "OCR two-model build matrix route contracts passed."
