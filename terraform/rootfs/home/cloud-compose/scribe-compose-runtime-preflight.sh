#!/usr/bin/env bash

set -euo pipefail
export LC_ALL=C

converge=false
if [[ "${1:-}" == "--converge" ]]; then
  converge=true
  shift
fi

if [[ "$#" -lt 2 ]]; then
  echo "Usage: $0 [--converge] PROJECT_DIR COMPOSE_FILE [COMPOSE_FILE ...]" >&2
  exit 2
fi

readonly PROJECT_DIR="$1"
shift
readonly ENV_FILE="${PROJECT_DIR}/.env"
declare -a COMPOSE_ARGS=()
declare -a container_ids=()
declare -a network_ids=()

fail() {
  echo "Compose runtime preflight failed: $*" >&2
  exit 1
}

valid_project_name() {
  [[ "$1" =~ ^[a-z0-9][a-z0-9_-]*$ ]]
}

valid_runtime_name() {
  [[ "$1" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]]
}

command -v docker >/dev/null 2>&1 || fail "Docker with the Compose plugin is required."
command -v jq >/dev/null 2>&1 || fail "jq is required."

[[ "$PROJECT_DIR" == /* && ! -L "$PROJECT_DIR" && -d "$PROJECT_DIR" ]] ||
  fail "the deployment checkout is missing or unsafe."
for compose_file in "$@"; do
  [[ "$compose_file" == /* && ! -L "$compose_file" && -f "$compose_file" ]] ||
    fail "a Compose file is missing or unsafe."
  COMPOSE_ARGS+=(-f "$compose_file")
done
if [[ -e "$ENV_FILE" || -L "$ENV_FILE" ]]; then
  [[ ! -L "$ENV_FILE" && -f "$ENV_FILE" ]] ||
    fail ".env must be a regular file, not a link or special file."
  [[ -r "$ENV_FILE" ]] || fail ".env is not readable by the runtime account."
  [[ -w "$ENV_FILE" ]] || fail ".env is not writable by the runtime account."
fi
[[ -w "$PROJECT_DIR" ]] ||
  fail "the deployment checkout is not writable by the runtime account."

compose_config="$(
  docker compose \
    --project-directory "$PROJECT_DIR" \
    "${COMPOSE_ARGS[@]}" \
    config --format json
)" || fail "Docker Compose could not render the canonical configuration."

runtime_contract="$(jq -ce '
  def ipv4_number:
    split(".")
    | if length != 4 then error("invalid IPv4 address") else . end
    | map(tonumber)
    | if any(.[]; . < 0 or . > 255 or floor != .) then
        error("invalid IPv4 octet")
      else
        .[0] * 16777216 + .[1] * 65536 + .[2] * 256 + .[3]
      end;
  def block_size($prefix):
    if $prefix == 24 then 256
    elif $prefix == 25 then 128
    elif $prefix == 26 then 64
    elif $prefix == 27 then 32
    elif $prefix == 28 then 16
    elif $prefix == 29 then 8
    else 0
    end;
  def parsed_cidr:
    split("/") as $parts
    | if $parts | length != 2 then error("invalid CIDR") else . end
    | ($parts[1] | tonumber) as $prefix
    | (block_size($prefix)) as $size
    | if $size == 0 then error("unsupported CIDR prefix") else . end
    | ($parts[0] | ipv4_number) as $address
    | {
        prefix: $prefix,
        size: $size,
        network: ((($address / $size) | floor) * $size)
      };

  select(
    (.name | type) == "string" and
    (.services | type) == "object" and
    (.services | length) > 0 and
    (.networks.default.name | type) == "string"
  )
  | .networks.default.ipam.config as $configs
  | select(($configs | type) == "array" and ($configs | length) == 1)
  | ($configs[0].subnet | parsed_cidr) as $subnet
  | ($configs[0].ip_range | parsed_cidr) as $dynamic
  | ($configs[0].gateway | ipv4_number) as $gateway
  | (.services.traefik.networks.default.ipv4_address | ipv4_number) as $traefik
  | select(
      $subnet.prefix >= 24 and
      $subnet.prefix <= 28 and
      $dynamic.prefix == ($subnet.prefix + 1) and
      $dynamic.network == ($subnet.network + ($subnet.size / 2)) and
      ($dynamic.size - 2) >= 6 and
      $gateway == ($subnet.network + 1) and
      $traefik == ($subnet.network + 2) and
      $gateway < $dynamic.network
    )
  | {
      project: .name,
      services: (.services | keys | sort),
      network: {
        name: .networks.default.name,
        subnet: $configs[0].subnet,
        ip_range: $configs[0].ip_range,
        gateway: $configs[0].gateway,
        traefik: .services.traefik.networks.default.ipv4_address
      }
    }
' <<<"$compose_config")" || runtime_contract=""
if [[ -z "$runtime_contract" ]]; then
  fail "the bridge gateway, fixed Traefik address, and upper-half dynamic range are inconsistent."
fi

declare -a project_names=()
mapfile -t project_names < <(jq -er '.project' <<<"$runtime_contract")
if [[ "${#project_names[@]}" -ne 1 ]] ||
  ! valid_project_name "${project_names[0]}"; then
  fail "the Compose project name is unsafe."
fi
project_name="${project_names[0]}"
readonly project_name
readonly project_filter="label=com.docker.compose.project=${project_name}"

declare -a network_names=()
mapfile -t network_names < <(jq -er '.network.name' <<<"$runtime_contract")
if [[ "${#network_names[@]}" -ne 1 ]] ||
  ! valid_runtime_name "${network_names[0]}"; then
  fail "the Compose network name is unsafe."
fi
network_name="${network_names[0]}"
readonly network_name
desired_subnet="$(jq -r '.network.subnet' <<<"$runtime_contract")"
readonly desired_subnet
desired_ip_range="$(jq -r '.network.ip_range' <<<"$runtime_contract")"
readonly desired_ip_range
desired_gateway="$(jq -r '.network.gateway' <<<"$runtime_contract")"
readonly desired_gateway
desired_traefik_ip="$(jq -r '.network.traefik' <<<"$runtime_contract")"
readonly desired_traefik_ip
expected_services="$(jq -c '.services' <<<"$runtime_contract")"
readonly expected_services
expected_service_count="$(jq -er '.services | length' <<<"$runtime_contract")"
readonly expected_service_count
declare -a expected_service_names=()
mapfile -t expected_service_names < <(jq -er '.services[]' <<<"$runtime_contract")
[[ "${#expected_service_names[@]}" -eq "$expected_service_count" ]] ||
  fail "a Compose service name is unsafe."
for service_name in "${expected_service_names[@]}"; do
  valid_runtime_name "$service_name" ||
    fail "a Compose service name is unsafe."
done
readonly expected_docker_root="${SCRIBE_EXPECTED_DOCKER_ROOT:-}"

if [[ -n "$expected_docker_root" ]]; then
  if [[ "$expected_docker_root" != /* ||
    "$expected_docker_root" == "/" ||
    "$expected_docker_root" == *$'\n'* ||
    "$expected_docker_root" == *$'\r'* ]]; then
    fail "the expected Docker data root is unsafe."
  fi
  actual_docker_root="$(docker info --format '{{.DockerRootDir}}')" ||
    fail "the Docker data root could not be verified."
  [[ "$actual_docker_root" == "$expected_docker_root" ]] ||
    fail "Docker is not using the expected persistent data root."
fi

printf 'Compose runtime preflight passed.\n'

[[ "$converge" == true ]] || exit 0

validated_ids() {
  local kind="$1" payload="$2" result_name="$3" id
  local -n result="$result_name"

  result=()
  [[ -n "$payload" ]] || return 0
  mapfile -t result <<<"$payload"
  for id in "${result[@]}"; do
    [[ "$id" =~ ^[0-9a-f]{64}$ ]] ||
      fail "Docker returned an invalid ${kind} identity."
  done
}

list_project_containers() {
  docker ps --all --quiet --no-trunc --filter "$project_filter"
}

list_project_networks() {
  docker network ls --quiet --no-trunc --filter "$project_filter"
}

container_output="$(list_project_containers)" ||
  fail "the exact Compose-project containers could not be queried."
network_output="$(list_project_networks)" ||
  fail "the exact Compose-project networks could not be queried."
validated_ids container "$container_output" container_ids
validated_ids network "$network_output" network_ids

# A network with the canonical name but without the exact project label is
# outside this lifecycle's authority. Refuse to mutate it rather than adopting
# or deleting an unrelated Docker resource.
named_network_output="$(
  docker network ls --quiet --no-trunc --filter "name=^${network_name}$"
)" || fail "the canonical Compose network name could not be queried."
declare -a named_network_ids=()
validated_ids network "$named_network_output" named_network_ids
for id in "${named_network_ids[@]}"; do
  if [[ ! " ${network_ids[*]} " =~ [[:space:]]${id}[[:space:]] ]]; then
    fail "the canonical Compose network name is owned outside the exact project label."
  fi
done

if ((${#container_ids[@]} == 0 && ${#network_ids[@]} == 0)); then
  printf 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=absent\n'
  exit 0
fi

container_inspection='[]'
if ((${#container_ids[@]} > 0)); then
  container_inspection="$(docker container inspect "${container_ids[@]}")" ||
    fail "the exact Compose-project containers could not be inspected."
fi
network_inspection='[]'
if ((${#network_ids[@]} > 0)); then
  network_inspection="$(docker network inspect "${network_ids[@]}")" ||
    fail "the exact Compose-project networks could not be inspected."
fi

project_ids="$(
  printf '%s\n' "${container_ids[@]}" |
    jq -Rsc 'split("\n") | map(select(length > 0)) | sort'
)"
network_ids_json="$(
  printf '%s\n' "${network_ids[@]}" |
    jq -Rsc 'split("\n") | map(select(length > 0)) | sort'
)"
if ! jq -e \
  --arg project "$project_name" \
  --argjson project_ids "$project_ids" '
    ([.[].Id] | sort) == $project_ids and
    all(.[];
      .Config.Labels["com.docker.compose.project"] == $project and
      (.Config.Labels["com.docker.compose.service"] | type) == "string"
    )
  ' <<<"$container_inspection" >/dev/null; then
  fail "exact Compose-project container ownership is ambiguous; refusing cleanup."
fi
if ! jq -e \
  --arg project "$project_name" \
  --argjson network_ids "$network_ids_json" '
    ([.[].Id] | sort) == $network_ids and
    all(.[];
      .Labels["com.docker.compose.project"] == $project and
      (.Name | type) == "string"
    )
  ' <<<"$network_inspection" >/dev/null; then
  fail "exact Compose-project network ownership is ambiguous; refusing cleanup."
fi

declare -a inspected_service_names=()
mapfile -t inspected_service_names < <(
  jq -er '.[].Config.Labels["com.docker.compose.service"]' \
    <<<"$container_inspection"
)
[[ "${#inspected_service_names[@]}" -eq "${#container_ids[@]}" ]] ||
  fail "exact Compose-project container ownership is ambiguous; refusing cleanup."
for service_name in "${inspected_service_names[@]}"; do
  valid_runtime_name "$service_name" ||
    fail "exact Compose-project container ownership is ambiguous; refusing cleanup."
done

declare -a inspected_network_names=()
mapfile -t inspected_network_names < <(jq -er '.[].Name' <<<"$network_inspection")
[[ "${#inspected_network_names[@]}" -eq "${#network_ids[@]}" ]] ||
  fail "exact Compose-project network ownership is ambiguous; refusing cleanup."
for inspected_network_name in "${inspected_network_names[@]}"; do
  valid_runtime_name "$inspected_network_name" ||
    fail "exact Compose-project network ownership is ambiguous; refusing cleanup."
done

if ! jq -e --argjson project_ids "$project_ids" '
  ([.[] | (.Containers // {}) | keys[]] - $project_ids) | length == 0
' <<<"$network_inspection" >/dev/null; then
  fail "an exact Compose-project network has a non-project endpoint; refusing to disrupt it."
fi

reason=""
if ! jq -e \
  --argjson services "$expected_services" '
    length == ($services | length) and
    ([.[].Config.Labels["com.docker.compose.service"]] | sort) == $services and
    ([.[].Config.Labels["com.docker.compose.service"]] | unique | length) == length
  ' <<<"$container_inspection" >/dev/null; then
  reason="service-set"
elif ((${#network_ids[@]} != 1)) ||
  ! jq -e --arg network "$network_name" 'all(.[]; .Name == $network)' \
    <<<"$network_inspection" >/dev/null; then
  reason="network-set"
elif ! jq -e \
  --arg project "$project_name" \
  --arg network "$network_name" \
  --argjson services "$expected_services" '
    length == ($services | length) and
    ([.[].Config.Labels["com.docker.compose.service"]] | sort) == $services and
    all(.[];
      .Config.Labels["com.docker.compose.project"] == $project and
      ((.Config.Labels["com.docker.compose.oneoff"] // "false") | ascii_downcase) == "false" and
      .State.Status == "running" and
      (.State.Health == null or .State.Health.Status == "healthy") and
      (.NetworkSettings.Networks | keys) == [$network]
    )
  ' <<<"$container_inspection" >/dev/null; then
  reason="container-state"
elif ! jq -e \
  --arg project "$project_name" \
  --arg network "$network_name" \
  --arg subnet "$desired_subnet" \
  --arg ip_range "$desired_ip_range" \
  --arg gateway "$desired_gateway" '
    length == 1 and
    .[0].Name == $network and
    .[0].Driver == "bridge" and
    .[0].Labels["com.docker.compose.project"] == $project and
    .[0].IPAM.Config == [{
      Subnet: $subnet,
      IPRange: $ip_range,
      Gateway: $gateway
    }]
  ' <<<"$network_inspection" >/dev/null; then
  reason="network-ipam"
elif ! jq -e \
  --argjson project_ids "$project_ids" '
    ([.[0].Containers | keys] | flatten | sort) == $project_ids
  ' <<<"$network_inspection" >/dev/null; then
  reason="network-attachments"
else
  traefik_id="$(jq -er '
    .[]
    | select(.Config.Labels["com.docker.compose.service"] == "traefik")
    | .Id
  ' <<<"$container_inspection")" ||
    fail "the Traefik container identity could not be resolved."
  if ! jq -e \
    --arg address "$desired_traefik_ip" \
    --arg traefik_id "$traefik_id" '
      [
        .[0].Containers
        | to_entries[]
        | select((.value.IPv4Address | split("/")[0]) == $address)
        | .key
      ] == [$traefik_id]
    ' <<<"$network_inspection" >/dev/null; then
    reason="traefik-address"
  fi
fi

if [[ -z "$reason" ]]; then
  printf 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=healthy\n'
  exit 0
fi

printf 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=rebuild reason=%s\n' "$reason"
if ((${#container_ids[@]} > 0)); then
  if ! docker container stop --time 30 "${container_ids[@]}"; then
    echo "Graceful exact-project shutdown failed; forcing bounded container cleanup." >&2
  fi
  docker container rm --force "${container_ids[@]}" ||
    fail "the exact Compose-project containers could not be removed."
fi
container_output="$(list_project_containers)" ||
  fail "the exact Compose-project containers could not be verified after cleanup."
[[ -z "$container_output" ]] ||
  fail "exact Compose-project containers remain after cleanup."

if ((${#network_ids[@]} > 0)); then
  docker network rm "${network_ids[@]}" ||
    fail "the exact Compose-project networks could not be removed."
fi
network_output="$(list_project_networks)" ||
  fail "the exact Compose-project networks could not be verified after cleanup."
[[ -z "$network_output" ]] ||
  fail "exact Compose-project networks remain after cleanup."

printf 'SCRIBE_COMPOSE_CONVERGENCE_V1 state=ready\n'
