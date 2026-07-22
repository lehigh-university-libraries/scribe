#!/usr/bin/env bash
#
# Emit a GitHub Actions build matrix for every Cloud Run OCR image derivable
# from config/ocr.yaml. The output is a single JSON object on stdout:
#
#   {"include": [ {...entry...}, ... ]}
#
# Each entry contains:
#   key           Stable service key used by Terraform (e.g. "kraken-seg/kraken").
#   service_name  GAR image name segment. Image names are workspace-independent
#                 so every workspace can share the same GAR objects.
#   ghcr_image    GHCR image name segment passed to the shared build workflow.
#   gar_image     GAR image repository path without the tag suffix.
#   image         Full GAR image reference with tag (no digest).
#   context       Docker build context, relative to the repo root.
#   dockerfile    Dockerfile path, relative to the build context.
#   file          Dockerfile path relative to the repo root.
#   build_args    Newline-delimited KEY=VALUE docker build args.
#   platform      Target platform passed to docker buildx.
#
# Required env:
#   GCLOUD_PROJECT   GCP project ID.
#   WORKSPACE_SLUG   Terraform workspace slug (prod, pr-123, dev-...).
#   IMAGE_TAG        Tag applied to every built image (git sha or branch slug).
#
# Optional env:
#   CONFIG_PATH      Path to OCR deploy metadata (default: config/ocr.yaml).
#   GAR_LOCATION     Artifact Registry location (default: us).
#   GAR_REPOSITORY   Artifact Registry repository (default: internal).

set -euo pipefail

: "${GCLOUD_PROJECT:?GCLOUD_PROJECT is required}"
: "${WORKSPACE_SLUG:?WORKSPACE_SLUG is required}"
: "${IMAGE_TAG:?IMAGE_TAG is required}"

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
config_path="${CONFIG_PATH:-$repo_root/config/ocr.yaml}"
gar_location="${GAR_LOCATION:-us}"
gar_repository="${GAR_REPOSITORY:-internal}"
gar_repo="${gar_location}-docker.pkg.dev/${GCLOUD_PROJECT}/${gar_repository}"

if ! command -v yq >/dev/null 2>&1; then
  echo "yq is required on PATH (https://github.com/mikefarah/yq)" >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required on PATH" >&2
  exit 1
fi

# WORKSPACE_SLUG is accepted for API parity with Terraform, but OCR image
# contents do not depend on it. Built images are keyed only by service role and
# model identity so every workspace (prod/dev/pr-N) can share the same GAR
# objects without rebuilds.
: "${WORKSPACE_SLUG}"

hash8() {
  # 8-char md5 prefix, stable across platforms.
  printf '%s' "$1" | md5sum 2>/dev/null | awk '{print substr($1,1,8)}' \
    || printf '%s' "$1" | openssl dgst -md5 -hex | awk '{print substr($2,1,8)}'
}

slugify() {
  # Use a POSIX BRE-safe pattern so this behaves the same on GNU and BSD sed.
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed 's/[^a-z0-9][^a-z0-9]*/-/g; s/^-//; s/-$//'
}

gar_image() {
  case "$IMAGE_TAG" in
    *:*)
      echo "IMAGE_TAG must be a valid Docker tag and cannot contain ':': $IMAGE_TAG" >&2
      exit 1
      ;;
  esac
  printf '%s/%s:%s' "$gar_repo" "$1" "$IMAGE_TAG"
}

entries_json="[]"

append_entry() {
  local entry="$1"
  entries_json="$(jq --argjson e "$entry" '. + [$e]' <<<"$entries_json")"
}

build_args_json() {
  # Join arguments into a JSON string with newline separators, matching the
  # multi-line format docker/build-push-action expects for build-args input.
  local out=""
  for pair in "$@"; do
    if [ -z "$pair" ]; then continue; fi
    if [ -z "$out" ]; then out="$pair"; else out="${out}"$'\n'"${pair}"; fi
  done
  jq -Rs . <<<"$out"
}

validate_model_key() {
  local label="$1"
  local value="$2"
  if [[ ! "$value" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]] || [[ "$value" == *..* ]]; then
    echo "$label must be a safe model key: $value" >&2
    exit 1
  fi
}

