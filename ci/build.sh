#!/usr/bin/env bash
set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/lehigh-university-libraries/scribe:main}"
PLATFORM="${DOCKER_DEFAULT_PLATFORM:-linux/amd64}"

DOCKER_BUILDKIT=1 docker build --platform "$PLATFORM" -t "$IMAGE" .
