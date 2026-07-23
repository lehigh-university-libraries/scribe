#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d "${TMPDIR:-/tmp}/scribe-compose-runtime-preflight-test.XXXXXX")"
trap 'rm -rf -- "$TEST_DIR"' EXIT

fail() {
  echo "Compose runtime preflight test failed: $*" >&2
  exit 1
}

readonly CONTAINER_MARIADB=0000000000000000000000000000000000000000000000000000000000000000
readonly CONTAINER_TRIPLET=1111111111111111111111111111111111111111111111111111111111111111
readonly CONTAINER_API=2222222222222222222222222222222222222222222222222222222222222222
readonly CONTAINER_WORKER=3333333333333333333333333333333333333333333333333333333333333333
readonly CONTAINER_TRAEFIK=4444444444444444444444444444444444444444444444444444444444444444
readonly CONTAINER_FOREIGN=ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff
readonly NETWORK_DEFAULT=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
readonly NETWORK_OLD=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb

mkdir -p "$TEST_DIR/bin" "$TEST_DIR/project"
touch "$TEST_DIR/project/docker-compose.yaml" "$TEST_DIR/runtime.compose.yaml"

jq -n '{
  name: "scribe-prod",
  services: {
    mariadb: {},
    triplet: {},
    api: {},
    worker: {},
    traefik: {
      networks: {
        default: {ipv4_address: "172.30.0.2"}
      }
    }
  },
  networks: {
    default: {
      name: "scribe-prod_default",
      ipam: {
        config: [{
          subnet: "172.30.0.0/24",
          ip_range: "172.30.0.128/25",
          gateway: "172.30.0.1"
        }]
      }
    }
  }
}' >"$TEST_DIR/config.json"
cp "$TEST_DIR/config.json" "$TEST_DIR/config-valid.json"

write_container_inspection() {
  local traefik_status="${1:-running}"
  local traefik_health="${2:-healthy}"

  jq -n \
    --arg mariadb "$CONTAINER_MARIADB" \
    --arg triplet "$CONTAINER_TRIPLET" \
    --arg api "$CONTAINER_API" \
    --arg worker "$CONTAINER_WORKER" \
    --arg traefik "$CONTAINER_TRAEFIK" \
    --arg traefik_status "$traefik_status" \
    --arg traefik_health "$traefik_health" '
    def container($id; $service; $status; $health):
      {
        Id: $id,
        Config: {
          Labels: {
            "com.docker.compose.project": "scribe-prod",
            "com.docker.compose.service": $service,
            "com.docker.compose.oneoff": "False"
          }
        },
        State: {
          Status: $status,
          Health: {Status: $health}
        },
        NetworkSettings: {
          Networks: {"scribe-prod_default": {}}
        }
      };
    [
      container($mariadb; "mariadb"; "running"; "healthy"),
      container($triplet; "triplet"; "running"; "healthy"),
      container($api; "api"; "running"; "healthy"),
      container($worker; "worker"; "running"; "healthy"),
      container($traefik; "traefik"; $traefik_status; $traefik_health)
    ]
  ' >"$TEST_DIR/containers.json"
}

write_network_inspection() {
  local ip_range="${1:-172.30.0.128/25}"
  local traefik_address="${2:-172.30.0.2/24}"
  local add_foreign="${3:-false}"
  local network_name="${4:-scribe-prod_default}"

  jq -n \
    --arg network "$NETWORK_DEFAULT" \
    --arg mariadb "$CONTAINER_MARIADB" \
    --arg triplet "$CONTAINER_TRIPLET" \
    --arg api "$CONTAINER_API" \
    --arg worker "$CONTAINER_WORKER" \
    --arg traefik "$CONTAINER_TRAEFIK" \
    --arg foreign "$CONTAINER_FOREIGN" \
    --arg ip_range "$ip_range" \
    --arg traefik_address "$traefik_address" \
    --arg network_name "$network_name" \
    --argjson add_foreign "$add_foreign" '
    [{
      Id: $network,
      Name: $network_name,
      Driver: "bridge",
      Labels: {"com.docker.compose.project": "scribe-prod"},
      IPAM: {
        Config: [{
          Subnet: "172.30.0.0/24",
          IPRange: $ip_range,
          Gateway: "172.30.0.1"
        }]
      },
      Containers: ({
        ($mariadb): {IPv4Address: "172.30.0.130/24"},
        ($triplet): {IPv4Address: "172.30.0.131/24"},
        ($api): {IPv4Address: "172.30.0.132/24"},
        ($worker): {IPv4Address: "172.30.0.133/24"},
        ($traefik): {IPv4Address: $traefik_address}
      } + if $add_foreign then {
        ($foreign): {IPv4Address: "172.30.0.140/24"}
      } else {} end)
    }]
  ' >"$TEST_DIR/network.json"
}

