#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d -- "${TMPDIR:-/tmp}/scribe-run-ci-network.XXXXXXXXXX")"
cleanup() {
  rm -rf -- "$TEST_DIR"
}
trap cleanup EXIT

mkdir -p -- "$TEST_DIR/bin"

cat >"$TEST_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\t%s\n' "${COMPOSE_PROJECT_NAME:-}" "$*" >>"$FAKE_DOCKER_LOG"
if [ "$1" = "compose" ] && [ "$2" = "version" ] && [ "$3" = "--short" ]; then
  echo "5.3.1"
  exit 0
fi
if [ "$1" = "network" ] && [ "$2" = "create" ]; then
  case " $* " in
    *" --subnet 172.31.0.0/24 "*) exit 1 ;;
    *" --subnet 172.31.1.0/24 "*) exit 0 ;;
    *) echo "unexpected network candidate: $*" >&2; exit 2 ;;
  esac
fi
if [ "$1" = "compose" ] && [ "$2" = "down" ]; then
  exit 0
fi
echo "unexpected fake Docker invocation: $*" >&2
exit 2
EOF
chmod 0755 -- "$TEST_DIR/bin/docker"

cat >"$TEST_DIR/bin/make" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

target="${!#}"
printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
  "$target" \
  "${COMPOSE_PROJECT_NAME:-}" \
  "${COMPOSE_FILE:-}" \
  "${SCRIBE_COMPOSE_SUBNET:-}" \
  "${SCRIBE_COMPOSE_GATEWAY:-}" \
  "${SCRIBE_COMPOSE_IP_RANGE:-}" \
  "${SCRIBE_TRAEFIK_IP:-}" \
  "${SERVER_TRUSTED_PROXY_CIDRS:-}" >>"$FAKE_MAKE_LOG"
EOF
chmod 0755 -- "$TEST_DIR/bin/make"

export FAKE_DOCKER_LOG="$TEST_DIR/docker.log"
export FAKE_MAKE_LOG="$TEST_DIR/make.log"
export PATH="$TEST_DIR/bin:$PATH"

SCRIBE_CI_COMPOSE_PROJECT=scribe-ci-network-contract \
SCRIBE_MAKE_COMMAND="$TEST_DIR/bin/make" \
  bash "$ROOT_DIR/ci/run-ci.sh" >/dev/null

grep -F $'scribe-ci-network-contract\tnetwork create --driver bridge --subnet 172.31.0.0/24' \
  "$FAKE_DOCKER_LOG" >/dev/null
grep -F $'scribe-ci-network-contract\tnetwork create --driver bridge --subnet 172.31.1.0/24 --ip-range 172.31.1.128/25 --gateway 172.31.1.1' \
  "$FAKE_DOCKER_LOG" >/dev/null

up_db_row="$(awk -F '\t' '$1 == "up-db" { print; exit }' "$FAKE_MAKE_LOG")"
expected_up_db="$(printf 'up-db\tscribe-ci-network-contract\t%s/docker-compose.yaml\t172.31.1.0/24\t172.31.1.1\t172.31.1.128/25\t172.31.1.2\t172.31.1.2/32' "$ROOT_DIR")"
[ "$up_db_row" = "$expected_up_db" ] || {
  echo "run-ci did not pass one isolated network tuple to Compose: $up_db_row" >&2
  exit 1
}

down_count="$(awk -F '\t' '$1 == "scribe-ci-network-contract" && $2 ~ /^compose down / { count++ } END { print count + 0 }' "$FAKE_DOCKER_LOG")"
[ "$down_count" -eq 1 ] || {
  echo "run-ci cleanup targeted $down_count Compose projects, want exactly one" >&2
  exit 1
}
if awk -F '\t' '$1 != "" && $1 != "scribe-ci-network-contract" { found = 1 } END { exit !found }' "$FAKE_DOCKER_LOG"; then
  echo "run-ci targeted a Compose project outside its isolated CI project" >&2
  exit 1
fi

echo "run-ci reserves and cleans an isolated non-overlapping Compose network."
