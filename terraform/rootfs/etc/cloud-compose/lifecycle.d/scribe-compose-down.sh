#!/usr/bin/env bash
set -euo pipefail

exec docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml down