cat >"$TEST_DIR/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

printf '%s\n' "$*" >>"$TEST_DOCKER_LOG"

if [[ "${1:-}" == "compose" ]]; then
  cat "$TEST_CONFIG_FILE"
  exit 0
fi

case "$*" in
  "info --format {{.DockerRootDir}}")
    printf '%s\n' "${TEST_ACTUAL_DOCKER_ROOT:-/mnt/disks/data/docker}"
    ;;
  "ps --all --quiet --no-trunc --filter label=com.docker.compose.project=scribe-prod")
    count="$(<"$TEST_PS_COUNT_FILE")"
    printf '%s' "$((count + 1))" >"$TEST_PS_COUNT_FILE"
    if [[ "$count" -eq 0 ]]; then
      printf '%s' "${TEST_CONTAINERS_BEFORE:-}"
    else
      printf '%s' "${TEST_CONTAINERS_AFTER:-}"
    fi
    ;;
  "network ls --quiet --no-trunc --filter label=com.docker.compose.project=scribe-prod")
    count="$(<"$TEST_NETWORK_COUNT_FILE")"
    printf '%s' "$((count + 1))" >"$TEST_NETWORK_COUNT_FILE"
    if [[ "$count" -eq 0 ]]; then
      printf '%s' "${TEST_NETWORKS_BEFORE:-}"
    else
      printf '%s' "${TEST_NETWORKS_AFTER:-}"
    fi
    ;;
  "network ls --quiet --no-trunc --filter name=^scribe-prod_default$")
    printf '%s' "${TEST_NAMED_NETWORKS:-}"
    ;;
  container\ inspect\ *)
    cat "$TEST_CONTAINER_INSPECTION"
    ;;
  network\ inspect\ *)
    cat "$TEST_NETWORK_INSPECTION"
    ;;
  container\ stop\ --time\ 30\ *)
    exit "${TEST_STOP_STATUS:-0}"
    ;;
  container\ rm\ --force\ *)
    exit "${TEST_CONTAINER_RM_STATUS:-0}"
    ;;
  network\ rm\ *)
    exit "${TEST_NETWORK_RM_STATUS:-0}"
    ;;
  *)
    echo "Unexpected Docker invocation: $*" >&2
    exit 99
    ;;
esac
EOF
chmod 0755 "$TEST_DIR/bin/docker"

all_containers="$CONTAINER_MARIADB"$'\n'"$CONTAINER_TRIPLET"$'\n'"$CONTAINER_API"$'\n'"$CONTAINER_WORKER"$'\n'"$CONTAINER_TRAEFIK"

reset_state() {
  : >"$TEST_DIR/docker.log"
  printf '0' >"$TEST_DIR/ps-count"
  printf '0' >"$TEST_DIR/network-count"
  write_container_inspection
  write_network_inspection
}

run_preflight() {
  TEST_DOCKER_LOG="$TEST_DIR/docker.log" \
    TEST_CONFIG_FILE="$TEST_DIR/config.json" \
    TEST_ACTUAL_DOCKER_ROOT="${TEST_ACTUAL_DOCKER_ROOT:-/mnt/disks/data/docker}" \
    SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker \
    PATH="$TEST_DIR/bin:$PATH" \
    bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-compose-runtime-preflight.sh" \
    "$TEST_DIR/project" \
    "$TEST_DIR/project/docker-compose.yaml" \
    "$TEST_DIR/runtime.compose.yaml"
}

run_convergence() {
  TEST_DOCKER_LOG="$TEST_DIR/docker.log" \
    TEST_CONFIG_FILE="$TEST_DIR/config.json" \
    TEST_PS_COUNT_FILE="$TEST_DIR/ps-count" \
    TEST_NETWORK_COUNT_FILE="$TEST_DIR/network-count" \
    TEST_CONTAINER_INSPECTION="$TEST_DIR/containers.json" \
    TEST_NETWORK_INSPECTION="$TEST_DIR/network.json" \
    TEST_ACTUAL_DOCKER_ROOT="${TEST_ACTUAL_DOCKER_ROOT:-/mnt/disks/data/docker}" \
    TEST_CONTAINERS_BEFORE="${TEST_CONTAINERS_BEFORE:-}" \
    TEST_CONTAINERS_AFTER="${TEST_CONTAINERS_AFTER:-}" \
    TEST_NETWORKS_BEFORE="${TEST_NETWORKS_BEFORE:-}" \
    TEST_NETWORKS_AFTER="${TEST_NETWORKS_AFTER:-}" \
    TEST_NAMED_NETWORKS="${TEST_NAMED_NETWORKS:-}" \
    TEST_STOP_STATUS="${TEST_STOP_STATUS:-0}" \
    TEST_CONTAINER_RM_STATUS="${TEST_CONTAINER_RM_STATUS:-0}" \
    TEST_NETWORK_RM_STATUS="${TEST_NETWORK_RM_STATUS:-0}" \
    SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker \
    PATH="$TEST_DIR/bin:$PATH" \
    bash "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-compose-runtime-preflight.sh" \
    --converge \
    "$TEST_DIR/project" \
    "$TEST_DIR/project/docker-compose.yaml" \
    "$TEST_DIR/runtime.compose.yaml"
}

