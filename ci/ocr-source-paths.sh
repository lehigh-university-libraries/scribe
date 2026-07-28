#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

# Keep the explicit non-Go inputs alongside the computed package closure. The
# segmentor image uses the repository root as its build context, so its
# Dockerfile, context filter, module graph, and every file copied into the final
# stage are image inputs. The remaining scripts/configuration determine the OCR
# matrix and the digest map consumed by Terraform.
paths=(
  .dockerignore
  .github/workflows/build-ocr.yaml
  Dockerfile.segmentor
  Makefile
  ci/generate-ocr-images-map.sh
  ci/ocr-matrix.sh
  ci/ocr-source-paths.sh
  ci/segmentor-lock.sh
  config/imagemagick-policy.xml
  config/ocr.yaml
  config/segmentor-requirements.lock
  go.mod
  go.sum
  scripts/install-kraken-models.sh
  terraform/modules/ollama-cloud-run/image
)

if ! package_dirs="$(
  cd "${repo_root}"
  CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
    go list -tags localocr -deps \
      -f '{{if and .Module .Module.Main}}{{.Dir}}{{end}}' \
      ./cmd/segmentor
)"; then
  echo "Unable to resolve the segmentor Go package closure." >&2
  exit 1
fi

while IFS= read -r package_dir; do
  [ -n "${package_dir}" ] || continue
  case "${package_dir}" in
    "${repo_root}"/*)
      relative_dir="${package_dir#"${repo_root}/"}"
      ;;
    *)
      echo "Segmentor dependency resolved outside the main module: ${package_dir}" >&2
      exit 1
      ;;
  esac
  paths+=("${relative_dir}")
done <<<"${package_dirs}"

printf '%s\n' "${paths[@]}" | LC_ALL=C sort -u
