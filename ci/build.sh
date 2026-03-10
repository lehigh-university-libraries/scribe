#!/usr/bin/env bash
set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/lehigh-university-libraries/scribe:main}"

DOCKER_BUILDKIT=1 docker build -t "$IMAGE" .
