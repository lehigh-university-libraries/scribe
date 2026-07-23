#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
CONFIRMATION_VALUE="delete-local-mariadb-data"

fail() {
  echo "Local MariaDB reset refused: $*" >&2
  exit 2
}

if [ "${SCRIBE_CONFIRM_RESET_DEV_DB:-}" != "${CONFIRMATION_VALUE}" ]; then
  fail "set SCRIBE_CONFIRM_RESET_DEV_DB=${CONFIRMATION_VALUE} to confirm deletion of only the local Compose MariaDB data volume."
fi

if [ -n "${CI:-}" ] || [ -n "${GITHUB_ACTIONS:-}" ]; then
  fail "this destructive helper is for an interactive local-development stack, not CI."
fi

for command in docker jq; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    fail "${command} is required. Run make doctor for local-development prerequisites."
  fi
done

compose=(
  docker compose
  --project-directory "${ROOT_DIR}"
  -f "${ROOT_DIR}/docker-compose.yaml"
)
if [ -f "${ROOT_DIR}/docker-compose.override.yaml" ]; then
  compose+=(-f "${ROOT_DIR}/docker-compose.override.yaml")
fi

if ! compose_json="$("${compose[@]}" config --format json)"; then
  fail "Docker Compose could not resolve the repository's local stack."
fi

if ! jq -e '
  (.name | type == "string" and test("^[a-z0-9][a-z0-9_.-]*$")) and
  ([.services.mariadb.volumes[]? | select(.target == "/var/lib/mysql")] | length == 1) and
  ([
    .services.mariadb.volumes[]?
    | select(
        .type == "volume" and
        .target == "/var/lib/mysql"
      )
  ] | length == 1) and
  (
    ([
      .services.mariadb.volumes[]?
      | select(.type == "volume" and .target == "/var/lib/mysql")
    ][0].source) as $source
    | ($source | type == "string" and test("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$")) and
      ((.volumes[$source].external // false) == false) and
      (.volumes[$source].name | type == "string" and test("^[a-zA-Z0-9][a-zA-Z0-9_.-]*$"))
  )
' >/dev/null <<<"${compose_json}"; then
  fail "docker-compose.yaml does not define exactly one internal volume mount for MariaDB at /var/lib/mysql."
fi

project_name="$(jq -er '.name' <<<"${compose_json}")"
volume_source="$(jq -er '[.services.mariadb.volumes[] | select(.type == "volume" and .target == "/var/lib/mysql")][0].source' <<<"${compose_json}")"
volume_name="$(jq -er --arg source "${volume_source}" '.volumes[$source].name' <<<"${compose_json}")"
case "${volume_name}" in
  "${project_name}_"* | "${project_name}-"*) ;;
  *) fail "the resolved MariaDB volume name is not scoped to Compose project ${project_name}; nothing was changed." ;;
esac

validate_volume() {
  local volume_json

  if ! volume_json="$(docker volume inspect -- "${volume_name}" 2>/dev/null)"; then
    fail "the resolved Compose volume ${volume_name} does not exist; nothing was changed."
  fi
  if ! jq -e \
    --arg volume "${volume_name}" \
    --arg project "${project_name}" \
    --arg source "${volume_source}" '
      length == 1 and
      .[0].Name == $volume and
      .[0].Labels["com.docker.compose.project"] == $project and
      .[0].Labels["com.docker.compose.volume"] == $source
    ' >/dev/null <<<"${volume_json}"; then
    fail "${volume_name} is not the labeled ${volume_source} volume owned by Compose project ${project_name}; nothing was changed."
  fi
}

validate_volume

if ! container_output="$("${compose[@]}" ps --all --quiet mariadb)"; then
  fail "Docker Compose could not resolve the local MariaDB container."
fi
container_ids=()
while IFS= read -r container_id; do
  if [ -n "${container_id}" ]; then
    container_ids+=("${container_id}")
  fi
done <<<"${container_output}"

