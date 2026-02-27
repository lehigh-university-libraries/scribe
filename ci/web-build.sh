#!/usr/bin/env bash

set -euo pipefail

IMAGE="${IMAGE:-ghcr.io/lehigh-university-libraries/hocredit:main}"

docker build --target web-build -t "${IMAGE}-web" .
