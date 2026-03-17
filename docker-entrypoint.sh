#!/bin/sh

set -eu

if [ -z "${DATABASE_DSN:-}" ] && [ -f /run/secrets/mariadb_password ]; then
  password="$(tr -d '\n' < /run/secrets/mariadb_password)"
  export DATABASE_DSN="${MARIADB_USER:-scribe}:${password}@tcp(mariadb:3306)/${MARIADB_DATABASE:-scribe}?parseTime=true"
fi

exec /app/scribe "$@"
