#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf "${TEST_DIR}"' EXIT

mkdir -p "${TEST_DIR}/bin"
docker_log="${TEST_DIR}/docker.log"
container_id="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

cat >"${TEST_DIR}/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s' "${1:-}" >>"${RESET_TEST_DOCKER_LOG}"
for argument in "${@:2}"; do
  printf '\t%s' "${argument}" >>"${RESET_TEST_DOCKER_LOG}"
done
printf '\n' >>"${RESET_TEST_DOCKER_LOG}"

if [ "${1:-}" = "compose" ]; then
  case " $* " in
    *" config --format json "*)
      cat <<JSON
{
  "name": "scribe-dev",
  "services": {
    "api": {"depends_on": {"mariadb": {"condition": "service_healthy"}}},
    "frontend": {"depends_on": {"api": {"condition": "service_healthy"}}},
    "mariadb": {
      "volumes": [
        {"type": "volume", "source": "mariadb-data-g17", "target": "/var/lib/mysql"}
      ]
    },
    "triplet": {},
    "worker": {"depends_on": {"api": {"condition": "service_healthy"}}}
  },
  "volumes": {
    "mariadb-data-g17": {"name": "scribe-dev_mariadb-data-g17"},
    "uploads-data": {"name": "scribe-dev_uploads-data"}
  }
}
JSON
      exit 0
      ;;
    *" ps --all --quiet mariadb "*)
      printf '%s\n' "${RESET_TEST_CONTAINER_ID}"
      exit 0
      ;;
    *" stop api frontend worker mariadb "*)
      exit 0
      ;;
  esac
fi

if [ "${1:-}" = "volume" ] && [ "${2:-}" = "inspect" ] && [ "${3:-}" = "--" ] && [ "${4:-}" = "scribe-dev_mariadb-data-g17" ]; then
  if [ "${RESET_TEST_MODE:-valid}" = "wrong-volume-label" ]; then
    volume_label="uploads-data"
  else
    volume_label="mariadb-data-g17"
  fi
  printf '[{"Name":"scribe-dev_mariadb-data-g17","Labels":{"com.docker.compose.project":"scribe-dev","com.docker.compose.volume":"%s"}}]\n' "${volume_label}"
  exit 0
fi

if [ "${1:-}" = "container" ] && [ "${2:-}" = "inspect" ] && [ "${3:-}" = "--" ] && [ "${4:-}" = "${RESET_TEST_CONTAINER_ID}" ]; then
  cat <<JSON
[
  {
    "Id": "${RESET_TEST_CONTAINER_ID}",
    "Config": {
      "Labels": {
        "com.docker.compose.project": "scribe-dev",
        "com.docker.compose.project.working_dir": "${RESET_TEST_ROOT}",
        "com.docker.compose.service": "mariadb"
      }
    },
    "Mounts": [
      {"Type": "volume", "Name": "scribe-dev_mariadb-data-g17", "Destination": "/var/lib/mysql"}
    ]
  }
]
JSON
  exit 0
fi

if [ "${1:-}" = "container" ] && [ "${2:-}" = "rm" ] && [ "${3:-}" = "--" ] && [ "${4:-}" = "${RESET_TEST_CONTAINER_ID}" ]; then
  exit 0
fi

if [ "${1:-}" = "volume" ] && [ "${2:-}" = "rm" ] && [ "${3:-}" = "--" ] && [ "${4:-}" = "scribe-dev_mariadb-data-g17" ]; then
  exit 0
fi

echo "unexpected fake Docker invocation: $*" >&2
exit 97
EOF
chmod +x "${TEST_DIR}/bin/docker"

for command in bash cat dirname env jq; do
  ln -s "$(command -v "${command}")" "${TEST_DIR}/bin/${command}"
done

run_reset() {
  local mode="$1"
  local stdout_file="$2"
  local stderr_file="$3"
  shift 3
  PATH="${TEST_DIR}/bin" \
    RESET_TEST_CONTAINER_ID="${container_id}" \
    RESET_TEST_DOCKER_LOG="${docker_log}" \
    RESET_TEST_MODE="${mode}" \
    RESET_TEST_ROOT="${ROOT_DIR}" \
    "$@" \
    bash "${ROOT_DIR}/ci/reset-dev-db.sh" >"${stdout_file}" 2>"${stderr_file}"
}

: >"${docker_log}"
if run_reset valid "${TEST_DIR}/unconfirmed.out" "${TEST_DIR}/unconfirmed.err" env; then
  echo "reset unexpectedly ran without explicit confirmation" >&2
  exit 1
fi
grep -F 'SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data' "${TEST_DIR}/unconfirmed.err" >/dev/null
if [ -s "${docker_log}" ]; then
  echo "reset contacted Docker before confirmation" >&2
  exit 1
fi

: >"${docker_log}"
if run_reset valid "${TEST_DIR}/ci.out" "${TEST_DIR}/ci.err" env \
  SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data CI=true; then
  echo "reset unexpectedly ran in CI" >&2
  exit 1
fi
grep -F 'interactive local-development stack, not CI' "${TEST_DIR}/ci.err" >/dev/null
if [ -s "${docker_log}" ]; then
  echo "CI refusal contacted Docker" >&2
  exit 1
fi

: >"${docker_log}"
run_reset valid "${TEST_DIR}/valid.out" "${TEST_DIR}/valid.err" env -u CI -u GITHUB_ACTIONS \
  SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data

grep -F $'compose\t--project-directory\t' "${docker_log}" >/dev/null
grep -F $'\tstop\tapi\tfrontend\tworker\tmariadb' "${docker_log}" >/dev/null
grep -Fx $'container\trm\t--\t'"${container_id}" "${docker_log}" >/dev/null
grep -Fx $'volume\trm\t--\tscribe-dev_mariadb-data-g17' "${docker_log}" >/dev/null
if [ "$(grep -Fc $'volume\trm\t--\t' "${docker_log}")" -ne 1 ]; then
  echo "reset did not issue exactly one volume removal" >&2
  exit 1
fi
if grep -E $'(^|\t)(down|--volumes|-v|system|prune)(\t|$)' "${docker_log}" >/dev/null; then
  echo "reset used a broad Docker cleanup operation" >&2
  exit 1
fi
if grep -E $'^volume\trm\t.*(uploads|cache|triplet)' "${docker_log}" >/dev/null; then
  echo "reset targeted a non-MariaDB volume" >&2
  exit 1
fi
grep -F 'Uploads, cache, and Triplet volumes were left intact.' "${TEST_DIR}/valid.out" >/dev/null

: >"${docker_log}"
if run_reset wrong-volume-label "${TEST_DIR}/wrong-label.out" "${TEST_DIR}/wrong-label.err" env -u CI -u GITHUB_ACTIONS \
  SCRIBE_CONFIRM_RESET_DEV_DB=delete-local-mariadb-data; then
  echo "reset unexpectedly accepted a mismatched Compose volume label" >&2
  exit 1
fi
grep -F 'is not the labeled mariadb-data-g17 volume' "${TEST_DIR}/wrong-label.err" >/dev/null
if grep -E $'^(container|volume)\trm\t' "${docker_log}" >/dev/null || grep -F $'\tstop\t' "${docker_log}" >/dev/null; then
  echo "reset mutated Docker state after a volume-label mismatch" >&2
  exit 1
fi

echo "Local MariaDB reset safety contracts passed."
