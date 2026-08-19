#!/usr/bin/env bash
set -euo pipefail

exec docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml \
  up --no-build --wait --wait-timeout 180 mariadb triplet api worker traefik
