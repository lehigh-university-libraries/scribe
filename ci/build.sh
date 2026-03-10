#!/usr/bin/env bash
set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/lehigh-university-libraries/scribe:main}"

docker build -t "$IMAGE" .
