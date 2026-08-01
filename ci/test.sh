#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

FAST_MODE=false
case "${1:-}" in
  "") ;;
  --fast) FAST_MODE=true ;;
  *) echo "usage: $0 [--fast]" >&2; exit 2 ;;
esac
if [ "$#" -gt 1 ]; then
  echo "usage: $0 [--fast]" >&2
  exit 2
fi

REQUIRE_TEST_DB="${SCRIBE_REQUIRE_TEST_DB:-false}"
TEST_MODE="${SCRIBE_BACKEND_TEST_MODE:-auto}"
case "$REQUIRE_TEST_DB" in
  true|false) ;;
  *)
    echo "SCRIBE_REQUIRE_TEST_DB must be true or false." >&2
    exit 2
    ;;
esac
case "$TEST_MODE" in
  auto|host|container) ;;
  *)
    echo "SCRIBE_BACKEND_TEST_MODE must be auto, host, or container." >&2
    exit 2
    ;;
esac
if [ "$FAST_MODE" = "true" ] && [ "$REQUIRE_TEST_DB" = "true" ]; then
  echo "--fast cannot satisfy SCRIBE_REQUIRE_TEST_DB=true; run the full backend gate." >&2
  exit 2
fi

# If MariaDB is running via Docker Compose, the full suite joins its network
# and receives TEST_DSN. Fast mode is intentionally unit-only and ignores an
# ambient database so cached package results remain safe and deterministic.
NETWORK_ARGS=()
DSN_ARGS=()
DB_USER="${MARIADB_USER:-scribe}"
DB_NAME="${MARIADB_DATABASE:-scribe}"
if [ "$FAST_MODE" = "false" ] && command -v docker >/dev/null 2>&1; then
  MARIADB_ID="$({
    docker compose -f "${ROOT_DIR}/docker-compose.yaml" ps -q mariadb 2>/dev/null || true
  } | head -1)"
  if [ -n "$MARIADB_ID" ]; then
    NETWORK="$({ docker inspect "$MARIADB_ID" \
      --format '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' \
      2>/dev/null || true; } | awk '{print $1}')"
    if [ -n "$NETWORK" ]; then
      echo "MariaDB detected — running integration tests on network: $NETWORK"
      NETWORK_ARGS=(--network "$NETWORK")
      if [ -f "./secrets/mariadb_password" ]; then
        DB_PASSWORD="$(tr -d '\n' < ./secrets/mariadb_password)"
      else
        DB_PASSWORD="scribe"
      fi
      DSN_ARGS=(-e "TEST_DSN=${DB_USER}:${DB_PASSWORD}@tcp(mariadb:3306)/${DB_NAME}?parseTime=true")
    fi
  fi
fi

if [ "$REQUIRE_TEST_DB" = "true" ] && [ "${#DSN_ARGS[@]}" -eq 0 ]; then
  echo "Required MariaDB test service is unavailable or has no Compose network." >&2
  exit 1
fi
if [ "$TEST_MODE" = "host" ] && [ "${#DSN_ARGS[@]}" -ne 0 ]; then
  echo "Host mode cannot reach the unexposed Compose database; use container mode." >&2
  exit 2
fi

HOST_GO=""
HOST_UNAVAILABLE_REASON=""
host_go_is_usable() {
  local expected_go actual_go cgo_enabled cc
  local goos="" gohostos="" goarch="" gohostarch=""

  if command -v go >/dev/null 2>&1; then
    HOST_GO="$(command -v go)"
  elif [ -x /usr/local/go/bin/go ]; then
    HOST_GO=/usr/local/go/bin/go
  else
    HOST_UNAVAILABLE_REASON="the pinned Go compiler is not installed"
    return 1
  fi

  expected_go="$(tr -d '[:space:]' < .go-version)"
  actual_go="$(GOTOOLCHAIN=local "$HOST_GO" env GOVERSION 2>/dev/null || true)"
  actual_go="${actual_go#go}"
  if [ "$actual_go" != "$expected_go" ]; then
    HOST_UNAVAILABLE_REASON="installed Go ${actual_go:-unknown} does not match ${expected_go}"
    return 1
  fi
  cgo_enabled="$(GOTOOLCHAIN=local "$HOST_GO" env CGO_ENABLED 2>/dev/null || true)"
  cc="$(GOTOOLCHAIN=local "$HOST_GO" env CC 2>/dev/null || true)"
  if [ "$cgo_enabled" != "1" ] || [ -z "$cc" ] || ! command -v "$cc" >/dev/null 2>&1; then
    HOST_UNAVAILABLE_REASON="the Go race detector requires an available host C compiler"
    return 1
  fi
  read -r goos gohostos goarch gohostarch < <(
    GOTOOLCHAIN=local "$HOST_GO" env GOOS GOHOSTOS GOARCH GOHOSTARCH 2>/dev/null |
      xargs
  )
  if [ -z "$goos" ] || [ "$goos" != "$gohostos" ] || [ "$goarch" != "$gohostarch" ]; then
    HOST_UNAVAILABLE_REASON="the Go race detector requires a native host target"
    return 1
  fi
  if [ "$FAST_MODE" = "false" ]; then
    if ! command -v xmllint >/dev/null 2>&1 || ! command -v sha256sum >/dev/null 2>&1; then
      HOST_UNAVAILABLE_REASON="xmllint and sha256sum are required by the full backend gate"
      return 1
    fi
  fi
}

