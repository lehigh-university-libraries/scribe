#!/usr/bin/env bash
# Delete the local Compose MariaDB data volume so the next `make up` starts with
# an empty database. Nothing else in the stack is touched.

set -euo pipefail
cd "$(dirname "$0")/.."

if [ "${SCRIBE_CONFIRM_RESET_DEV_DB:-}" != "delete-local-mariadb-data" ]; then
  read -r -p "Delete the local MariaDB data volume? Type yes: " answer
  [ "$answer" = "yes" ] || { echo "Nothing was changed." >&2; exit 1; }
fi

volume="$(docker compose config --format json |
  jq -er '.volumes[.services.mariadb.volumes[] | select(.target == "/var/lib/mysql") | .source].name')"
docker compose rm --stop --force mariadb
docker volume rm "$volume"
echo "Deleted ${volume}. Run make up to start with an empty database."
