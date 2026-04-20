#!/usr/bin/env bash

set -euo pipefail

FRONTEND_IMAGE="${FRONTEND_IMAGE:-scribe-frontend:local}"
BACKEND_ORIGIN="${SCRIBE_FRONTEND_BACKEND_ORIGIN:-}"

DOCKER_BUILDKIT=1 docker build \
  -f Dockerfile.frontend \
  --build-arg "SCRIBE_FRONTEND_BACKEND_ORIGIN=${BACKEND_ORIGIN}" \
  -t "$FRONTEND_IMAGE" \
  .
