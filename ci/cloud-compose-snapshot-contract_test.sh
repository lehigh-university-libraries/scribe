#!/bin/sh

set -eu

ROOT_DIR="$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "cloud-compose snapshot contract failed: $*" >&2
  exit 1
}

module_root=""
for candidate in terraform/.terraform/modules/scribe/cloud-compose-*; do
  if [ -d "$candidate" ]; then
    module_root="$candidate"
    break
  fi
done
[ -n "$module_root" ] || fail "the pinned scribe module is not initialized; run terraform init first"

provider_main="${module_root}/providers/gcp/main.tf"
module_main="${module_root}/modules/gcp/main.tf"
module_variables="${module_root}/variables.tf"
for required_file in "$provider_main" "$module_main" "$module_variables"; do
  [ -r "$required_file" ] || fail "initialized module is missing ${required_file}"
done

scribe_module="$(sed -n '/^module "scribe" {/,/^}/p' terraform/main.tf)"
printf '%s\n' "$scribe_module" | grep -Eq 'production[[:space:]]*=[[:space:]]*local\.is_prod_workspace' ||
  fail "Scribe does not pass its production workspace decision to cloud-compose"
printf '%s\n' "$scribe_module" | grep -Eq 'enabled[[:space:]]*=[[:space:]]*var\.run_snapshots' ||
  fail "Scribe does not pass the reviewed snapshot flag to cloud-compose"
grep -Eq 'is_prod_workspace[[:space:]]*=[[:space:]]*terraform\.workspace[[:space:]]*==[[:space:]]*"prod"' terraform/main.tf ||
  fail "the production workspace predicate is not exact"

grep -Eq 'production[[:space:]]*=[[:space:]]*optional\(bool,[[:space:]]*false\)' "$module_variables" ||
  fail "the initialized cloud-compose production default changed"
grep -Eq 'production[[:space:]]*=[[:space:]]*local\.gcp_instance\.production' "$provider_main" ||
  fail "cloud-compose no longer forwards instance.production to its GCP module"
grep -Eq 'run_snapshots[[:space:]]*=[[:space:]]*local\.gcp_snapshots\.enabled' "$provider_main" ||
  fail "cloud-compose no longer forwards snapshots.enabled to its GCP module"
grep -Eq 'scheduled_snapshots_enabled[[:space:]]*=[[:space:]]*var\.production[[:space:]]*&&[[:space:]]*var\.run_snapshots' "$module_main" ||
  fail "the initialized cloud-compose snapshot gate changed"

gate_consumers="$(grep -Ec '(count|for_each)[[:space:]]*=[[:space:]]*local\.scheduled_snapshots_enabled' "$module_main")"
[ "$gate_consumers" -ge 4 ] || fail "cloud-compose snapshot resources are no longer consistently gated"

echo "cloud-compose production snapshot contracts passed."
