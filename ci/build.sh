#!/usr/bin/env bash
set -euo pipefail

IMAGE="${IMAGE:-scribe-api:local}"
PLATFORM="${DOCKER_DEFAULT_PLATFORM:-linux/amd64}"

DOCKER_BUILDKIT=1 docker build --platform "$PLATFORM" -t "$IMAGE" .
