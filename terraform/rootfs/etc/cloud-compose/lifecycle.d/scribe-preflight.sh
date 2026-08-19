#!/usr/bin/env bash
set -euo pipefail

export SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker
exec bash /home/cloud-compose/scribe-compose-runtime-preflight.sh \
  "$PWD" "$PWD/docker-compose.yaml" /home/cloud-compose/scribe-runtime.compose.yaml