if ((${#container_ids[@]} > 1)); then
  fail "Compose resolved more than one MariaDB container; nothing was changed."
fi

container_id="${container_ids[0]:-}"
if [ -n "${container_id}" ]; then
  if [[ ! "${container_id}" =~ ^[a-f0-9]{12,64}$ ]]; then
    fail "Compose returned an invalid MariaDB container ID; nothing was changed."
  fi
  if ! container_json="$(docker container inspect -- "${container_id}")"; then
    fail "the resolved MariaDB container could not be inspected; nothing was changed."
  fi
  if ! jq -e \
    --arg id "${container_id}" \
    --arg project "${project_name}" \
    --arg root "${ROOT_DIR}" \
    --arg volume "${volume_name}" '
      length == 1 and
      .[0].Id == $id and
      .[0].Config.Labels["com.docker.compose.project"] == $project and
      .[0].Config.Labels["com.docker.compose.service"] == "mariadb" and
      .[0].Config.Labels["com.docker.compose.project.working_dir"] == $root and
      ([.[0].Mounts[]? | select(.Destination == "/var/lib/mysql")] | length == 1) and
      ([
        .[0].Mounts[]?
        | select(
            .Type == "volume" and
            .Name == $volume and
            .Destination == "/var/lib/mysql"
          )
      ] | length == 1)
    ' >/dev/null <<<"${container_json}"; then
    fail "the resolved container is not this checkout's MariaDB container mounted to ${volume_name}; nothing was changed."
  fi
fi

if ! stop_services_output="$(jq -er '
  .services as $services |
  def reaches($service; $target; $seen):
    if $service == $target then true
    elif ($seen | index($service)) != null then false
    else any(
      (($services[$service].depends_on // {}) | keys)[]?;
      reaches(.; $target; $seen + [$service])
    )
    end;
  [
    ($services | keys[]) as $service
    | select(reaches($service; "mariadb"; []))
    | $service
  ]
  | (map(select(. != "mariadb")) | sort) + ["mariadb"]
  | .[]
' <<<"${compose_json}")"; then
  fail "could not determine the exact local services that depend on MariaDB."
fi
stop_services=()
while IFS= read -r service; do
  if [ -n "${service}" ]; then
    stop_services+=("${service}")
  fi
done <<<"${stop_services_output}"
if ((${#stop_services[@]} == 0)); then
  fail "could not establish a safe MariaDB service stop order."
fi
last_stop_index=$((${#stop_services[@]} - 1))
if [ "${stop_services[${last_stop_index}]:-}" != "mariadb" ]; then
  fail "could not establish a safe MariaDB service stop order."
fi

echo "Stopping local Compose services that transitively depend on MariaDB: ${stop_services[*]}"
"${compose[@]}" stop "${stop_services[@]}"

if ! post_stop_output="$("${compose[@]}" ps --all --quiet mariadb)"; then
  fail "Docker Compose could not revalidate the stopped MariaDB container."
fi
post_stop_ids=()
while IFS= read -r post_stop_id; do
  if [ -n "${post_stop_id}" ]; then
    post_stop_ids+=("${post_stop_id}")
  fi
done <<<"${post_stop_output}"
if ((${#post_stop_ids[@]} != ${#container_ids[@]})); then
  fail "the MariaDB container changed while the reset was in progress; the volume was not removed."
fi
if [ -n "${container_id}" ] && [ "${post_stop_ids[0]}" != "${container_id}" ]; then
  fail "the MariaDB container changed while the reset was in progress; the volume was not removed."
fi

if [ -n "${container_id}" ]; then
  docker container rm -- "${container_id}" >/dev/null
fi

# Recheck the exact volume labels immediately before the one destructive volume
# operation. Upload, cache, and Triplet volumes are never command targets.
validate_volume
docker volume rm -- "${volume_name}" >/dev/null

echo "Removed local MariaDB container and volume ${volume_name}."
echo "Uploads, cache, and Triplet volumes were left intact. Run make up-db or make up to recreate MariaDB."
