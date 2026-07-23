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
force_docker="${SCRIBE_DOCS_FORCE_DOCKER:-false}"
if [ "$force_docker" != "true" ] && [ "$force_docker" != "false" ]; then
  echo "Error: SCRIBE_DOCS_FORCE_DOCKER must be true or false." >&2
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

prepare_site_output() {
  local site_dir="${ROOT_DIR}/site"
  if [ -L "${site_dir}" ] || { [ -e "${site_dir}" ] && [ ! -d "${site_dir}" ]; }; then
    echo "Error: documentation output must be a real directory: ${site_dir}" >&2
    return 1
  fi
  mkdir -p "${site_dir}"
  find "${site_dir}" -mindepth 1 -depth -delete
}

if [ "$force_docker" = "false" ] && command -v zensical >/dev/null 2>&1; then
  zensical_bin="$(command -v zensical)"
elif [ "$force_docker" = "false" ] && [ -x "${ROOT_DIR}/.tools/docs/bin/zensical" ]; then
  zensical_bin="${ROOT_DIR}/.tools/docs/bin/zensical"
elif command -v docker >/dev/null 2>&1; then
  docker_image="${SCRIBE_DOCS_IMAGE:-scribe-docs:local}"
  if ! docker_image_ref="$(docker image inspect --format '{{.Id}}' "${docker_image}" 2>/dev/null)"; then
    echo "Error: ${docker_image} is not built. Run 'make install-doc-tools'." >&2
    exit 127
  fi
  # Resolve the mutable local tag once. This keeps every operation on the same
  # image even if another build retags scribe-docs:local concurrently.
  readonly docker_image_ref
  docker_zensical_version=""
  for attempt in 1 2 3 4 5; do
    if docker_zensical_version="$(docker run --rm --entrypoint zensical "${docker_image_ref}" --version 2>&1)"; then
      break
    fi
    if [ "$attempt" -lt 5 ]; then
      # Some containerd-backed Docker engines report the freshly loaded image
      # before its runnable platform manifest is visible.
      sleep 1
    fi
  done
  assert_zensical_version_output "$docker_zensical_version"
  if [ "${mode}" = "build" ]; then
    prepare_site_output
  fi
  docker_args=("${mode}")
  if [ "${mode}" = "build" ]; then
    docker_args+=("--clean" "--strict")
  else
    docker_args+=("--dev-addr=0.0.0.0:8000")
  fi
  if docker run --rm \
    --mount "type=bind,src=${ROOT_DIR},dst=/workspace,readonly" \
    -w /workspace \
    --entrypoint sh \
    "${docker_image_ref}" \
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
      "${docker_image_ref}" \
      "${docker_args[@]}"
  fi

  if [ "${mode}" = "serve" ]; then
    echo "Error: docs-serve requires a Docker daemon that can bind-mount ${ROOT_DIR}." >&2
    exit 1
  fi

  container_id="$(docker create -e HOME=/tmp -w /workspace "${docker_image_ref}" "${docker_args[@]}")"
  trap 'docker rm -f "${container_id}" >/dev/null 2>&1 || true' EXIT
  tar -C "${ROOT_DIR}" -cf - zensical.toml docs | docker cp - "${container_id}:/workspace"
  docker start -a "${container_id}"
  if [ "${SCRIBE_DOCS_EXPORT:-true}" = "true" ]; then
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
  prepare_site_output
  exec "${zensical_bin}" build --clean --strict
fi
exec "${zensical_bin}" serve