assert_invalid_compose_name() {
  local label="$1"
  local filter="$2"

  jq "$filter" "$TEST_DIR/config-valid.json" >"$TEST_DIR/config.json"
  reset_state
  if run_preflight >"$TEST_DIR/${label}.out" 2>&1; then
    fail "validation-only mode accepted ${label}"
  fi
  if rg -q '^info |^ps |^network |container (stop|rm)' "$TEST_DIR/docker.log"; then
    fail "${label} reached runtime inspection or cleanup"
  fi
  cp "$TEST_DIR/config-valid.json" "$TEST_DIR/config.json"
}

reset_state
run_preflight >"$TEST_DIR/preflight.out"
grep -Fxq 'Compose runtime preflight passed.' "$TEST_DIR/preflight.out" ||
  fail "validation-only mode did not pass"
[[ "$(wc -l <"$TEST_DIR/docker.log")" -eq 2 ]] ||
  fail "validation-only mode inspected or changed runtime state"
grep -Fxq 'info --format {{.DockerRootDir}}' "$TEST_DIR/docker.log" ||
  fail "validation-only mode did not verify the persistent Docker root"
if rg -q '^ps |^network ' "$TEST_DIR/docker.log"; then
  fail "validation-only mode inspected Compose runtime resources"
fi

assert_invalid_compose_name uppercase-project '.name = "Scribe-prod"'
assert_invalid_compose_name unicode-project '.name = "scribe-é"'
assert_invalid_compose_name leading-punctuation-service \
  "(.services.api) as \$api | del(.services.api) | .services[\".api\"] = \$api"
assert_invalid_compose_name whitespace-network \
  '.networks.default.name = "scribe prod_default"'

reset_state
if TEST_ACTUAL_DOCKER_ROOT=/var/lib/docker \
  run_preflight >"$TEST_DIR/preflight-wrong-root.out" 2>&1; then
  fail "validation-only mode accepted a non-persistent Docker data root"
fi
if rg -q '^ps |^network |container (stop|rm)' "$TEST_DIR/docker.log"; then
  fail "a pre-pull Docker-root mismatch reached runtime inspection or cleanup"
fi

reset_state
run_convergence >"$TEST_DIR/absent.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=absent' "$TEST_DIR/absent.out" ||
  fail "an absent runtime was not treated as converged"
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "an absent runtime reached cleanup"
fi

reset_state
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/healthy.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=healthy' "$TEST_DIR/healthy.out" ||
  fail "a healthy compatible runtime was not preserved"
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "a healthy compatible runtime was disrupted"
fi

reset_state
jq '.[4].Config.Labels["com.docker.compose.service"] = "api"' \
  "$TEST_DIR/containers.json" >"$TEST_DIR/containers-duplicate.json"
mv "$TEST_DIR/containers-duplicate.json" "$TEST_DIR/containers.json"
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/duplicate-service.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=service-set' \
  "$TEST_DIR/duplicate-service.out" ||
  fail "duplicate exact-project services did not trigger bounded recovery"
grep -Fxq "network rm $NETWORK_DEFAULT" "$TEST_DIR/docker.log" ||
  fail "duplicate-service recovery did not remove the exact-project network"

reset_state
jq '.[4].Config.Labels["com.docker.compose.service"] = "retired-service"' \
  "$TEST_DIR/containers.json" >"$TEST_DIR/containers-retired.json"
mv "$TEST_DIR/containers-retired.json" "$TEST_DIR/containers.json"
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/retired-service.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=service-set' \
  "$TEST_DIR/retired-service.out" ||
  fail "a syntactically safe retired service did not trigger bounded recovery"
grep -Fxq "network rm $NETWORK_DEFAULT" "$TEST_DIR/docker.log" ||
  fail "retired-service recovery did not remove the exact-project network"

reset_state
jq '.[4].Config.Labels["com.docker.compose.service"] = "api\n"' \
  "$TEST_DIR/containers.json" >"$TEST_DIR/containers-unsafe.json"
mv "$TEST_DIR/containers-unsafe.json" "$TEST_DIR/containers.json"
if TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/unsafe-service.out" 2>&1; then
  fail "an unsafe exact-project service label was accepted"
fi
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "an unsafe service label reached destructive convergence"
fi

