#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly EXPECTED_ZENSICAL_VERSION="0.0.51"
cd "${ROOT_DIR}"
mode="${1:-build}"
if [ "${mode}" != "build" ] && [ "${mode}" != "serve" ]; then
  echo "Usage: $0 [build|serve]" >&2
  exit 2
fi

assert_zensical_version_output() {
  local output="$1"
  case "$output" in
    "$EXPECTED_ZENSICAL_VERSION"|"zensical $EXPECTED_ZENSICAL_VERSION"|"zensical, version $EXPECTED_ZENSICAL_VERSION") return 0 ;;
  esac
  echo "Error: Zensical ${EXPECTED_ZENSICAL_VERSION} is required; found '${output:-unknown}'. Run 'make install-doc-tools'." >&2
  return 127
}

if command -v zensical >/dev/null 2>&1; then
  zensical_bin="$(command -v zensical)"
elif [ -x "${ROOT_DIR}/.tools/docs/bin/zensical" ]; then
  zensical_bin="${ROOT_DIR}/.tools/docs/bin/zensical"
elif command -v docker >/dev/null 2>&1; then
  docker_image="${SCRIBE_DOCS_IMAGE:-scribe-docs:local}"
  if ! docker image inspect "${docker_image}" >/dev/null 2>&1; then
    echo "Error: ${docker_image} is not built. Run 'make install-doc-tools'." >&2
    exit 127
  fi
  docker_zensical_version="$(docker run --rm --entrypoint zensical "${docker_image}" --version 2>&1 || true)"
  assert_zensical_version_output "$docker_zensical_version"
  docker_args=("${mode}")
  if [ "${mode}" = "build" ]; then
    docker_args+=("--clean")
  else
    docker_args+=("--dev-addr=0.0.0.0:8000")
  fi
  if docker run --rm \
    --mount "type=bind,src=${ROOT_DIR},dst=/workspace,readonly" \
    -w /workspace \
    --entrypoint sh \
    "${docker_image}" \
    -c 'test -f zensical.toml' >/dev/null 2>&1; then
    docker_run_args=(
      --rm
      --user "$(id -u):$(id -g)"
      -e HOME=/tmp
      --mount "type=bind,src=${ROOT_DIR},dst=/workspace"
      -w /workspace
    )
    if [ "${mode}" = "serve" ]; then
      docker_run_args+=(-p 8000:8000)
    fi
    exec docker run \
      "${docker_run_args[@]}" \
      "${docker_image}" \
      "${docker_args[@]}"
  fi

  if [ "${mode}" = "serve" ]; then
    echo "Error: docs-serve requires a Docker daemon that can bind-mount ${ROOT_DIR}." >&2
    exit 1
  fi

  container_id="$(docker create -e HOME=/tmp -w /workspace "${docker_image}" "${docker_args[@]}")"
  trap 'docker rm -f "${container_id}" >/dev/null 2>&1 || true' EXIT
  tar -C "${ROOT_DIR}" -cf - zensical.toml docs | docker cp - "${container_id}:/workspace"
  docker start -a "${container_id}"
  if [ "${SCRIBE_DOCS_EXPORT:-true}" = "true" ]; then
    mkdir -p "${ROOT_DIR}/site"
    docker cp "${container_id}:/workspace/site/." "${ROOT_DIR}/site"
  fi
  exit 0
else
  echo "Error: Zensical is required. Run 'make install-doc-tools'." >&2
  exit 127
fi

zensical_version="$("${zensical_bin}" --version 2>&1 || true)"
assert_zensical_version_output "$zensical_version"

if [ "${mode}" = "build" ]; then
  exec "${zensical_bin}" build --clean
fi
exec "${zensical_bin}" serve