run_host_tests() {
  echo "Using pinned host Go; set SCRIBE_BACKEND_TEST_MODE=container to force isolation."
  if [ "$FAST_MODE" = "false" ]; then
    ./ci/export-schema-check.sh
  fi
  (
    unset TEST_DSN SCRIBE_RESTORE_SMOKE SCRIBE_RESTORE_PHASE
    if [ "$FAST_MODE" = "true" ]; then
      # Package-level Go caching reruns only packages affected by source or
      # dependency changes. DB-backed tests skip because TEST_DSN is absent.
      GOFLAGS='' GOTOOLCHAIN=local "$HOST_GO" test -race ./...
    else
      GOFLAGS='' GOTOOLCHAIN=local "$HOST_GO" test -p=1 -count=1 -v -race ./...
    fi
  )
}

if [ "$TEST_MODE" != "container" ] && [ "${#DSN_ARGS[@]}" -eq 0 ]; then
  if host_go_is_usable; then
    run_host_tests
    exit 0
  fi
  if [ "$TEST_MODE" = "host" ]; then
    echo "Host backend tests are unavailable: $HOST_UNAVAILABLE_REASON." >&2
    exit 127
  fi
  echo "Host backend tests are unavailable ($HOST_UNAVAILABLE_REASON); using the prepared container." >&2
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "Docker is required for containerized backend tests." >&2
  exit 127
fi

# The test-runner stage pins build-base and xmllint in the Docker build cache;
# repeated invocations do not run apk or resolve package indexes again.
TEST_RUNNER_FINGERPRINT="$(git hash-object Dockerfile)"
GO_TEST_IMAGE="scribe-go-test:local-${TEST_RUNNER_FINGERPRINT}"
if ! docker image inspect "$GO_TEST_IMAGE" >/dev/null 2>&1; then
  echo "Preparing cached backend test image..."
  docker build --quiet \
    --build-arg "SCRIBE_TEST_RUNNER_FINGERPRINT=${TEST_RUNNER_FINGERPRINT}" \
    --target test-runner \
    --tag "$GO_TEST_IMAGE" \
    "$ROOT_DIR" >/dev/null
else
  echo "Reusing cached backend test image."
fi
GO_TEST_IMAGE_ID="$(docker image inspect --format '{{.Id}}' "$GO_TEST_IMAGE")"

if [ "$FAST_MODE" = "true" ]; then
  schema_command=""
  go_test_command="go test -race ./..."
else
  schema_command="./ci/export-schema-check.sh"
  # DB-backed packages share the one isolated schema created by make ci.
  # Serialize packages so schema-global quota/outbox assertions cannot consume
  # one another's rows, and bypass cached results for the new database.
  go_test_command="go test -p=1 -count=1 -v -race ./..."
fi
run_tests="
    set -eu
    export PATH=\"/usr/local/go/bin:\$PATH\"
    ${schema_command}
    ${go_test_command}
  "

container_id="$(
  docker create \
    "${NETWORK_ARGS[@]}" \
    "${DSN_ARGS[@]}" \
    --mount "type=volume,src=scribe-go-test-build-cache-v1,dst=/root/.cache/go-build" \
    --mount "type=volume,src=scribe-go-test-mod-cache-v1,dst=/go/pkg/mod" \
    -w /app \
    "$GO_TEST_IMAGE_ID" \
    sh -lc "$run_tests"
)"
cleanup() {
  docker rm -f "$container_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

tar \
  --exclude=.env \
  --exclude=.git \
  --exclude=.tools \
  --exclude='gha-creds-*.json' \
  --exclude=secrets \
  --exclude=terraform/.terraform \
  --exclude=site \
  --exclude='web/node_modules*' \
  --exclude=web/dist \
  --exclude='mirador-scribe/node_modules*' \
  --exclude=mirador-scribe/dist \
  -C "${ROOT_DIR}" -cf - . | docker cp - "$container_id:/app"
docker start -a "$container_id"
