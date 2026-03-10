#!/usr/bin/env bash

set -euo pipefail

SEQUEL_ACE_PATH="${SEQUEL_ACE_PATH:-/Applications/Sequel Ace.app}"
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Error: Sequel Ace is only available on macOS." >&2
  exit 1
fi

if [[ ! -d "$SEQUEL_ACE_PATH" ]]; then
  echo "Error: Sequel Ace is not installed at $SEQUEL_ACE_PATH" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Error: docker is required." >&2
  exit 1
fi

if ! docker compose version >/dev/null 2>&1; then
  echo "Error: docker compose is required." >&2
  exit 1
fi

if port_output="$(cd "$PROJECT_ROOT" && docker compose port mariadb 3306 2>/dev/null)"; then
  :
else
  port_output=""
fi
if [[ -z "$port_output" ]]; then
  echo "Error: MariaDB is not published to the host." >&2
  echo "" >&2
  echo "Add a port mapping for the mariadb service, for example:" >&2
  echo "  ports:" >&2
  echo "    - \"3306:3306\"" >&2
  echo "" >&2
  echo "Then restart the stack with \`make up\`." >&2
  exit 1
fi

host_port="${port_output##*:}"
if [[ "$host_port" != "3306" ]]; then
  echo "Error: MariaDB is published on host port ${host_port}, not 3306." >&2
  echo "" >&2
  echo "This target expects an explicit \`3306:3306\` mapping for the mariadb service." >&2
  exit 1
fi

db_host="127.0.0.1"
db_name="${MARIADB_DATABASE:-scribe}"
db_user="${MARIADB_USER:-scribe}"
db_password="${MARIADB_PASSWORD:-scribe}"

open "mysql://${db_user}:${db_password}@${db_host}:${host_port}/${db_name}" -a "$SEQUEL_ACE_PATH"