reset_state
write_network_inspection 172.30.0.128/25 172.30.0.2/24 false scribe-prod_previous
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/mismatched-network-name.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=network-set' \
  "$TEST_DIR/mismatched-network-name.out" ||
  fail "a retired exact-project network name did not trigger bounded recovery"
grep -Fxq "network rm $NETWORK_DEFAULT" "$TEST_DIR/docker.log" ||
  fail "the retired exact-project network was not removed"

reset_state
write_network_inspection 172.30.0.128/25 172.30.0.2/24 false $'scribe-prod\nprevious'
if TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/unsafe-network-name.out" 2>&1; then
  fail "an unsafe exact-project network name was accepted"
fi
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "an unsafe network name reached destructive convergence"
fi

reset_state
jq --arg old "$NETWORK_OLD" '
  .[0] as $canonical
  | . + [
      $canonical
      | .Id = $old
      | .Name = "scribe-prod_previous"
      | .Containers = {}
    ]
' "$TEST_DIR/network.json" >"$TEST_DIR/networks-multiple.json"
mv "$TEST_DIR/networks-multiple.json" "$TEST_DIR/network.json"
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT"$'\n'"$NETWORK_OLD" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/multiple-networks.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=network-set' \
  "$TEST_DIR/multiple-networks.out" ||
  fail "multiple exact-project networks did not trigger bounded recovery"
grep -Fxq "network rm $NETWORK_DEFAULT $NETWORK_OLD" "$TEST_DIR/docker.log" ||
  fail "multiple exact-project networks were not removed by validated identity"

reset_state
write_network_inspection 172.30.0.0/24
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/ipam.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=network-ipam' "$TEST_DIR/ipam.out" ||
  fail "incompatible project IPAM did not trigger bounded recovery"
grep -Fq "container stop --time 30 $CONTAINER_MARIADB $CONTAINER_TRIPLET $CONTAINER_API $CONTAINER_WORKER $CONTAINER_TRAEFIK" \
  "$TEST_DIR/docker.log" ||
  fail "stale exact-project containers were not stopped gracefully"
grep -Fq "container rm --force $CONTAINER_MARIADB $CONTAINER_TRIPLET $CONTAINER_API $CONTAINER_WORKER $CONTAINER_TRAEFIK" \
  "$TEST_DIR/docker.log" ||
  fail "stale exact-project containers were not removed by validated identity"
grep -Fxq "network rm $NETWORK_DEFAULT" "$TEST_DIR/docker.log" ||
  fail "the incompatible exact-project network was not removed"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=ready' "$TEST_DIR/ipam.out" ||
  fail "IPAM recovery did not establish the ready postcondition"

reset_state
write_container_inspection created starting
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/stale.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=container-state' "$TEST_DIR/stale.out" ||
  fail "stale exact-project container state did not trigger recovery"

reset_state
write_network_inspection 172.30.0.128/25 172.30.0.140/24
TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/collision.out"
grep -Fxq 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=traefik-address' "$TEST_DIR/collision.out" ||
  fail "a fixed Traefik address collision did not trigger recovery"

reset_state
write_network_inspection 172.30.0.128/25 172.30.0.2/24 true
if TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/foreign.out" 2>&1; then
  fail "a foreign endpoint on the project network was accepted"
fi
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "a foreign endpoint reached destructive convergence"
fi

reset_state
if TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/unowned-network.out" 2>&1; then
  fail "an unowned canonical network was adopted"
fi
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "an unowned canonical network reached destructive convergence"
fi

reset_state
if TEST_ACTUAL_DOCKER_ROOT=/var/lib/docker \
  TEST_CONTAINERS_BEFORE="$all_containers" \
  TEST_NETWORKS_BEFORE="$NETWORK_DEFAULT" \
  TEST_NAMED_NETWORKS="$NETWORK_DEFAULT" \
  run_convergence >"$TEST_DIR/wrong-root.out" 2>&1; then
  fail "convergence ran against a non-persistent Docker data root"
fi
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "a wrong Docker data root reached destructive convergence"
fi

reset_state
if TEST_CONTAINERS_BEFORE=not-an-id \
  run_convergence >"$TEST_DIR/invalid-id.out" 2>&1; then
  fail "an invalid Docker identity was accepted"
fi
if rg -q 'container (stop|rm)|network rm' "$TEST_DIR/docker.log"; then
  fail "an invalid Docker identity reached destructive convergence"
fi

if rg -q -- '--volumes|docker[[:space:]]+(volume|system[[:space:]]+prune|container[[:space:]]+prune|network[[:space:]]+prune)' \
  "$ROOT_DIR/terraform/rootfs/home/cloud-compose/scribe-compose-runtime-preflight.sh"; then
  fail "runtime convergence can delete persistent volumes or prune unrelated state"
fi

echo "Compose runtime preflight preserves healthy state and recovers only stale exact-project resources."
