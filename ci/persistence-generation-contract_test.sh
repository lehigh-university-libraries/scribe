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

# The cloud-compose checkout is revisioned, but its project directory and
# Compose namespace must remain stable for one Terraform workspace. These
# source assertions prevent a future refactor from silently accepting the
# module's commit-derived legacy defaults.
rg -q 'primary[[:space:]]*=[[:space:]]*"primary"' terraform/main.tf || fail "Terraform does not select the explicit primary Compose project"
rg -q 'project_dir[[:space:]]*=[[:space:]]*"/mnt/disks/data/scribe/\$\{local\.workspace_slug\}"' terraform/main.tf || fail "Terraform project_dir is not workspace-stable"
rg -q 'compose_project_name[[:space:]]*=[[:space:]]*"\$\{var\.name\}-\$\{local\.workspace_slug\}"' terraform/main.tf || fail "Terraform Compose namespace is not site/workspace-stable"

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
    docker compose -p "$project_name" -f docker-compose.yaml config --format json \
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
