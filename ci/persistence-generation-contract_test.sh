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

current_generation="canonical-v2"
prior_generation="canonical-v1"

# Every forward-entry default must select the schema-isolated generation for
# this rollout. Historical state fixtures remain on canonical-v1 so replay
# tests prove rollback and destroy use recorded inputs instead of these defaults.
rg -Fxq '      SCRIBE_DATA_GENERATION: canonical-v2' .github/workflows/terraform-deploy.yaml ||
  fail "the protected forward deploy does not select canonical-v2"
if rg -Fq '      SCRIBE_DATA_GENERATION: canonical-v1' .github/workflows/terraform-deploy.yaml; then
  fail "the protected forward deploy still selects canonical-v1"
fi
generation_variable_block="$(sed -n '/^variable "data_generation" {$/,/^}$/p' terraform/variables.tf)"
rg -Fxq '  default     = "canonical-v2"' <<<"$generation_variable_block" ||
  fail "the Terraform data_generation default is not canonical-v2"
rg -Fq 'contains(["canonical-v1", "canonical-v2"], var.data_generation)' <<<"$generation_variable_block" ||
  fail "Terraform accepts an unreviewed persistence generation"
rg -q '^data_generation[[:space:]]*=[[:space:]]*"canonical-v2"$' terraform/terraform.tfvars.example ||
  fail "the Terraform example does not select canonical-v2"
rg -Fq 'data_generation="${SCRIBE_DATA_GENERATION:-canonical-v2}"' terraform/deploy-local.sh ||
  fail "the local Terraform entry point does not default to canonical-v2"
rg -Fxq 'SCRIBE_DATA_GENERATION=canonical-v2' sample.env ||
  fail "new local stacks do not select canonical-v2"
for allowlist_boundary in \
  .github/workflows/terraform-deploy.yaml \
  terraform/deploy-local.sh \
  ci/resolve-rollback-inputs.sh \
  ci/resolve-destroy-inputs.sh; do
  rg -Fq 'canonical-v(1|2)' "$allowlist_boundary" ||
    fail "${allowlist_boundary} does not reject unreviewed canonical generations"
done

# The production cutover must create v2 alongside the recorded v1 queue graph.
# Original v1 addresses stay intact for old-source drift after rollback. The
# keyed forward graph slices an explicit ordered review list through the current
# generation, so future cutovers retain their immediate predecessor without
# accepting an attacker-selected resource fan-out.
rg -Fq 'reviewed_data_generations' terraform/main.tf ||
  fail "Terraform has no explicit ordered generation review list"
rg -Fq '["canonical-v1", "canonical-v2"]' terraform/main.tf ||
  fail "the reviewed generation list does not contain the v1-to-v2 cutover"
rg -Fq 'toset(slice(local.reviewed_data_generations, 1, local.data_generation_index + 1))' terraform/main.tf ||
  fail "the forward queue graph does not retain prior canonical generations"
rg -Fq 'google_pubsub_topic.transcription_jobs_forward[var.data_generation].name' terraform/main.tf ||
  fail "runtime topic configuration does not select the current generation"
rg -Fq 'google_pubsub_subscription.transcription_workers_forward[var.data_generation].name' terraform/main.tf ||
  fail "runtime subscription configuration does not select the current generation"
if [ -e terraform/pubsub_generation_moved.tf ]; then
  fail "the cutover moves canonical-v1 away from addresses understood by the rollback source"
fi

while IFS='|' read -r resource_type resource_name expected_reference; do
  resource_block="$(sed -n "/^resource \"${resource_type}\" \"${resource_name}\" {$/,/^}/p" terraform/main.tf)"
  [ -n "$resource_block" ] || fail "missing retained ${resource_type}.${resource_name}"
  if rg -q '^[[:space:]]*(count|for_each)[[:space:]]*=' <<<"$resource_block"; then
    fail "${resource_type}.${resource_name} changed its deployed singleton address"
  fi
  rg -Fq "$expected_reference" <<<"$resource_block" ||
    fail "${resource_type}.${resource_name} no longer targets canonical-v1"
done <<'EOF'
google_pubsub_topic|transcription_jobs|canonical-v1-transcription-jobs
google_pubsub_topic|transcription_jobs_dead_letter|canonical-v1-transcription-jobs-dlq
google_pubsub_subscription|transcription_workers|canonical-v1-transcription-workers
google_pubsub_subscription|transcription_dead_letter_monitor|canonical-v1-transcription-jobs-dlq-monitor
google_pubsub_topic_iam_member|transcription_jobs_publisher|google_pubsub_topic.transcription_jobs.name
google_pubsub_subscription_iam_member|transcription_workers_subscriber|google_pubsub_subscription.transcription_workers.name
google_pubsub_topic_iam_member|transcription_dead_letter_publisher|google_pubsub_topic.transcription_jobs_dead_letter.name
google_pubsub_subscription_iam_member|transcription_dead_letter_source_subscriber|google_pubsub_subscription.transcription_workers.name
EOF

while IFS='|' read -r terraform_file resource_name expected_reference; do
  resource_block="$(sed -n "/^resource \"google_monitoring_alert_policy\" \"${resource_name}\" {$/,/^}/p" "$terraform_file")"
  rg -Fq 'count = local.is_prod_workspace ? 1 : 0' <<<"$resource_block" ||
    fail "google_monitoring_alert_policy.${resource_name} changed its deployed count address"
  rg -Fq "$expected_reference" <<<"$resource_block" ||
    fail "google_monitoring_alert_policy.${resource_name} no longer monitors canonical-v1"
done <<'EOF'
terraform/main.tf|transcription_dead_letter_depth|google_pubsub_subscription.transcription_dead_letter_monitor.name
terraform/monitoring.tf|transcription_queue_age|google_pubsub_subscription.transcription_workers.name
EOF

while IFS='|' read -r terraform_file resource_type resource_name; do
  resource_block="$(sed -n "/^resource \"${resource_type}\" \"${resource_name}\" {$/,/^}/p" "$terraform_file")"
  rg -Fq 'for_each = local.forward_transcription_data_generations' <<<"$resource_block" ||
    rg -Fq 'for_each = local.forward_production_transcription_data_generations' <<<"$resource_block" ||
    fail "${resource_type}.${resource_name} is not part of the forward generation graph"
done <<'EOF'
terraform/main.tf|google_pubsub_topic|transcription_jobs_forward
terraform/main.tf|google_pubsub_topic|transcription_jobs_dead_letter_forward
terraform/main.tf|google_pubsub_subscription|transcription_workers_forward
terraform/main.tf|google_pubsub_subscription|transcription_dead_letter_monitor_forward
terraform/main.tf|google_pubsub_topic_iam_member|transcription_jobs_publisher_forward
terraform/main.tf|google_pubsub_subscription_iam_member|transcription_workers_subscriber_forward
terraform/main.tf|google_pubsub_topic_iam_member|transcription_dead_letter_publisher_forward
terraform/main.tf|google_pubsub_subscription_iam_member|transcription_dead_letter_source_subscriber_forward
terraform/main.tf|google_monitoring_alert_policy|transcription_dead_letter_depth_forward
terraform/monitoring.tf|google_monitoring_alert_policy|transcription_queue_age_forward
EOF

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

echo "Persistence generation namespace contracts passed."
