#!/usr/bin/env bash

set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/lehigh-university-libraries/hocredit:main}"

docker run --rm \
  --entrypoint /bin/bash \
  "$IMAGE" \
  -c "go test -v -race ./..."
