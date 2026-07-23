#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly ROOT_DIR

exec bash \
  "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-compose-runtime-preflight.sh" \
  "$ROOT_DIR" \
  "$ROOT_DIR/docker-compose.yaml" \
  "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-runtime.compose.yaml"
