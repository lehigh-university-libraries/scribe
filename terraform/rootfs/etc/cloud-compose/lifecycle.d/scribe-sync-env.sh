#!/usr/bin/env bash
set -euo pipefail

# shellcheck disable=SC1091
source /home/cloud-compose/profile.sh

sync_compose_application_env
update_compose_env COMPOSE_PROJECT_NAME "$COMPOSE_PROJECT_NAME"
update_compose_env SITE_NAME "${CLOUD_COMPOSE_INSTANCE_NAME:-${GCP_INSTANCE_NAME:-$APP_NAME}}"
update_compose_env COMPOSE_BIND_PORT "$COMPOSE_BIND_PORT"
