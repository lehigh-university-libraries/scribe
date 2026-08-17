#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Persistence generation test failed: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

current_generation="canonical-v2"
prior_generation="canonical-v1"

# App-owned token repair changes one nonsecret service label. Docker Compose
# includes labels in its service configuration hash, so an ordinary local or
# hosted `compose up` recreates every process that copied a repaired file token
# into its environment without disrupting MariaDB's persistence generation.
render_token_generation() {
  SCRIBE_LOCAL_TOKEN_GENERATION="$1" \
    docker compose -f docker-compose.yaml config --format json
}

render_token_generation_hashes() {
  SCRIBE_LOCAL_TOKEN_GENERATION="$1" \
    docker compose -f docker-compose.yaml config --hash '*' |
    sort
}

first_token_generation="generation-one"
second_token_generation="generation-two"
first_token_config="$(render_token_generation "$first_token_generation")"
first_token_hashes="$(render_token_generation_hashes "$first_token_generation")"
second_token_hashes="$(render_token_generation_hashes "$second_token_generation")"
token_generation_label="org.libops.scribe.local-token-generation"
local_token_secrets='[
  "page_token_signing_key",
  "triplet_presentation_write_token",
  "triplet_source_read_token"
]'
mapfile -t token_consumers < <(
  jq -er --argjson token_secrets "$local_token_secrets" '
    .services
    | to_entries[]
    | . as $service
    | [$service.value.secrets[]?.source] as $service_secrets
    | select(any($service_secrets[];
        . as $source | $token_secrets | index($source)))
    | $service.key
  ' <<<"$first_token_config" |
    sort
)
[[ "${token_consumers[*]}" == "api triplet worker" ]] ||
  fail "unexpected app-owned token consumer set: ${token_consumers[*]}"
mapfile -t all_services < <(jq -er '.services | keys[]' <<<"$first_token_config")
for service in "${all_services[@]}"; do
  first_hash="$(awk -v service="$service" '$1 == service { print $2 }' <<<"$first_token_hashes")"
  second_hash="$(awk -v service="$service" '$1 == service { print $2 }' <<<"$second_token_hashes")"
  [[ "$first_hash" =~ ^[0-9a-f]{64}$ && "$second_hash" =~ ^[0-9a-f]{64}$ ]] ||
    fail "Docker Compose omitted the ${service} service hash"

  if [[ " ${token_consumers[*]} " == *" ${service} "* ]]; then
    jq -e \
      --arg service "$service" \
      --arg label "$token_generation_label" \
      --arg generation "$first_token_generation" \
      '.services[$service].labels[$label] == $generation' \
      <<<"$first_token_config" >/dev/null ||
      fail "${service} does not consume the local-token generation label"
    [[ "$first_hash" != "$second_hash" ]] ||
      fail "${service} hash does not change after token repair"
  else
    jq -e \
      --arg service "$service" \
      --arg label "$token_generation_label" \
      '.services[$service].labels[$label] == null' \
      <<<"$first_token_config" >/dev/null ||
      fail "${service} is unnecessarily coupled to app-token repair"
    [[ "$first_hash" == "$second_hash" ]] ||
      fail "${service} hash changed even though it consumes no repaired token"
  fi
done

volume_names() {
  local site="$1"
  local workspace="$2"
  local generation="$3"
  local source_ref="$4"
  local project_name="${site}-${workspace}"

  # SCRIBE_DEPLOY_REF is intentionally irrelevant to Compose volume identity.
  # Supplying it makes the two-ref invariant explicit in this executable test.
  COMPOSE_PROJECT_NAME="$project_name" \
    SCRIBE_DATA_GENERATION="$generation" \
    SCRIBE_DEPLOY_REF="$source_ref" \
    docker compose \
      -p "$project_name" \
      -f docker-compose.yaml \
      -f terraform/rootfs/home/cloud-compose/scribe-runtime.compose.yaml \
      config --format json \
    | jq -r '.volumes | to_entries | sort_by(.key) | map(.value.name) | .[]'
}

first_ref="$(volume_names scribe prod "$current_generation" 1111111111111111111111111111111111111111)"
second_ref="$(volume_names scribe prod "$current_generation" 2222222222222222222222222222222222222222)"
[[ "$first_ref" == "$second_ref" ]] || fail "volume names changed across immutable source refs"

default_ref="$(volume_names scribe prod "" 1111111111111111111111111111111111111111)"
[[ "$first_ref" == "$default_ref" ]] || fail "Compose does not default to the current persistence generation"

changed_workspace="$(volume_names scribe pr-75 "$current_generation" 1111111111111111111111111111111111111111)"
[[ "$first_ref" != "$changed_workspace" ]] || fail "production and preview workspaces share volume names"

changed_generation="$(volume_names scribe prod "$prior_generation" 1111111111111111111111111111111111111111)"
[[ "$first_ref" != "$changed_generation" ]] || fail "persistence generations share volume names"

while IFS= read -r volume; do
  [[ "$volume" == scribe-prod-canonical-v2-* ]] || fail "unexpected production volume name: $volume"
done <<<"$first_ref"

while IFS= read -r volume; do
  [[ "$volume" == scribe-prod-canonical-v1-* ]] || fail "unexpected prior-generation volume name: $volume"
done <<<"$changed_generation"

echo "Persistence generation rendering behavior passed."
