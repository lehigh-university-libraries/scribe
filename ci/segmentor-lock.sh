#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# Keep lock generation on the same interpreter image as Dockerfile.segmentor.
# renovate: datasource=docker depName=python
readonly python_image="python:3.14.7-slim-bookworm@sha256:9ab8d9c8514b44f90cf0029dd42fdd7e9e211e639c8b995304cc04568dee900f"

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
