#!/usr/bin/env bash

set -euo pipefail

FRONTEND_IMAGE="${FRONTEND_IMAGE:-scribe-frontend:local}"
BACKEND_ORIGIN="${SCRIBE_FRONTEND_BACKEND_ORIGIN:-}"
IIIF_ORIGIN="${SCRIBE_FRONTEND_IIIF_ORIGIN:-}"
PLATFORM="${DOCKER_DEFAULT_PLATFORM:-linux/amd64}"

DOCKER_BUILDKIT=1 docker build \
  --platform "$PLATFORM" \
  -f Dockerfile.frontend \
  --build-arg "SCRIBE_FRONTEND_BACKEND_ORIGIN=${BACKEND_ORIGIN}" \
  --build-arg "SCRIBE_FRONTEND_IIIF_ORIGIN=${IIIF_ORIGIN}" \
  -t "$FRONTEND_IMAGE" \
  .
