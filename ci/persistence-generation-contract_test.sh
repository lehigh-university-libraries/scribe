#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "Persistence generation contract failed: $*" >&2
  exit 1
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

# The cloud-compose checkout, its local secrets, and the Compose namespace must
# remain stable across immutable application revisions. MariaDB's existing
# named volume and ignored root-password file form one persistence generation,
# so changing either identity requires an explicit credential migration.
rg -q 'primary[[:space:]]*=[[:space:]]*"primary"' terraform/main.tf || fail "Terraform does not select the explicit primary Compose project"
rg -q 'project_dir[[:space:]]*=[[:space:]]*"/mnt/disks/data/scribe/\$\{local\.workspace_slug\}"' terraform/main.tf ||
  fail "Terraform project_dir is not persistence-generation-stable"
rg -q 'compose_project_name[[:space:]]*=[[:space:]]*"\$\{var\.name\}-\$\{local\.workspace_slug\}"' terraform/main.tf ||
  fail "Terraform Compose namespace local is not site/workspace-stable"
rg -q 'compose_project_name[[:space:]]*=[[:space:]]*local\.compose_project_name' terraform/main.tf ||
  fail "Cloud Compose does not consume the stable Compose namespace local"

# The current stable namespace can survive a VM replacement on the persistent
# Docker disk. One-time init only writes environment values and validates the
# runtime contract. The restartable up lifecycle validates again, retries the
# complete reviewed image pull, synchronizes secrets from the already-cached
# API image, then conditionally converges stale exact-project containers and
# networks before Compose up.
compose_init_block="$(sed -n '/^  docker_compose_init = concat(/,/^  docker_compose_up = concat/p' terraform/main.tf)"
compose_up_block="$(sed -n '/^  docker_compose_up = concat(/,/^  docker_compose_down = concat/p' terraform/main.tf)"
# shellcheck disable=SC2016 # Match literal shell expansion in the Terraform command.
runtime_preflight='SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker bash /home/cloud-compose/scribe-compose-runtime-preflight.sh \"$PWD\" \"$PWD/docker-compose.yaml\" /home/cloud-compose/scribe-runtime.compose.yaml'
# shellcheck disable=SC2016 # Match literal shell expansion in the Terraform command.
runtime_convergence='SCRIBE_EXPECTED_DOCKER_ROOT=/mnt/disks/data/docker bash /home/cloud-compose/scribe-compose-runtime-preflight.sh --converge \"$PWD\" \"$PWD/docker-compose.yaml\" /home/cloud-compose/scribe-runtime.compose.yaml'
compose_pull='format("source /home/cloud-compose/profile.sh && retry_until_success docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml pull %s", join(" ", local.docker_compose_services))'
compose_up='format("docker compose -f docker-compose.yaml -f /home/cloud-compose/scribe-runtime.compose.yaml up --no-build --wait --wait-timeout 180 %s", join(" ", local.docker_compose_services))'

line_of_single_command() {
  local block="$1" label="$2" command="$3"
  local matches

  matches="$(grep -nF "$command" <<<"$block" || true)"
  [[ "$(wc -l <<<"$matches")" -eq 1 && -n "$matches" ]] ||
    fail "Terraform must contain the exact ${label} command once"
  cut -d: -f1 <<<"$matches"
}

[[ "$(grep -Fxc "      \"${runtime_convergence}\"," <<<"$compose_up_block" || true)" -eq 1 ]] ||
  fail "Terraform must conditionally converge the current Compose project once"

env_updates_line="$(line_of_single_command "$compose_init_block" environment-updates '    local.compose_env_update_commands,')"
init_preflight_line="$(line_of_single_command "$compose_init_block" init-runtime-preflight "$runtime_preflight")"
((
  env_updates_line < init_preflight_line
)) || fail "init must update the environment before validating the runtime contract"
if grep -Fq 'generate-secrets.sh' <<<"$compose_init_block"; then
  fail "one-time init must not perform restartable Vault-backed secret convergence"
fi
if grep -Fq 'SCRIBE_APP_INIT_TRACE_V1 stage=scribe-' <<<"$compose_init_block"; then
  fail "one-time init retains temporary Scribe bootstrap trace markers"
fi

up_preflight_match="$(grep -nFx "      \"${runtime_preflight}\"," <<<"$compose_up_block" || true)"
[[ "$(wc -l <<<"$up_preflight_match")" -eq 1 && -n "$up_preflight_match" ]] ||
  fail "Terraform must contain the exact up-runtime-preflight command once"
up_preflight_line="$(cut -d: -f1 <<<"$up_preflight_match")"
pull_line="$(line_of_single_command "$compose_up_block" image-pull "$compose_pull")"
up_generate_line="$(line_of_single_command "$compose_up_block" up-secret-generation 'SCRIBE_REPAIR_LOCAL_TOKENS=true bash generate-secrets.sh')"
convergence_line="$(line_of_single_command "$compose_up_block" runtime-convergence "$runtime_convergence")"
up_line="$(line_of_single_command "$compose_up_block" compose-up "$compose_up")"
((
  up_preflight_line < pull_line &&
    pull_line < up_generate_line &&
    up_generate_line < convergence_line &&
    convergence_line < up_line
)) || fail "up must validate, retry the reviewed pull, synchronize secrets, conditionally converge stale runtime state, then run Compose"
grep -Fq $'\t@SCRIBE_REPAIR_LOCAL_TOKENS=true bash generate-secrets.sh' Makefile ||
  fail "local make up does not use the same full-lifecycle token repair"
[[ "$(grep -Fxc $'\t@bash generate-secrets.sh' Makefile || true)" -eq 1 ]] ||
  fail "partial local lifecycles must not opt into live application-token repair"

if rg -q -- '--volumes|docker[[:space:]]+(volume|system[[:space:]]+prune|container[[:space:]]+prune|network[[:space:]]+prune)' \
  terraform/rootfs/home/cloud-compose/scribe-compose-runtime-preflight.sh; then
  fail "Compose convergence must preserve every volume and unrelated Docker resource"
fi

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

first_ref="$(volume_names scribe prod canonical-v1 1111111111111111111111111111111111111111)"
second_ref="$(volume_names scribe prod canonical-v1 2222222222222222222222222222222222222222)"
[[ "$first_ref" == "$second_ref" ]] || fail "volume names changed across immutable source refs"

changed_workspace="$(volume_names scribe pr-75 canonical-v1 1111111111111111111111111111111111111111)"
[[ "$first_ref" != "$changed_workspace" ]] || fail "production and preview workspaces share volume names"

changed_generation="$(volume_names scribe prod canonical-v2 1111111111111111111111111111111111111111)"
[[ "$first_ref" != "$changed_generation" ]] || fail "persistence generations share volume names"

while IFS= read -r volume; do
  [[ "$volume" == scribe-prod-canonical-v1-* ]] || fail "unexpected production volume name: $volume"
done <<<"$first_ref"

echo "Persistence generation namespace contracts passed."
