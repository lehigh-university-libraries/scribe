#!/usr/bin/env bash

set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ci/build-push-gar-image.sh --image us-docker.pkg.dev/<project>/<repo>/<name>:<tag> [options]

Options:
  --context DIR          Docker build context (default: .)
  --file FILE            Dockerfile path (optional)
  --platform PLATFORM    Docker target platform (default: linux/amd64)
  --build-arg KEY=VALUE  Repeatable docker build argument

Notes:
  - Requires Docker auth for us-docker.pkg.dev.
  - Uses docker buildx when available so local Apple Silicon hosts can still
    publish linux/amd64 images for Cloud Run.
EOF
}

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "Missing required command: $1" >&2
    exit 1
  }
}

require_cmd docker

image_ref=""
context="."
dockerfile=""
platform="linux/amd64"
build_args=()

while [ $# -gt 0 ]; do
  case "$1" in
    --image)
      image_ref="${2:?--image requires a value}"
      shift 2
      ;;
    --context)
      context="${2:?--context requires a value}"
      shift 2
      ;;
    --file)
      dockerfile="${2:?--file requires a value}"
      shift 2
      ;;
    --platform)
      platform="${2:?--platform requires a value}"
      shift 2
      ;;
    --build-arg)
      build_args+=("--build-arg" "${2:?--build-arg requires a value}")
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

if [ -z "$image_ref" ]; then
  usage
  exit 1
fi

case "$image_ref" in
  us-docker.pkg.dev/*:*)
    ;;
  *)
    echo "expected a tagged GAR image reference, got: $image_ref" >&2
    exit 1
    ;;
esac

docker_file_args=()
if [ -n "$dockerfile" ]; then
  docker_file_args+=("-f" "$dockerfile")
fi

if docker buildx version >/dev/null 2>&1; then
  DOCKER_BUILDKIT=1 docker buildx build \
    --platform "$platform" \
    "${docker_file_args[@]}" \
    "${build_args[@]}" \
    -t "$image_ref" \
    --push \
    "$context"
  exit 0
fi

case "$platform" in
  linux/amd64)
    host_arch="$(uname -m)"
    case "$host_arch" in
      x86_64|amd64)
        DOCKER_BUILDKIT=1 docker build \
          "${docker_file_args[@]}" \
          "${build_args[@]}" \
          -t "$image_ref" \
          "$context"
        docker push "$image_ref"
        exit 0
        ;;
    esac
    ;;
esac

echo "docker buildx is required to publish ${platform} images from this host." >&2
exit 1