validate_model_ref() {
  local label="$1"
  local doi="$2"
  local filename="$3"
  local sha256="$4"
  if [ -n "$filename" ]; then
    if [[ ! "$filename" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*\.mlmodel$ ]] || [[ "$filename" == *..* ]]; then
      echo "$label file must be an exact .mlmodel basename: $filename" >&2
      exit 1
    fi
  fi
  if [ -n "$doi" ]; then
    if [[ ! "$doi" =~ ^10\.[0-9]{4,9}/[A-Za-z0-9._:/+()-]+$ ]]; then
      echo "$label DOI is invalid: $doi" >&2
      exit 1
    fi
    if [ -z "$filename" ]; then
      echo "$label must declare the exact filename downloaded from its DOI" >&2
      exit 1
    fi
    if [[ ! "$sha256" =~ ^[0-9a-f]{64}$ ]]; then
      echo "$label must declare the lowercase SHA-256 digest of its DOI artifact" >&2
      exit 1
    fi
  elif [ -n "$sha256" ]; then
    echo "$label declares sha256 without a DOI" >&2
    exit 1
  fi
}

kraken_pip_spec="$(yq -r '.kraken.pip_spec // "kraken==7.0.2"' "$config_path")"
if [[ ! "$kraken_pip_spec" =~ ^kraken==[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
  echo "config/ocr.yaml kraken.pip_spec must pin an exact kraken release: $kraken_pip_spec" >&2
  exit 1
fi
default_tx_key="$(yq -r '.kraken.default_transcription_model // ""' "$config_path")"
if [ -z "$default_tx_key" ]; then
  echo "config/ocr.yaml kraken.default_transcription_model must be set" >&2
  exit 1
fi
validate_model_key "default transcription model" "$default_tx_key"
if ! DEFAULT_TX_KEY="$default_tx_key" yq -e '.kraken.transcription_models | has(strenv(DEFAULT_TX_KEY))' "$config_path" >/dev/null; then
  echo "config/ocr.yaml kraken.default_transcription_model must reference a key in kraken.transcription_models" >&2
  exit 1
fi

default_tx_doi="$(yq -r ".kraken.transcription_models[\"${default_tx_key}\"].doi // \"\"" "$config_path")"
default_tx_file="$(yq -r ".kraken.transcription_models[\"${default_tx_key}\"].file // \"\"" "$config_path")"
default_tx_sha256="$(yq -r ".kraken.transcription_models[\"${default_tx_key}\"].sha256 // \"\"" "$config_path")"
validate_model_ref "default transcription model" "$default_tx_doi" "$default_tx_file" "$default_tx_sha256"

# The segmentor service bundles the default segmentation + default transcription
# models so the primary segmentation path still has a recognizer available.
default_seg_key="$(yq -r '.kraken.default_segmentation_model // ""' "$config_path")"
if [ -z "$default_seg_key" ]; then
  default_seg_key="$(yq -r '.kraken.segmentation_models | keys | sort | .[0] // ""' "$config_path")"
fi
validate_model_key "default segmentation model" "$default_seg_key"
if [ -z "$default_seg_key" ]; then
  echo "config/ocr.yaml kraken.segmentation_models must declare at least one model" >&2
  exit 1
fi
if ! DEFAULT_SEG_KEY="$default_seg_key" yq -e '.kraken.segmentation_models | has(strenv(DEFAULT_SEG_KEY))' "$config_path" >/dev/null; then
  echo "config/ocr.yaml kraken.default_segmentation_model must reference a key in kraken.segmentation_models" >&2
  exit 1
fi
default_seg_doi="$(yq -r ".kraken.segmentation_models[\"${default_seg_key}\"].doi // \"\"" "$config_path")"
default_seg_file="$(yq -r ".kraken.segmentation_models[\"${default_seg_key}\"].file // \"\"" "$config_path")"
default_seg_sha256="$(yq -r ".kraken.segmentation_models[\"${default_seg_key}\"].sha256 // \"\"" "$config_path")"
validate_model_ref "default segmentation model" "$default_seg_doi" "$default_seg_file" "$default_seg_sha256"

segmentor_service="scribe-segmentor"
append_entry "$(jq -n \
  --arg key "segmentor" \
  --arg service "$segmentor_service" \
  --arg ghcr_image "$segmentor_service" \
  --arg gar_image "${gar_repo}/${segmentor_service}" \
  --arg image "$(gar_image "$segmentor_service")" \
  --arg context "." \
  --arg dockerfile "Dockerfile.segmentor" \
  --arg file "Dockerfile.segmentor" \
  --arg platform "linux/amd64" \
  --argjson build_args "$(build_args_json \
    "KRAKEN_PIP_SPEC=${kraken_pip_spec}" \
    "KRAKEN_RECOGNITION_MODEL_DOI=${default_tx_doi}" \
    "KRAKEN_RECOGNITION_MODEL_FILE=${default_tx_file}" \
    "KRAKEN_RECOGNITION_MODEL_SHA256=${default_tx_sha256}" \
    "KRAKEN_SEGMENTATION_MODEL_DOI=${default_seg_doi}" \
    "KRAKEN_SEGMENTATION_MODEL_FILE=${default_seg_file}" \
    "KRAKEN_SEGMENTATION_MODEL_SHA256=${default_seg_sha256}")" \
  '{key:$key, service_name:$service, ghcr_image:$ghcr_image, gar_image:$gar_image, image:$image, context:$context, dockerfile:$dockerfile, file:$file, platform:$platform, build_args:$build_args}')"

# Kraken segmentation services: one per entry in ocr.kraken.segmentation_models.
while IFS= read -r seg_key; do
  [ -z "$seg_key" ] && continue
  validate_model_key "segmentation model" "$seg_key"
  seg_doi="$(yq -r ".kraken.segmentation_models[\"${seg_key}\"].doi // \"\"" "$config_path")"
  seg_file="$(yq -r ".kraken.segmentation_models[\"${seg_key}\"].file // \"\"" "$config_path")"
  seg_sha256="$(yq -r ".kraken.segmentation_models[\"${seg_key}\"].sha256 // \"\"" "$config_path")"
  validate_model_ref "segmentation model ${seg_key}" "$seg_doi" "$seg_file" "$seg_sha256"
  service="scribe-ks-$(hash8 "$seg_key")"
  append_entry "$(jq -n \
    --arg key "kraken-seg/${seg_key}" \
    --arg service "$service" \
    --arg ghcr_image "$service" \
    --arg gar_image "${gar_repo}/${service}" \
    --arg image "$(gar_image "$service")" \
    --arg context "." \
    --arg dockerfile "Dockerfile.segmentor" \
    --arg file "Dockerfile.segmentor" \
    --arg platform "linux/amd64" \
    --argjson build_args "$(build_args_json \
      "KRAKEN_PIP_SPEC=${kraken_pip_spec}" \
      "KRAKEN_RECOGNITION_MODEL_DOI=" \
      "KRAKEN_RECOGNITION_MODEL_FILE=" \
      "KRAKEN_RECOGNITION_MODEL_SHA256=" \
      "KRAKEN_SEGMENTATION_MODEL_DOI=${seg_doi}" \
      "KRAKEN_SEGMENTATION_MODEL_FILE=${seg_file}" \
      "KRAKEN_SEGMENTATION_MODEL_SHA256=${seg_sha256}")" \
    '{key:$key, service_name:$service, ghcr_image:$ghcr_image, gar_image:$gar_image, image:$image, context:$context, dockerfile:$dockerfile, file:$file, platform:$platform, build_args:$build_args}')"
done < <(yq -r '.kraken.segmentation_models | keys | sort | .[]' "$config_path")

# Kraken transcription services: one per entry in ocr.kraken.transcription_models.
while IFS= read -r tx_key; do
  [ -z "$tx_key" ] && continue
  validate_model_key "transcription model" "$tx_key"
  tx_doi="$(yq -r ".kraken.transcription_models[\"${tx_key}\"].doi // \"\"" "$config_path")"
  tx_file="$(yq -r ".kraken.transcription_models[\"${tx_key}\"].file // \"\"" "$config_path")"
  tx_sha256="$(yq -r ".kraken.transcription_models[\"${tx_key}\"].sha256 // \"\"" "$config_path")"
  validate_model_ref "transcription model ${tx_key}" "$tx_doi" "$tx_file" "$tx_sha256"
  service="scribe-ko-$(hash8 "$tx_key")"
  append_entry "$(jq -n \
    --arg key "kraken-ocr/${tx_key}" \
    --arg service "$service" \
    --arg ghcr_image "$service" \
    --arg gar_image "${gar_repo}/${service}" \
    --arg image "$(gar_image "$service")" \
    --arg context "." \
    --arg dockerfile "Dockerfile.segmentor" \
    --arg file "Dockerfile.segmentor" \
    --arg platform "linux/amd64" \
    --argjson build_args "$(build_args_json \
      "KRAKEN_PIP_SPEC=${kraken_pip_spec}" \
      "KRAKEN_RECOGNITION_MODEL_DOI=${tx_doi}" \
      "KRAKEN_RECOGNITION_MODEL_FILE=${tx_file}" \
      "KRAKEN_RECOGNITION_MODEL_SHA256=${tx_sha256}" \
      "KRAKEN_SEGMENTATION_MODEL_DOI=${default_seg_doi}" \
      "KRAKEN_SEGMENTATION_MODEL_FILE=${default_seg_file}" \
      "KRAKEN_SEGMENTATION_MODEL_SHA256=${default_seg_sha256}")" \
    '{key:$key, service_name:$service, ghcr_image:$ghcr_image, gar_image:$gar_image, image:$image, context:$context, dockerfile:$dockerfile, file:$file, platform:$platform, build_args:$build_args}')"
done < <(yq -r '.kraken.transcription_models | keys | sort | .[]' "$config_path")

# Ollama services: one per entry in ocr.ollama.models. Prod only — Terraform
# gates the deploy side on workspace == "prod", but building the images in
# every environment is fine since they'd be reused if promoted.
ollama_base="$(yq -r '.ollama.base_image // ""' "$config_path")"
if [[ ! "$ollama_base" =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]]; then
  echo "config/ocr.yaml ollama.base_image must use an immutable sha256 digest: $ollama_base" >&2
  exit 1
fi
while IFS= read -r model; do
  [ -z "$model" ] && continue
  slug="$(slugify "$model")"
  model_digest="$(MODEL="$model" yq -r '.ollama.models[strenv(MODEL)].manifest_digest // ""' "$config_path")"
  if [[ ! "$model_digest" =~ ^[0-9a-f]{64}$ ]]; then
    echo "config/ocr.yaml ollama model ${model} must declare an exact manifest_digest" >&2
    exit 1
  fi
  service="ollama-${slug}"
  if [ "${#service}" -gt 63 ]; then service="$(printf '%s' "$service" | cut -c1-63 | sed 's/-*$//')"; fi
  append_entry "$(jq -n \
    --arg key "ollama/${model}" \
    --arg service "$service" \
    --arg ghcr_image "$service" \
    --arg gar_image "${gar_repo}/${service}" \
    --arg image "$(gar_image "$service")" \
    --arg context "terraform/modules/ollama-cloud-run/image" \
    --arg dockerfile "Dockerfile" \
    --arg file "terraform/modules/ollama-cloud-run/image/Dockerfile" \
    --arg platform "linux/amd64" \
    --argjson build_args "$(build_args_json \
      "OLLAMA_BASE_IMAGE=${ollama_base}" \
      "OLLAMA_MODEL=${model}" \
      "OLLAMA_MODEL_DIGEST=${model_digest}")" \
    '{key:$key, service_name:$service, ghcr_image:$ghcr_image, gar_image:$gar_image, image:$image, context:$context, dockerfile:$dockerfile, file:$file, platform:$platform, build_args:$build_args}')"
done < <(yq -r '.ollama.models | keys | sort | .[]' "$config_path")

jq -c '{include: .}' <<<"$entries_json"
