#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Keep lock generation on the same interpreter image as Dockerfile.segmentor.
# renovate: datasource=docker depName=python
readonly python_image="python:3.13.14-slim-bookworm@sha256:9d7f287598e1a5a978c015ee176d8216435aaf335ed69ac3c38dd1bbb10e8d64"

command -v docker >/dev/null 2>&1 || {
  echo "Docker is required to regenerate config/segmentor-requirements.lock." >&2
  exit 127
}

docker run --rm \
  --user "$(id -u):$(id -g)" \
  --env HOME=/tmp \
  --volume "$repo_root:/src" \
  --workdir /src \
  "$python_image" \
  sh -ec "python -m pip install --disable-pip-version-check --no-cache-dir --require-hashes --only-binary=:all: --requirement config/pip-tools-requirements.lock >/dev/null && python -m piptools compile --allow-unsafe --generate-hashes --resolver=backtracking --strip-extras --output-file=config/segmentor-requirements.lock config/segmentor-requirements.in"
