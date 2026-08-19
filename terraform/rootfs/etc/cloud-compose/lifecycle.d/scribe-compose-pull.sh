#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091
source /home/cloud-compose/profile.sh
retry_until_success docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml \
  pull mariadb triplet api worker traefik
